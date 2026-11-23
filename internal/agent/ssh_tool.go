package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"localcode/internal/config"
	"localcode/internal/sshterm"
	"localcode/internal/tools"
)

// The model working in SSH connections, shown in the SSH panel of the
// desktop window and the Web UI, where the person watches, types, holds
// a terminal, and answers what only they may answer: host keys,
// passwords, passphrases.
//
// Offered while the ssh setting is on, in a turn somebody can answer
// (not a scheduled one: a password question there would wait for
// nobody). Unlike the browser it does not need the panel on screen: a
// connection the model is working in should not vanish because the person
// looked at another conversation.
//
// It asks inside Execute rather than through the registry's gate, under
// three rule names: ssh_connect for where a connection goes, ssh for a
// command (resolved as a bash line is), and ssh_input for typed text and
// keys, asked every time. Asking there is what lets it look at the
// terminal again after the answer: a person who typed in it while the
// question was open has changed what the approval was for.

// SSHToolName is the tool's name.
const SSHToolName = config.SSHToolName

// sshOffered reports whether this turn is offered the ssh tool.
func (l *Loop) sshOffered(ctx context.Context) bool {
	if l.Config == nil || !l.Config.SSHLive() || l.SSH == nil || Unattended(ctx) {
		return false
	}
	_, ok := SessionIDFromContext(ctx)
	return ok
}

// SSHTool drives the conversation's SSH terminals.
type SSHTool struct {
	loop *Loop
}

// NewSSHTool returns the tool.
func NewSSHTool(loop *Loop) SSHTool { return SSHTool{loop: loop} }

func (SSHTool) Name() string { return SSHToolName }

func (SSHTool) Description() string {
	return `Work on other machines over SSH, in terminals shown to the user in the SSH panel of the app. The user sees the same screen and can type in it too. A terminal stays connected for as long as this conversation exists in the running app, between your turns, so a shell keeps its directory, variables and running programs.

Actions:
- connect {host, user?, port?}: open a connection and its shell. host may be an alias from the user's ~/.ssh/config. Host keys, passwords and passphrases are answered by the user in the panel; the call waits for them. Returns the terminal's id (t1, t2, ...).
- run {command, terminal?, timeout?}: type a one-line command and Enter, and wait until it finishes (timeout in seconds, default 120, at most 600). Returns its output and exit status. A command still running at the timeout keeps running: use wait, read, or key ctrl+c.
- type {text, enter?, terminal?}: type text into whatever runs in the terminal (an editor, a REPL, a prompt), then Enter when enter is true.
- key {keys, terminal?}: press keys, space-separated: "ctrl+c", "Enter", "Escape", "Up", "Tab", "q", "ctrl+d", "F1".
- read {terminal?, scrollback?}: the screen as text, and up to scrollback earlier lines.
- wait {terminal?, seconds?, for?}: wait until text for is on the screen, or the running command finishes (default 10 seconds, at most 300).
- list: this conversation's terminals and their state.
- close {terminal}: end a connection you opened.

terminal defaults to the one you used last.

Never ask the user for a password, passphrase or one-time code in the conversation, and never type one: when a command waits for one, tell the user to type it in the SSH panel. When the user is typing in a terminal or holds it, your input there is refused; say what you need and wait.`
}

