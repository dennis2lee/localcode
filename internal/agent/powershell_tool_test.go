package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"localcode/internal/config"
	"localcode/internal/sshterm"
	"localcode/internal/tools"
)

// The powershell tool's offer, what it asks under which rule, and what
// it answers. The terminals are sshterm's to test (local_test.go, and
// local_windows_test.go with the real thing); here a small fake shell
// stands in, so these run everywhere.

// psShell is a PowerShell as far as the tool can see: the integration's
// marks around a prompt, an echo, and "echo".
type psShell struct {
	inR, outR *io.PipeReader
	inW, outW *io.PipeWriter
	nonce     string
	mu        sync.Mutex
	got       strings.Builder
	done      chan struct{}
	once      sync.Once
}

var psNonce = regexp.MustCompile(`\$global:__lc_n='([0-9a-f]+)'`)

func newPSShell(argv []string) *psShell {
	s := &psShell{done: make(chan struct{})}
	s.inR, s.inW = io.Pipe()
	s.outR, s.outW = io.Pipe()
	if b, err := base64.StdEncoding.DecodeString(argv[len(argv)-1]); err == nil {
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
		}
		if m := psNonce.FindStringSubmatch(string(utf16.Decode(u))); m != nil {
			s.nonce = m[1]
		}
	}
	go s.run()
	return s
}

func (s *psShell) Read(p []byte) (int, error)  { return s.outR.Read(p) }
func (s *psShell) Write(p []byte) (int, error) { return s.inW.Write(p) }
func (s *psShell) Resize(int, int) error       { return nil }
func (s *psShell) Wait() (int, error)          { <-s.done; return 0, nil }
func (s *psShell) Close() error {
	s.once.Do(func() { s.inR.Close(); s.outW.Close(); close(s.done) })
	return nil
}

func (s *psShell) prompt(status int) {
	const p = `PS C:\work> `
	fmt.Fprintf(s.outW, "\x1b]133;D;%d;p=%s;lc=%s\x07%s", status, base64.StdEncoding.EncodeToString([]byte(p)), s.nonce, p)
}

func (s *psShell) run() {
	s.prompt(0)
	var line []byte
	buf := make([]byte, 256)
	for {
		n, err := s.inR.Read(buf)
		if err != nil {
			return
		}
		for _, b := range buf[:n] {
			s.mu.Lock()
			s.got.WriteByte(b)
			s.mu.Unlock()
			if b != '\r' {
				line = append(line, b)
				s.outW.Write([]byte{b})
				continue
			}
			fmt.Fprintf(s.outW, "\r\n\x1b]133;C;lc=%s\x07", s.nonce)
			cmd := strings.Fields(string(line))
			line = line[:0]
			status := 0
			switch {
			case len(cmd) == 0:
			case cmd[0] == "echo":
				fmt.Fprintf(s.outW, "%s\r\n", strings.Join(cmd[1:], " "))
			default:
				fmt.Fprintf(s.outW, "%s : The term is not recognized.\r\n", cmd[0])
				status = 1
			}
			s.prompt(status)
		}
	}
}

func (s *psShell) typed() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got.String()
}

// psLoop is a loop with a PowerShell manager of fake shells, and the tool
// in a registry whose questions are answered by asked.
func psLoop(t *testing.T, asked func(tools.Ask) bool) (*Loop, chan *psShell) {
	t.Helper()
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	started := make(chan *psShell, 8)
	m := sshterm.NewManager(sshterm.Options{Local: &sshterm.LocalShell{
		Path: "powershell.exe",
		Start: func(argv []string, dir string, cols, rows int) (sshterm.LocalProcess, error) {
			s := newPSShell(argv)
			started <- s
			return s, nil
		},
	}})
	t.Cleanup(func() { m.CloseAll("test over", true) })
	loop.PowerShell = m
	loop.ClosePowerShell = func() { m.CloseAll("PowerShell was turned off", false) }
	reg := tools.NewRegistry(func(_ context.Context, a tools.Ask) (bool, error) { return asked(a), nil })
	reg.Resolver = tools.ComposeResolver(
		func(ctx context.Context, toolName, subject string, static bool) tools.Decision {
			return tools.Decision(loop.Config.ResolvePermissionFor(ctx, toolName, subject, static))
		},
		NewPermissionPolicy(loop.Store, loop.Config).ToolsPolicy(),
	)
	reg.Register(NewPowerShellTool(loop))
	loop.Tools = reg
	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	return loop, started
}

