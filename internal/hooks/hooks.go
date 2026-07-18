// Package hooks implements Claude Code-style lifecycle hooks: shell
// commands that run at fixed points (a tool about to run, a tool that
// just ran, a user prompt about to be sent, a turn finishing, a session
// starting) and can optionally block the action. Unlike permission rules
// (allow/ask/deny with no side effects), a hook is a real command — it can
// auto-format a file after an edit, log every tool call, page someone, or
// run arbitrary validation logic.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Event names — the config.json keys under "hooks". Kept snake_case to
// match this project's own JSON convention (Claude Code itself uses
// PascalCase for the same concepts: PreToolUse, PostToolUse,
// UserPromptSubmit, Stop, SessionStart).
const (
	EventPreToolUse       = "pre_tool_use"
	EventPostToolUse      = "post_tool_use"
	EventUserPromptSubmit = "user_prompt_submit"
	EventStop             = "stop"
	EventSessionStart     = "session_start"
)

// KnownEvents lists every event name Run recognizes, for config
// validation.
var KnownEvents = map[string]bool{
	EventPreToolUse:       true,
	EventPostToolUse:      true,
	EventUserPromptSubmit: true,
	EventStop:             true,
	EventSessionStart:     true,
}

// Hook is one shell command registered against an event. Matcher, if set,
// is a regular expression matched against the payload's "tool_name" field
// (meaningful for pre_tool_use/post_tool_use only — other events have no
// tool name, so a Matcher there simply never matches and the hook never
// runs; leave it empty for those events).
type Hook struct {
	Matcher string `json:"matcher,omitempty"`
	Command string `json:"command"`
}

// Config maps an event name to the ordered list of hooks registered for
// it.
type Config map[string][]Hook

// defaultTimeout bounds one hook's execution, so a hung script can't wedge
// the whole turn.
const defaultTimeout = 30 * time.Second

// Run executes every hook registered for event whose Matcher (if any)
// matches payload's "tool_name", in order, stopping at the first one that
// blocks. payload is marshaled to JSON and piped to each hook's stdin.
//
// A hook blocks the action by either exiting with status 2 (reason taken
// from stderr) or printing {"decision":"block","reason":"..."} as JSON on
// stdout — mirroring Claude Code's own hook contract. Any other outcome
// (zero exit, or a nonzero exit that isn't a block signal) lets the
// action proceed; a script's own failure is reported back as a warning,
// not treated as an implicit block, so a broken hook script can't lock
// the user out of their own tools.
func Run(ctx context.Context, cfg Config, event string, payload map[string]any) (blocked bool, reason string, warnings []error) {
	list := cfg[event]
	if len(list) == 0 {
		return false, "", nil
	}

	toolName, _ := payload["tool_name"].(string)
	data, err := json.Marshal(payload)
	if err != nil {
		return false, "", []error{fmt.Errorf("marshal hook payload: %w", err)}
	}

	for _, h := range list {
		if h.Matcher != "" {
			matched, err := regexp.MatchString(h.Matcher, toolName)
			if err != nil {
				warnings = append(warnings, fmt.Errorf("hook %q: invalid matcher %q: %w", h.Command, h.Matcher, err))
				continue
			}
			if !matched {
				continue
			}
		}

		hookCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
		cmd := exec.CommandContext(hookCtx, "sh", "-c", h.Command)
		cmd.Stdin = bytes.NewReader(data)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		cancel()

		var resp struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		_ = json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp)
		if resp.Decision == "block" {
			return true, resp.Reason, warnings
		}

		if runErr != nil {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 2 {
				r := strings.TrimSpace(stderr.String())
				if r == "" {
					r = fmt.Sprintf("hook %q exited with status 2", h.Command)
				}
				return true, r, warnings
			}
			warnings = append(warnings, fmt.Errorf("hook %q: %w (stderr: %s)", h.Command, runErr, strings.TrimSpace(stderr.String())))
		}
	}

	return false, "", warnings
}