func (SSHTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"action":{"type":"string","enum":["connect","run","type","key","read","wait","list","close"]},
"host":{"type":"string","description":"connect: a host name, address, or ~/.ssh/config alias; user@host and host:port are read too"},
"user":{"type":"string"},
"port":{"type":"integer"},
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

// RequiresPermission is false: the tool asks inside Execute, with the
// subject each action has (see the file comment). A deny rule for "ssh"
// with no subject ("*") still refuses every call at the gate.
func (SSHTool) RequiresPermission(json.RawMessage) bool { return false }

type sshInput struct {
	Action     string  `json:"action"`
	Host       string  `json:"host"`
	User       string  `json:"user"`
	Port       int     `json:"port"`
	Terminal   string  `json:"terminal"`
	Command    string  `json:"command"`
	Timeout    int     `json:"timeout"`
	Text       string  `json:"text"`
	Enter      bool    `json:"enter"`
	Keys       string  `json:"keys"`
	Scrollback int     `json:"scrollback"`
	Seconds    float64 `json:"seconds"`
	For        string  `json:"for"`
}

func (SSHTool) Describe(input json.RawMessage) string {
	var in sshInput
	_ = json.Unmarshal(input, &in)
	switch in.Action {
	case "connect":
		return "ssh: connect to " + in.Host
	case "run":
		return "ssh: run " + in.Command
	default:
		return "ssh: " + in.Action
	}
}

func sshError(format string, args ...any) tools.Result {
	return tools.Result{Content: fmt.Sprintf(format, args...), IsError: true}
}

func sshRefused(err error) tools.Result {
	return tools.Result{Content: err.Error(), IsError: true, Refused: true}
}

func (t SSHTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	var in sshInput
	if err := json.Unmarshal(input, &in); err != nil {
		return sshError("invalid input: %v", err)
	}
	sessionID, ok := SessionIDFromContext(ctx)
	if !ok {
		return sshError("ssh has no conversation to belong to")
	}
	if t.loop == nil || !t.loop.sshOffered(ctx) {
		return tools.Result{Content: "ssh is not available: it is off, or this turn has nobody to answer its questions", IsError: true, Refused: true}
	}
	switch in.Action {
	case "connect":
		return t.connect(ctx, sessionID, in)
	case "list":
		return t.list(sessionID)
	}
	term := t.terminal(sessionID, in.Terminal)
	if term == nil {
		if in.Terminal != "" {
			return sshError("no terminal %q in this conversation; list shows them", in.Terminal)
		}
		return sshError("this conversation has no SSH terminal; connect first")
	}
	if refusal := t.hostDenied(term); refusal != "" {
		return tools.Result{Content: refusal, IsError: true, Refused: true}
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
		return tools.Result{Content: formatRead(r)}
	case "wait":
		return t.wait(ctx, term, in)
	case "close":
		return t.close(sessionID, term)
	}
	return sshError("unknown action %q", in.Action)
}

func (t SSHTool) terminal(sessionID, id string) *sshterm.Terminal {
	m := t.loop.SSH
	if id == "" {
		return m.Default(sessionID)
	}
	return m.Get(sessionID, id)
}

// SSHConnectSubject is connectSubject, for the daemon's check just before
// a connection is made.
func SSHConnectSubject(r sshterm.Route) string { return connectSubject(r) }

// connectSubject is what ssh_connect rules match: the alias asked for
// when it is not the host itself, the target as user@host:port, and each
// jump host, so a rule names a host by either name.
func connectSubject(r sshterm.Route) string {
	var b strings.Builder
	if !strings.EqualFold(r.Asked.Host, r.Target.Host) {
		b.WriteString(r.Asked.Host + " ")
	}
	b.WriteString(r.Target.String())
	for i, h := range r.Hops {
		if i == 0 {
			b.WriteString(" via ")
		} else {
			b.WriteString(",")
		}
		b.WriteString(h.String())
	}
	return b.String()
}

// hostDenied is the refusal for a terminal whose host a deny rule for
// ssh_connect covers, whoever opened it: the rule keeps the model off
// that host, including through a connection the person made.
func (t SSHTool) hostDenied(term *sshterm.Terminal) string {
	if t.loop.Config == nil {
		return ""
	}
	r, err := t.loop.SSH.RouteTo(term.Spec)
	if err != nil {
		return ""
	}
	if t.loop.Config.DeniedAnywhere(config.SSHConnectToolName, connectSubject(r)) {
		return fmt.Sprintf("terminal %s is on %s, which a permission rule for %q denies", term.ID, r.Target.Host, config.SSHConnectToolName)
	}
	return ""
}

// printableHost refuses a host name that is not printable ASCII, so the
// question shown to the person says exactly what will be connected to.
func printableHost(s string) error {
	for _, r := range s {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%q: a host name is printable ASCII with no spaces (write an international name in its xn-- form)", s)
		}
	}
	return nil
}

