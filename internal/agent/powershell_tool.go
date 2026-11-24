package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"localcode/internal/config"
	"localcode/internal/sshterm"
	"localcode/internal/tools"
)

// The model working in PowerShell on the computer localcode runs on, in
// terminals shown in the PowerShell panel, where the person watches and
// types too. Windows only: the daemon has no PowerShell manager anywhere
// else, so the tool is not offered there.
//
// The same actions as the ssh tool, and the same code once there is a
// terminal (termTool): only opening one differs. It asks inside Execute,
// under two rule names: powershell for a command, decided as a line of a
// shell that does not split lines as bash does, and powershell_input for
// typed text and keys, asked every time. Opening a terminal asks nothing:
// it starts the person's own PowerShell, profile and all, and every
// command typed into it asks.

// PowerShellToolName is the tool's name.
const PowerShellToolName = config.PowerShellToolName

var powershellKind = termKind{
	tool:    config.PowerShellToolName,
	input:   config.PowerShellInputToolName,
	panel:   "PowerShell panel",
	manager: func(l *Loop) *sshterm.Manager { return l.PowerShell },
	posix:   func(sshterm.Info) bool { return false },
	where:   func(sshterm.Info) string { return "PowerShell" },
}

// powershellOffered reports whether this turn is offered the powershell
// tool: the setting on, a manager to hold the terminals (Windows), and a
// turn somebody can answer, in the conversation the panel shows.
func (l *Loop) powershellOffered(ctx context.Context) bool {
	if l.Config == nil || !l.Config.PowerShellLive() || l.PowerShell == nil || Unattended(ctx) {
		return false
	}
	id, ok := SessionIDFromContext(ctx)
	if !ok {
		return false
	}
	// A delegated sub-agent works in a session of its own, which no panel
	// shows.
	if l.Store != nil {
		if sess, err := l.Store.Get(id); err == nil && sess.ParentID != "" {
			return false
		}
	}
	return true
}

// PowerShellTool drives the conversation's PowerShell terminals.
type PowerShellTool struct {
	termTool
}

// NewPowerShellTool returns the tool.
func NewPowerShellTool(loop *Loop) PowerShellTool {
	return PowerShellTool{termTool{loop: loop, kind: &powershellKind}}
}

func (PowerShellTool) Name() string { return PowerShellToolName }

func (PowerShellTool) Description() string {
	return `Work in PowerShell on the computer localcode runs on, in terminals shown to the user in the PowerShell panel of the app. The user sees the same screen and can type in it too. A terminal stays open for as long as this conversation exists in the running app, between your turns, so the shell keeps its directory, variables and running programs. It starts with the user's PowerShell profile, in this conversation's working directory.

Actions:
- open: start a PowerShell terminal. Returns its id (t1, t2, ...) and its screen.
- run {command, terminal?, timeout?}: type a one-line command and Enter, and wait until it finishes (timeout in seconds, default 120, at most 600). Returns its output and exit status: a program's own exit code, 1 for a cmdlet that failed, 0 otherwise. A command still running at the timeout keeps running: use wait, read, or key ctrl+c.
- type {text, enter?, terminal?}: type text into whatever runs in the terminal (a program's question, a REPL, Read-Host), then Enter when enter is true. At the shell's own prompt, commands go through run: type there takes no line end, and key only line-editing keys.
- key {keys, terminal?}: press keys, space-separated: "ctrl+c", "Enter", "Escape", "Up", "Tab", "y".
- read {terminal?, scrollback?}: the screen as text, and up to scrollback earlier lines.
- wait {terminal?, seconds?, for?}: wait until text for is on the screen, or the running command finishes (default 10 seconds, at most 300).
- list: this conversation's terminals and their state.
- close {terminal}: end a terminal you opened.

terminal defaults to the one you used last.

Commands are PowerShell, not bash: Get-ChildItem, Select-String, $env:NAME, "a; b" to run two. Never ask the user for a password or code in the conversation, and never type one: when a command waits for one, tell the user to type it in the PowerShell panel. When the user is typing in a terminal or holds it, your input there is refused; say what you need and wait.`
}