func execPS(t *testing.T, loop *Loop, in map[string]any) tools.Result {
	t.Helper()
	b, _ := json.Marshal(in)
	return loop.Tools.Call(WithSessionID(context.Background(), "s1"), PowerShellToolName, b, "")
}

func TestPowerShellIsOfferedWhileOnWithAManagerToATurnSomebodyWatches(t *testing.T) {
	loop, _ := psLoop(t, func(tools.Ask) bool { return true })
	ctx := WithSessionID(context.Background(), "s1")
	offered := func(ctx context.Context) bool { return !loop.hiddenTools(ctx)[PowerShellToolName] }
	if offered(ctx) {
		t.Error("offered with the setting off")
	}
	loop.Config.SetPowerShellRuntime(true)
	if !offered(ctx) {
		t.Error("not offered with the setting on")
	}
	if offered(WithUnattended(ctx)) {
		t.Error("offered to an unattended turn")
	}
	if _, err := loop.Store.CreateSession("child", "s1", "general-purpose", false); err != nil {
		t.Fatal(err)
	}
	if offered(WithSessionID(context.Background(), "child")) {
		t.Error("offered to a sub-agent, whose terminals no panel shows")
	}
	// Anywhere but Windows there is no manager, and so no tool.
	loop.PowerShell = nil
	if offered(ctx) {
		t.Error("offered with no PowerShell manager")
	}
	res := execPS(t, loop, map[string]any{"action": "open"})
	if !res.Refused || !strings.Contains(res.Content, "not available") {
		t.Errorf("open with no manager: %+v", res)
	}
}

func TestThePowerShellToolOpensRunsAndAsksUnderItsOwnRules(t *testing.T) {
	var mu sync.Mutex
	var asks []tools.Ask
	loop, started := psLoop(t, func(a tools.Ask) bool {
		mu.Lock()
		defer mu.Unlock()
		asks = append(asks, a)
		return true
	})
	loop.Config.SetPowerShellRuntime(true)

	res := execPS(t, loop, map[string]any{"action": "open"})
	if res.IsError || !strings.Contains(res.Content, "Started: terminal t1, powershell.") || !strings.Contains(res.Content, "reports when each command ends") || !strings.Contains(res.Content, `PS C:\work>`) {
		t.Fatalf("open: %+v", res)
	}
	shell := <-started
	if len(asks) != 0 {
		t.Errorf("opening asked: %+v", asks)
	}

	res = execPS(t, loop, map[string]any{"action": "run", "command": "echo hello"})
	if res.IsError || !strings.Contains(res.Content, "Exit status 0") || !strings.HasSuffix(res.Content, "hello") {
		t.Fatalf("run: %+v", res)
	}
	res = execPS(t, loop, map[string]any{"action": "run", "command": "Get-Nothing"})
	if !strings.Contains(res.Content, "Exit status 1") {
		t.Fatalf("a failing run: %+v", res)
	}
	mu.Lock()
	if len(asks) != 2 || asks[0].Tool != config.PowerShellToolName || asks[0].Subject != "echo hello" {
		t.Errorf("asks for the runs: %+v", asks)
	}
	mu.Unlock()

	res = execPS(t, loop, map[string]any{"action": "key", "keys": "ctrl+u"})
	if res.IsError {
		t.Fatalf("key: %+v", res)
	}
	mu.Lock()
	if last := asks[len(asks)-1]; last.Tool != config.PowerShellInputToolName || last.Subject != "key ctrl+u" {
		t.Errorf("a key asked %+v", last)
	}
	mu.Unlock()
	if !strings.Contains(shell.typed(), "echo hello\r") {
		t.Errorf("the shell got %q", shell.typed())
	}

	res = execPS(t, loop, map[string]any{"action": "list"})
	if !strings.Contains(res.Content, "t1") || strings.Contains(res.Content, "SSH") {
		t.Errorf("list: %q", res.Content)
	}
	res = execPS(t, loop, map[string]any{"action": "read"})
	if !strings.Contains(res.Content, "Screen (") || !strings.Contains(res.Content, "hello") {
		t.Errorf("read: %q", res.Content)
	}
	res = execPS(t, loop, map[string]any{"action": "close", "terminal": "t1"})
	if res.IsError || !strings.Contains(res.Content, "Closed terminal t1") {
		t.Errorf("close: %+v", res)
	}
}