func (t SSHTool) connect(ctx context.Context, sessionID string, in sshInput) tools.Result {
	host := strings.TrimSpace(in.Host)
	if host == "" {
		return sshError("connect needs a host")
	}
	if err := printableHost(host + in.User); err != nil {
		return sshError("%v", err)
	}
	spec := host
	if in.User != "" && !strings.Contains(host, "@") {
		spec = in.User + "@" + spec
	}
	if in.Port != 0 {
		if strings.Contains(spec, ":") && !strings.HasPrefix(spec, "[") && strings.Count(spec, ":") == 1 {
			return sshError("give the port once: in host or in port")
		}
		spec += ":" + strconv.Itoa(in.Port)
	}
	route, err := t.loop.SSH.RouteTo(spec)
	if err != nil {
		return sshError("%v", err)
	}
	subject := connectSubject(route)
	describe := "ssh: connect to " + subject
	if ok, why := t.loop.Tools.PermitNamed(ctx, config.SSHConnectToolName, subject, describe, false); !ok {
		return tools.Result{Content: why, IsError: true, Refused: true}
	}
	term, err := t.loop.SSH.Open(sessionID, spec, sshterm.ByModel)
	if err != nil {
		return sshError("%v", err)
	}
	if err := term.WaitConnected(ctx); err != nil {
		if ctx.Err() != nil {
			t.loop.SSH.Remove(sessionID, term.ID, "the turn was stopped while connecting")
			return tools.CancelledResult()
		}
		return sshError("%s: %v", spec, err)
	}
	// The shell's first prompt, and its integration line, before the
	// first run.
	settle, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for settle.Err() == nil {
		info := term.Info()
		if info.Integrated || info.Shell != "" && info.Shell != "bash" && info.Shell != "zsh" {
			break
		}
		select {
		case <-term.Changed():
		case <-settle.Done():
		}
	}
	r, err := term.Read(0, true)
	if err != nil {
		return tools.Result{Content: fmt.Sprintf("Connected: terminal %s, %s. %v", term.ID, term.Info().Target, err)}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Connected: terminal %s, %s (%s).\n", term.ID, r.Info.Target, r.Info.Server)
	b.WriteString(integrationNote(r.Info))
	b.WriteString("\n\n")
	b.WriteString(formatScreen(r))
	return tools.Result{Content: b.String()}
}

func integrationNote(i sshterm.Info) string {
	if i.Integrated {
		return fmt.Sprintf("The shell (%s) reports when each command ends, so run returns exit statuses.", i.Shell)
	}
	shell := i.Shell
	if shell == "" {
		shell = "the shell"
	}
	return fmt.Sprintf("%s does not report when a command ends (only bash and zsh do, through a line localcode typed into them), so run returns when output stops, with no exit status.", shell)
}

// remotePOSIX reports whether the terminal's shell splits a command line
// as bash does, which an allow rule decided segment by segment relies on.
func remotePOSIX(i sshterm.Info) bool {
	return i.Integrated && (i.Shell == "bash" || i.Shell == "zsh")
}

func (t SSHTool) run(ctx context.Context, term *sshterm.Terminal, in sshInput) tools.Result {
	if err := sshterm.CheckCommand(in.Command); err != nil {
		return sshError("%v", err)
	}
	timeout := 120 * time.Second
	if in.Timeout > 0 {
		timeout = time.Duration(min(in.Timeout, 600)) * time.Second
	}
	info := term.Info()
	gen := term.PersonGen()
	describe := fmt.Sprintf("ssh %s (%s): run %s\nA grant for this command covers it on every host.", info.Target, term.ID, in.Command)
	ok, why := t.loop.Tools.PermitNamed(config.WithRemoteShell(ctx, remotePOSIX(info)), config.SSHToolName, in.Command, describe, false)
	if !ok {
		return tools.Result{Content: why, IsError: true, Refused: true}
	}
	res, err := term.Run(ctx, in.Command, timeout, gen)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return tools.CancelledResult()
		}
		return sshRefused(err)
	}
	return tools.Result{Content: formatRun(term.ID, res, timeout)}
}

