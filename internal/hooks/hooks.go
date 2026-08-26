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

	"localcode/internal/shell"
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

	// The points a turn passes through that are not a tool call. A tool
	// hook can see everything a tool does and nothing else, which leaves
	// the expensive half of an agent session unobservable and
	// ungovernable: the model call itself, the decision to hand work to a
	// sub-agent, the history being summarized, the switch to another model
	// when one will not answer.
	//
	// EventPreModel runs before each provider call. Blocking it refuses
	// the request, which is the one place a policy can stop a turn before
	// anything is sent anywhere. It is also the injection point: a hook
	// that prints {"context":"..."} has that text appended to the system
	// prompt for that call.
	EventPreModel = "pre_model"
	// EventPostModel runs after each response. Fire and forget: the reply
	// has already arrived, so there is nothing left to block.
	EventPostModel = "post_model"
	// EventDelegate runs before a sub-agent is started. Blocking it
	// refuses the delegation, which is a real control: "no agent of mine
	// spawns another" is enforceable here in a way a prompt cannot be.
	EventDelegate = "delegate"
	// EventCompact runs after the history has been summarized or trimmed.
	EventCompact = "compact"
	// EventRetry runs when a turn moves to another model after one would
	// not answer.
	EventRetry = "retry"
)

// KnownEvents lists every event name Run recognizes, for config
// validation.
var KnownEvents = map[string]bool{
	EventPreToolUse:       true,
	EventPostToolUse:      true,
	EventUserPromptSubmit: true,
	EventStop:             true,
	EventSessionStart:     true,
	EventPreModel:         true,
	EventPostModel:        true,
	EventDelegate:         true,
	EventCompact:          true,
	EventRetry:            true,
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
	out := RunOutcome(ctx, cfg, event, payload)
	return out.Blocked, out.Reason, out.Warnings
}

// Outcome is everything running a hook list produced.
//
// Run above is the older, narrower view of the same thing, kept because
// most call sites only ever ask "was this blocked?". Context is the part
// that needs the wider one: a hook can hand text back to be added to the
// request, which is what makes pre_model an injection point rather than
// only a veto.
type Outcome struct {
	Blocked  bool
	Reason   string
	Context  []string
	Warnings []error
}

// RunOutcome is Run with the hooks' own output returned as well.
func RunOutcome(ctx context.Context, cfg Config, event string, payload map[string]any) (out Outcome) {
	list := cfg[event]
	if len(list) == 0 {
		return out
	}

	toolName, _ := payload["tool_name"].(string)
	data, err := json.Marshal(payload)
	if err != nil {
		out.Warnings = []error{fmt.Errorf("marshal hook payload: %w", err)}
		return out
	}

	for _, h := range list {
		if h.Matcher != "" {
			// Anchored to the full tool name (like Claude Code's matchers):
			// "bash" matches only the bash tool, not every tool whose name
			// happens to contain "bash". Alternation ("bash|edit") and
			// patterns ("mcp__github__.*") still work as expected.
			matched, err := regexp.MatchString("^(?:"+h.Matcher+")$", toolName)
			if err != nil {
				out.Warnings = append(out.Warnings, fmt.Errorf("hook %q: invalid matcher %q: %w", h.Command, h.Matcher, err))
				continue
			}
			if !matched {
				continue
			}
		}

		hookCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
		cmd := shell.Command(hookCtx, h.Command)
		cmd.Stdin = bytes.NewReader(data)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		cancel()

		var resp struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
			Context  string `json:"context"`
		}
		_ = json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp)
		if resp.Context != "" {
			out.Context = append(out.Context, resp.Context)
		}
		if resp.Decision == "block" {
			out.Blocked, out.Reason = true, resp.Reason
			return out
		}

		if runErr != nil {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 2 {
				r := strings.TrimSpace(stderr.String())
				if r == "" {
					r = fmt.Sprintf("hook %q exited with status 2", h.Command)
				}
				out.Blocked, out.Reason = true, r
				return out
			}
			out.Warnings = append(out.Warnings, fmt.Errorf("hook %q: %w (stderr: %s)", h.Command, runErr, strings.TrimSpace(stderr.String())))
		}
	}

	return out
}