// A PowerShell line is not split as a bash line is: a deny rule found in
// any part still denies it, and an allow that splitting would decide is
// asked instead.
func TestAPowerShellLineIsDecidedAsAShellThatIsNotBash(t *testing.T) {
	asked := 0
	loop, _ := psLoop(t, func(tools.Ask) bool { asked++; return false })
	loop.Config.SetPowerShellRuntime(true)
	loop.Config.Permissions = map[string]config.ToolPermission{
		config.PowerShellToolName: {Rules: []config.PermissionRule{
			{Match: "Remove-Item *", Decision: config.DecisionDeny},
			{Match: "echo *", Decision: config.DecisionAllow},
		}},
	}
	if res := execPS(t, loop, map[string]any{"action": "open"}); res.IsError {
		t.Fatalf("open: %+v", res)
	}
	for _, cmd := range []string{"Remove-Item C:\\x", "echo a; Remove-Item C:\\x"} {
		res := execPS(t, loop, map[string]any{"action": "run", "command": cmd})
		if !res.Refused || !strings.Contains(res.Content, "denied") {
			t.Errorf("%q: %+v", cmd, res)
		}
	}
	if asked != 0 {
		t.Errorf("a denied line was asked about %d times", asked)
	}
	// An allow for "echo *" does not cover a line with more in it.
	res := execPS(t, loop, map[string]any{"action": "run", "command": "echo a; Get-ChildItem"})
	if !res.Refused || asked != 1 {
		t.Errorf("an allow decided by splitting the line: %+v, asked %d", res, asked)
	}
}

func TestThePowerShellCommandTurnsItOnAndOff(t *testing.T) {
	loop, started := psLoop(t, func(tools.Ask) bool { return true })
	sid := "s1"
	if out := replyTo(t, loop, sid, "/powershell on"); !strings.Contains(out, "powershell: on") || !strings.Contains(out, "PowerShell panel") {
		t.Errorf("/powershell on said %q", out)
	}
	if !loop.Config.PowerShellLive() {
		t.Fatal("not on")
	}
	if res := execPS(t, loop, map[string]any{"action": "open"}); res.IsError {
		t.Fatalf("open: %+v", res)
	}
	<-started
	if out := replyTo(t, loop, sid, "/powershell"); !strings.Contains(out, "powershell: off") || !strings.Contains(out, "closed") {
		t.Errorf("/powershell said %q", out)
	}
	if len(loop.PowerShell.LiveTerminals()) != 0 {
		t.Error("a terminal outlived the switch")
	}
	if out := replyTo(t, loop, sid, "/powershell maybe"); !strings.Contains(out, "usage") {
		t.Errorf("/powershell maybe said %q", out)
	}
	// Where there is no PowerShell the setting is kept, and says so.
	loop.PowerShell = nil
	if out := replyTo(t, loop, sid, "/powershell on"); !strings.Contains(out, "Windows only") {
		t.Errorf("/powershell on with no manager said %q", out)
	}
}

func TestThePowerShellToolSaysItIsPowerShell(t *testing.T) {
	d := NewPowerShellTool(nil).Description()
	for _, want := range []string{"PowerShell panel", "not bash", "open", "profile"} {
		if !strings.Contains(d, want) {
			t.Errorf("the description has no %q", want)
		}
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(NewPowerShellTool(nil).InputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"host", "user", "port"} {
		if _, ok := schema.Properties[field]; ok {
			t.Errorf("the schema has ssh's %q", field)
		}
	}
}