func formatRun(id string, res sshterm.RunResult, timeout time.Duration) string {
	var b strings.Builder
	switch {
	case res.Ended != "":
		fmt.Fprintf(&b, "Terminal %s ended while the command ran: %s.", id, res.Ended)
	case res.Alt:
		b.WriteString("The command took over the screen (a full-screen program). Use read to see it, key to work it, and wait for it to end.")
	case res.Asking:
		b.WriteString("Output stopped at what may be a question for input (a password prompt, a yes/no). If it asks for a password, passphrase or code, tell the user to type it in the SSH panel; otherwise answer with type or key.")
	case res.TimedOut:
		fmt.Fprintf(&b, "Still running after %s; it keeps running. Use wait, read, or key ctrl+c.", timeout)
	case res.Lost:
		b.WriteString("The shell stopped reporting when commands end (a nested shell such as sudo -i, su, ssh or docker exec does not). The command may have finished; the exit status is not known.")
	case res.Finished:
		fmt.Fprintf(&b, "Exit status %s (%s).", res.Status, res.Took)
	default:
		fmt.Fprintf(&b, "Output stopped (%s); the shell does not report when commands end, so the exit status is not known.", res.Took)
	}
	if res.Person {
		b.WriteString(" The user typed in the terminal while the command ran.")
	}
	if res.Output != "" {
		b.WriteString("\n\n" + res.Output)
	}
	return b.String()
}

func (t SSHTool) typeText(ctx context.Context, term *sshterm.Terminal, in sshInput) tools.Result {
	if in.Text == "" && !in.Enter {
		return sshError("type needs text")
	}
	info := term.Info()
	gen := term.PersonGen()
	subject := "type " + in.Text
	shown := in.Text
	if in.Enter {
		subject += "\n"
		shown += " then Enter"
	}
	describe := fmt.Sprintf("ssh %s (%s): type %q", info.Target, term.ID, shown)
	if ok, why := t.loop.Tools.PermitNamed(ctx, config.SSHInputToolName, subject, describe, true); !ok {
		return tools.Result{Content: why, IsError: true, Refused: true}
	}
	if err := term.Type(in.Text, in.Enter, gen); err != nil {
		return sshRefused(err)
	}
	return t.afterInput(ctx, term, "Typed.")
}

func (t SSHTool) key(ctx context.Context, term *sshterm.Terminal, in sshInput) tools.Result {
	if strings.TrimSpace(in.Keys) == "" {
		return sshError("key needs keys")
	}
	if _, err := sshterm.ParseKeys(in.Keys); err != nil {
		return sshError("%v", err)
	}
	info := term.Info()
	gen := term.PersonGen()
	describe := fmt.Sprintf("ssh %s (%s): press %s", info.Target, term.ID, in.Keys)
	if ok, why := t.loop.Tools.PermitNamed(ctx, config.SSHInputToolName, "key "+in.Keys, describe, true); !ok {
		return tools.Result{Content: why, IsError: true, Refused: true}
	}
	if err := term.Keys(in.Keys, gen); err != nil {
		return sshRefused(err)
	}
	return t.afterInput(ctx, term, "Pressed.")
}

