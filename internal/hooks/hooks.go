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

	// Timeout bounds this hook in seconds, or 0 for the 30-second
	// default. A check that has to look at a large tree, or ask
	// something over a network, does not fit in a number chosen for
	// everything; the alternative to a per-hook setting was writing the
	// hook to exit early and let the tool through, which is a guard that
	// gives up rather than one that takes longer.
	Timeout int `json:"timeout,omitempty"`

	// FailClosed blocks the action when this hook does not finish —
	// killed at its timeout, or unable to start at all.
	//
	// Off by default, and the default is a real decision rather than
	// inertia. A hook that cannot run and blocks everything locks
	// somebody out of their own tools in the middle of a session, which
	// is the more common accident and the more damaging one; a hook that
	// cannot run and allows leaves a guard silently not guarding, which
	// matters to the smaller number of people who deliberately wrote
	// one. So the safe-for-most default stays, the other is one line of
	// config away, and a timeout now says loudly that the hook never got
	// to decide rather than reporting it as an ordinary script failure.
	//
	// It covers not-finishing only. A hook that ran and exited nonzero
	// has decided: the contract says exit 2 to block, and anything else
	// is a broken script rather than a veto. Treating that as a block
	// would make every bug in a hook a lockout.
	FailClosed bool `json:"fail_closed,omitempty"`
}

// timeout is how long this hook may take.
func (h Hook) timeout() time.Duration {
	if h.Timeout > 0 {
		return time.Duration(h.Timeout) * time.Second
	}
	return defaultTimeout
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
// dir is the directory the hook commands run in: the workspace of the
// session this event is about. It is a parameter rather than something
// read from ctx because a hook is a shell command and where a shell
// command runs is not a detail — passing it makes every call site name
// the project it is calling about, and there is no way to add a new one
// without answering the question. Empty means the process's own working
// directory, which is what every hook used to get: a `git status` or a
// `./scripts/check.sh` ran wherever the daemon was started rather than in
// the project whose tool call had just triggered it.
//
// A hook blocks the action by either exiting with status 2 (reason taken
// from stderr) or printing {"decision":"block","reason":"..."} as JSON on
// stdout — mirroring Claude Code's own hook contract. Any other outcome
// (zero exit, or a nonzero exit that isn't a block signal) lets the
// action proceed; a script's own failure is reported back as a warning,
// not treated as an implicit block, so a broken hook script can't lock
// the user out of their own tools.
func Run(ctx context.Context, cfg Config, event, dir string, payload map[string]any) (blocked bool, reason string, warnings []error) {
	out := RunOutcome(ctx, cfg, event, dir, payload)
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
func RunOutcome(ctx context.Context, cfg Config, event, dir string, payload map[string]any) (out Outcome) {
	list := cfg[event]
	if len(list) == 0 {
		return out
	}

	toolName, _ := payload["tool_name"].(string)
	if dir != "" {
		// Told as well as applied. A hook that shells out somewhere else,
		// or writes a line to a log shared by several projects, still has
		// to be able to say which project it was called about, and the
		// answer is not derivable from anything else on stdin.
		//
		// Copied rather than written into: the caller built this map for
		// this call, and a function that quietly adds a key to its
		// argument is one that surprises the next caller who reuses one.
		with := make(map[string]any, len(payload)+1)
		for k, v := range payload {
			with[k] = v
		}
		with["cwd"] = dir
		payload = with
	}
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

		hookCtx, cancel := context.WithTimeout(ctx, h.timeout())
		cmd := shell.Command(hookCtx, h.Command)
		cmd.Dir = dir
		cmd.Stdin = bytes.NewReader(data)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		// Read before cancel, not after. A context keeps its first cause,
		// so this would be right either way, and depending on that is
		// depending on a detail of the standard library for a line whose
		// whole job is to tell two failures apart.
		timedOut := hookCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil
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
			// Killed at its timeout is a different thing from a script
			// that ran and failed, and it used to be reported as the
			// same thing: "signal: killed", in a warning, with the tool
			// going ahead. The hook never reached a decision, and that
			// is what the message has to say — and what fail_closed acts
			// on.
			//
			// hookCtx rather than the error, because a killed process
			// reports a signal rather than a deadline.
			if timedOut {
				note := fmt.Sprintf("hook %q did not finish within %s, so it never decided",
					h.Command, h.timeout())
				if h.FailClosed {
					out.Blocked, out.Reason = true, note+" (fail_closed)"
					return out
				}
				out.Warnings = append(out.Warnings, errors.New(note+"; the action went ahead"))
				continue
			}
			if h.FailClosed && errors.Is(runErr, exec.ErrNotFound) {
				out.Blocked, out.Reason = true, fmt.Sprintf("hook %q could not be started, so it never decided (fail_closed)", h.Command)
				return out
			}
			out.Warnings = append(out.Warnings, fmt.Errorf("hook %q: %w (stderr: %s)", h.Command, runErr, strings.TrimSpace(stderr.String())))
		}
	}

	return out
}