func (PowerShellTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"action":{"type":"string","enum":["open","run","type","key","read","wait","list","close"]},
"terminal":{"type":"string","description":"a terminal id such as t1; the last one used when left out"},
"command":{"type":"string"},
"timeout":{"type":"integer","description":"run: seconds to wait, default 120, at most 600"},
"text":{"type":"string"},
"enter":{"type":"boolean"},
"keys":{"type":"string"},
"scrollback":{"type":"integer","description":"read: earlier lines to include, at most 500"},
"seconds":{"type":"number"},
"for":{"type":"string","description":"wait: text to wait for on the screen"}
},"required":["action"]}`)
}

// RequiresPermission is false: the tool asks inside Execute (see the file
// comment). A deny rule for "powershell" with no subject ("*") still
// refuses every call at the gate.
func (PowerShellTool) RequiresPermission(json.RawMessage) bool { return false }

func (PowerShellTool) Describe(input json.RawMessage) string {
	var in sshInput
	_ = json.Unmarshal(input, &in)
	if in.Action == "run" {
		return "powershell: run " + in.Command
	}
	return "powershell: " + in.Action
}

func (t PowerShellTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	var in sshInput
	if err := json.Unmarshal(input, &in); err != nil {
		return sshError("invalid input: %v", err)
	}
	sessionID, ok := SessionIDFromContext(ctx)
	if !ok {
		return sshError("powershell has no conversation to belong to")
	}
	if t.loop == nil || !t.loop.powershellOffered(ctx) {
		return tools.Result{Content: "powershell is not available: it is off, this is not Windows, or this turn has nobody to watch it", IsError: true, Refused: true}
	}
	switch in.Action {
	case "open":
		return t.open(ctx, sessionID)
	case "list":
		return t.list(sessionID)
	}
	term := t.terminal(sessionID, in.Terminal)
	if term == nil {
		if in.Terminal != "" {
			return sshError("no terminal %q in this conversation; list shows them", in.Terminal)
		}
		return sshError("this conversation has no PowerShell terminal; open one first")
	}
	switch in.Action {
	case "run":
		return t.run(ctx, term, in)
	case "type":
		return t.typeText(ctx, term, in)
	case "key":
		return t.key(ctx, term, in)
	case "read":
		n := max(0, min(500, in.Scrollback))
		r, err := term.Read(n, true)
		if err != nil {
			return sshRefused(err)
		}
		return tools.Result{Content: t.kind.formatRead(r)}
	case "wait":
		return t.wait(ctx, term, in)
	case "close":
		return t.close(sessionID, term)
	}
	return sshError("unknown action %q", in.Action)
}

// open starts a terminal and returns its first screen, once the shell
// has drawn its first prompt: that is the screen the model types into.
func (t PowerShellTool) open(ctx context.Context, sessionID string) tools.Result {
	m := t.manager()
	term, err := m.Open(sessionID, "PowerShell", sshterm.ByModel)
	if err != nil {
		return sshError("%v", err)
	}
	if err := term.WaitConnected(ctx); err != nil {
		if ctx.Err() != nil {
			m.Remove(sessionID, term.ID, "the turn was stopped while starting")
			return tools.CancelledResult()
		}
		return sshError("PowerShell: %v", err)
	}
	// A profile that loads modules takes seconds.
	settle, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for settle.Err() == nil {
		if info := term.Info(); info.Integrated || info.State != sshterm.StateConnected {
			break
		}
		select {
		case <-term.Changed():
		case <-settle.Done():
		}
	}
	term.WaitPrompt(settle, 3*time.Second)
	r, err := term.Read(0, true)
	if err != nil {
		return tools.Result{Content: fmt.Sprintf("Started: terminal %s. %v", term.ID, err)}
	}
	if r.Info.State == sshterm.StateEnded {
		return sshError("PowerShell ended as it started: %s", r.Info.Reason)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Started: terminal %s, %s.\n", term.ID, r.Info.Shell)
	if r.Info.Integrated {
		b.WriteString("The shell reports when each command ends, so run returns exit statuses.")
	} else {
		b.WriteString("The shell did not report its first prompt in time (its profile may replace the prompt), so run returns when output stops, with no exit status.")
	}
	b.WriteString("\n\n")
	b.WriteString(formatScreen(r))
	return tools.Result{Content: b.String()}
}