// afterInput waits a moment for the screen to answer what was typed, and
// returns it.
func (t SSHTool) afterInput(ctx context.Context, term *sshterm.Terminal, what string) tools.Result {
	quiet, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	for {
		select {
		case <-term.Changed():
			continue
		case <-time.After(300 * time.Millisecond):
		case <-quiet.Done():
		}
		break
	}
	r, err := term.Read(0, true)
	if err != nil {
		return tools.Result{Content: what + " " + err.Error()}
	}
	return tools.Result{Content: what + "\n\n" + formatScreen(r)}
}

func (t SSHTool) wait(ctx context.Context, term *sshterm.Terminal, in sshInput) tools.Result {
	d := 10 * time.Second
	if in.Seconds > 0 {
		d = time.Duration(min(in.Seconds, 300) * float64(time.Second))
	}
	met, err := term.Wait(ctx, d, in.For)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return tools.CancelledResult()
		}
		return sshRefused(err)
	}
	r, err := term.Read(0, true)
	if err != nil {
		return sshRefused(err)
	}
	head := "Waited " + d.String() + "; nothing changed what was waited for."
	if met {
		head = "Done waiting."
		if in.For != "" {
			head = fmt.Sprintf("%q is on the screen.", in.For)
		}
	}
	return tools.Result{Content: head + "\n\n" + formatRead(r)}
}

func (t SSHTool) close(sessionID string, term *sshterm.Terminal) tools.Result {
	info := term.Info()
	if info.Held {
		return sshRefused(sshterm.ErrHeld)
	}
	if info.Opener == sshterm.ByPerson {
		return sshRefused(fmt.Errorf("terminal %s was opened by the user; ask them to close it", term.ID))
	}
	t.loop.SSH.Remove(sessionID, term.ID, "closed by the model")
	return tools.Result{Content: "Closed terminal " + term.ID + "."}
}

func (t SSHTool) list(sessionID string) tools.Result {
	terms := t.loop.SSH.List(sessionID)
	if len(terms) == 0 {
		return tools.Result{Content: "No SSH terminals in this conversation."}
	}
	var b strings.Builder
	for _, term := range terms {
		b.WriteString(describeTerminal(term.Info()) + "\n")
	}
	return tools.Result{Content: strings.TrimRight(b.String(), "\n")}
}

func describeTerminal(i sshterm.Info) string {
	where := i.Target
	if where == "" {
		where = i.Spec
	}
	parts := []string{i.ID, where, string(i.State)}
	if i.State == sshterm.StateEnded && i.Reason != "" {
		parts = append(parts, "("+i.Reason+")")
	}
	if i.Question != nil {
		parts = append(parts, "waiting for the user to answer in the SSH panel")
	}
	if i.Held {
		parts = append(parts, "held by the user")
	}
	if i.Running {
		parts = append(parts, "a command is running")
	}
	if i.Alt {
		parts = append(parts, "a full-screen program is running")
	}
	if i.Opener == sshterm.ByPerson {
		parts = append(parts, "opened by the user")
	}
	return strings.Join(parts, ", ")
}

func formatScreen(r sshterm.ReadResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Screen (%dx%d, cursor at line %d, column %d):\n", r.Info.Cols, r.Info.Rows, r.CursorY+1, r.CursorX+1)
	b.WriteString(strings.Join(r.Screen, "\n"))
	return b.String()
}

func formatRead(r sshterm.ReadResult) string {
	var b strings.Builder
	b.WriteString(describeTerminal(r.Info))
	if r.RunningFor > 0 {
		fmt.Fprintf(&b, " for %s", r.RunningFor)
	}
	b.WriteString(".\n")
	if r.Info.Title != "" {
		fmt.Fprintf(&b, "Title: %s\n", r.Info.Title)
	}
	if len(r.Scrollback) > 0 {
		fmt.Fprintf(&b, "Earlier lines (%d):\n%s\n", len(r.Scrollback), strings.Join(r.Scrollback, "\n"))
	}
	b.WriteString(formatScreen(r))
	return b.String()
}
