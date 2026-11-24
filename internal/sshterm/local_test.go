package sshterm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

var isWindows = runtime.GOOS == "windows"

// A local terminal's own code: how its shell is started, how it is told
// to report the end of a command, how it is resized and ended. What
// happens in the terminal after that is the code the SSH terminals run,
// tested in terminal_test.go and the rest. These run on every platform,
// against a fake PowerShell; local_windows_test.go runs the real ones
// under a real pseudo console.

// fakePS is a PowerShell as far as a terminal can see: it reads its
// nonce from the integration it was started with, draws a prompt after
// the D mark, echoes what is typed, prints the C mark when a line is
// read, and runs a handful of commands.
type fakePS struct {
	argv []string
	dir  string

	inR  *io.PipeReader
	inW  *io.PipeWriter
	outR *io.PipeReader
	outW *io.PipeWriter

	nonce   string
	noMarks bool // a shell without the integration
	// early sends the marks ahead of the text before them, as Windows
	// Server 2022's pseudo console did: "both" sends C and D before the
	// command's echo, "d" sends D before its output.
	early string
	// promptText is the prompt drawn after each D mark; "" draws
	// `PS C:\work> `.
	promptText string
	delay      time.Duration // before the first prompt: a profile loading
	wmu        sync.Mutex
	mu         sync.Mutex
	sizes      [][2]int
	closed     bool
	exitCode   int
	exited     chan struct{}
	exitOnce   sync.Once
}

var nonceInScript = regexp.MustCompile(`\$global:__lc_n='([0-9a-f]+)'`)

// decodePS is -EncodedCommand read back.
func decodePS(t interface{ Fatalf(string, ...any) }, enc string) string {
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(b)%2 != 0 {
		t.Fatalf("not an encoded command: %q (%v)", enc, err)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

func newFakePS(argv []string, dir string) *fakePS {
	f := &fakePS{argv: argv, dir: dir, exited: make(chan struct{})}
	f.inR, f.inW = io.Pipe()
	f.outR, f.outW = io.Pipe()
	for i, a := range argv {
		if a == "-EncodedCommand" && i+1 < len(argv) {
			if b, err := base64.StdEncoding.DecodeString(argv[i+1]); err == nil {
				u := make([]uint16, len(b)/2)
				for j := range u {
					u[j] = uint16(b[2*j]) | uint16(b[2*j+1])<<8
				}
				if m := nonceInScript.FindStringSubmatch(string(utf16.Decode(u))); m != nil {
					f.nonce = m[1]
				}
			}
		}
	}
	return f
}

func (f *fakePS) Read(p []byte) (int, error)  { return f.outR.Read(p) }
func (f *fakePS) Write(p []byte) (int, error) { return f.inW.Write(p) }

func (f *fakePS) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sizes = append(f.sizes, [2]int{cols, rows})
	return nil
}

func (f *fakePS) Wait() (int, error) {
	<-f.exited
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exitCode, nil
}

func (f *fakePS) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	f.exit(1)
	return nil
}

func (f *fakePS) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakePS) lastSize() [2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sizes) == 0 {
		return [2]int{}
	}
	return f.sizes[len(f.sizes)-1]
}

func (f *fakePS) exit(code int) {
	f.exitOnce.Do(func() {
		f.mu.Lock()
		f.exitCode = code
		f.mu.Unlock()
		f.inR.Close()
		f.outW.Close()
		close(f.exited)
	})
}

func (f *fakePS) write(s string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	_, _ = io.WriteString(f.outW, s)
}

func (f *fakePS) mark(kind string, status int) string {
	if f.noMarks || f.nonce == "" {
		return ""
	}
	if kind == "C" {
		return "\x1b]133;C;lc=" + f.nonce + "\x07"
	}
	// As the integration sends it: the prompt that follows, base64.
	return fmt.Sprintf("\x1b]133;D;%d;p=%s;lc=%s\x07", status, base64.StdEncoding.EncodeToString([]byte(f.promptString())), f.nonce)
}

func (f *fakePS) promptString() string {
	if f.promptText != "" {
		return f.promptText
	}
	return `PS C:\work> `
}

func (f *fakePS) prompt(status int) {
	f.write(f.mark("D", status) + strings.ReplaceAll(f.promptString(), "\n", "\r\n"))
}

func (f *fakePS) run() {
	time.Sleep(f.delay)
	f.prompt(0)
	var line []rune
	buf := make([]byte, 256)
	pending := []byte{}
	for {
		n, err := f.inR.Read(buf)
		if err != nil {
			return
		}
		pending = append(pending, buf[:n]...)
		for len(pending) > 0 && utf8.FullRune(pending) {
			r, size := utf8.DecodeRune(pending)
			pending = pending[size:]
			switch {
			case r == '\r':
				cmd := strings.TrimSpace(string(line))
				line = line[:0]
				switch f.early {
				case "both":
					// The echo was held back: the marks come first, then
					// everything the screen shows.
					status, out, exit := f.capture(cmd)
					f.write(f.mark("C", 0) + f.mark("D", status) + cmd + "\r\n" + out)
					if exit >= 0 {
						f.exit(exit)
						return
					}
					f.write(strings.ReplaceAll(f.promptString(), "\n", "\r\n"))
					continue
				case "d", "mid":
					status, out, exit := f.capture(cmd)
					if f.early == "mid" {
						// In the middle of the output: after its first line.
						if i := strings.Index(out, "\r\n"); i >= 0 {
							out = out[:i+2] + f.mark("D", status) + out[i+2:]
						} else {
							out = f.mark("D", status) + out
						}
						f.write("\r\n" + f.mark("C", 0) + out)
					} else {
						f.write("\r\n" + f.mark("C", 0) + f.mark("D", status) + out)
					}
					if exit >= 0 {
						f.exit(exit)
						return
					}
					f.write(strings.ReplaceAll(f.promptString(), "\n", "\r\n"))
					continue
				}
				f.write("\r\n" + f.mark("C", 0))
				status, exit := f.exec(cmd)
				if exit >= 0 {
					f.exit(exit)
					return
				}
				f.prompt(status)
			case r == 0x03:
				f.write("^C\r\n")
				line = line[:0]
				f.prompt(1)
			case r == 0x7f:
				if len(line) > 0 {
					line = line[:len(line)-1]
					f.write("\b \b")
				}
			default:
				line = append(line, r)
				if f.early != "both" {
					f.write(string(r))
				}
			}
		}
	}
}

// capture runs one line and returns what it printed rather than printing
// it.
func (f *fakePS) capture(line string) (int, string, int) {
	var b strings.Builder
	saved := f.outW
	r, w := io.Pipe()
	f.wmu.Lock()
	f.outW = w
	f.wmu.Unlock()
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				close(done)
				return
			}
		}
	}()
	status, exit := f.exec(line)
	w.Close()
	<-done
	f.wmu.Lock()
	f.outW = saved
	f.wmu.Unlock()
	return status, b.String(), exit
}

// exec runs one line: its status, and an exit code when it ends the shell
// (-1 when it does not).
func (f *fakePS) exec(line string) (int, int) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0, -1
	}
	switch fields[0] {
	case "echo", "Write-Output":
		f.write(strings.Join(fields[1:], " ") + "\r\n")
	case "korean":
		f.write("안녕하세요 한글\r\n")
	case "Get-Item":
		f.write("Get-Item : Cannot find path because it does not exist.\r\n")
		return 1, -1
	case "cmd":
		var n int
		fmt.Sscanf(fields[len(fields)-1], "%d", &n)
		return n, -1
	case "exit":
		var n int
		if len(fields) > 1 {
			fmt.Sscanf(fields[1], "%d", &n)
		}
		return 0, n
	case "pwd":
		f.write(f.dir + "\r\n")
	case "lines":
		var n int
		fmt.Sscanf(fields[1], "%d", &n)
		for i := 1; i <= n; i++ {
			f.write(fmt.Sprintf("line %d\r\n", i))
		}
	default:
		f.write(fields[0] + " : The term '" + fields[0] + "' is not recognized.\r\n")
		return 1, -1
	}
	return 0, -1
}

// localManager is a manager of fake PowerShells. started receives each
// one as it starts.
func localManager(t *testing.T, configure func(*fakePS)) (*Manager, chan *fakePS) {
	t.Helper()
	started := make(chan *fakePS, 16)
	m := NewManager(Options{Local: &LocalShell{
		Path: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		Dir:  func(conv string) string { return `C:\work\` + conv },
		Start: func(argv []string, dir string, cols, rows int) (LocalProcess, error) {
			f := newFakePS(argv, dir)
			f.sizes = append(f.sizes, [2]int{cols, rows})
			if configure != nil {
				configure(f)
			}
			go f.run()
			started <- f
			return f, nil
		},
	}})
	t.Cleanup(func() { m.CloseAll("test over", true) })
	return m, started
}

// localTerminal opens a terminal and waits for its first prompt and mark.
func localTerminal(t *testing.T, m *Manager) *Terminal {
	t.Helper()
	term, err := m.Open("c1", "PowerShell", ByModel)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := term.WaitConnected(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	within(t, 10*time.Second, "integration on", func() bool { return term.Info().Integrated })
	settled(t, term)
	return term
}

func TestALocalShellIsGivenItsIntegrationOnItsCommandLine(t *testing.T) {
	argv := powershellArgs("powershell.exe", "0123abcd")
	if len(argv) != 5 || argv[0] != "powershell.exe" || argv[1] != "-NoLogo" || argv[2] != "-NoExit" || argv[3] != "-EncodedCommand" {
		t.Fatalf("argv = %q", argv)
	}
	script := decodePS(t, argv[4])
	if script != powershellIntegration("0123abcd") {
		t.Fatalf("the encoded command is not the integration:\n%s", script)
	}
	for _, want := range []string{
		`$global:__lc_n='0123abcd'`,
		`function global:prompt`,
		`$ok=$global:?`,
		`]133;D;$s$t;lc=`,
		`$b = [Convert]::ToBase64String(`,
		`{';p=' + $b}`,
		`[object]::ReferenceEquals($er, $global:__lc_er)`,
		`function global:PSConsoleHostReadLine`,
		`]133;C;lc=`,
		`& $global:__lc_prompt`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the integration has no %q", want)
		}
	}
	// Windows PowerShell 5.1 has no `e escape: an ESC written that way
	// would print as a literal "e".
	if strings.Contains(script, "`e") {
		t.Error("the integration uses `e, which Windows PowerShell 5.1 does not have")
	}
	// -NoProfile would start a shell without the person's own setup, and
	// the panel is their PowerShell.
	for _, a := range argv {
		if strings.EqualFold(a, "-NoProfile") {
			t.Error("the shell is started without its profile")
		}
	}
}

func TestShellNameIsTheExecutablesName(t *testing.T) {
	for path, want := range map[string]string{
		"powershell.exe": "powershell",
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`: "powershell",
		"pwsh":                                   "pwsh",
		`C:\Program Files\PowerShell\7\pwsh.exe`: "pwsh",
		`C:\Program Files\PowerShell\7\PWSH.EXE`: "pwsh",
		"/usr/local/bin/pwsh":                    "pwsh",
		`D:\tools\other.exe`:                     "other",
	} {
		if got := ShellName(path); got != want {
			t.Errorf("ShellName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestALocalTerminalRunsCommandsAndReportsTheirStatus(t *testing.T) {
	m, started := localManager(t, nil)
	term := localTerminal(t, m)
	f := <-started
	if f.nonce != term.marks.nonce {
		t.Fatalf("the shell was given nonce %q, the terminal has %q", f.nonce, term.marks.nonce)
	}
	if info := term.Info(); info.Shell != "powershell" || info.State != StateConnected || info.Spec != "PowerShell" {
		t.Fatalf("info = %+v", info)
	}
	for _, c := range []struct {
		cmd, out, status string
	}{
		{"echo hello world", "hello world", "0"},
		{"cmd /c exit 3", "", "3"},
		{"Get-Item C:\\no\\such\\thing", "Get-Item : Cannot find path because it does not exist.", "1"},
		{"korean", "안녕하세요 한글", "0"},
		{"echo a!b ^c", "a!b ^c", "0"},
	} {
		res := run(t, term, c.cmd)
		if !res.Finished || res.Output != c.out || res.Status != c.status || !res.Integrated {
			t.Errorf("run %q: %+v, want output %q status %s", c.cmd, res, c.out, c.status)
		}
	}
}

func TestALocalShellStartsInTheConversationsDirectory(t *testing.T) {
	m, started := localManager(t, nil)
	term := localTerminal(t, m)
	f := <-started
	if f.dir != `C:\work\c1` {
		t.Fatalf("started in %q", f.dir)
	}
	if res := run(t, term, "pwd"); res.Output != `C:\work\c1` {
		t.Fatalf("pwd: %+v", res)
	}
	if f.argv[0] != `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe` {
		t.Fatalf("started %q", f.argv[0])
	}
}

func TestTheShellExitingEndsTheLocalTerminal(t *testing.T) {
	m, _ := localManager(t, nil)
	term := localTerminal(t, m)
	res, err := term.Run(context.Background(), "exit 5", 5*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if res.Ended == "" {
		t.Errorf("run of exit: %+v, want the terminal's end", res)
	}
	within(t, 5*time.Second, "the terminal ended", func() bool { return term.Info().State == StateEnded })
	if r := term.Info().Reason; r != "the shell exited with status 5" {
		t.Errorf("reason %q", r)
	}
}

func TestClosingALocalTerminalEndsItsShell(t *testing.T) {
	m, started := localManager(t, nil)
	term := localTerminal(t, m)
	f := <-started
	if !m.Remove("c1", term.ID, "closed in the panel") {
		t.Fatal("not removed")
	}
	within(t, 5*time.Second, "the shell closed", f.isClosed)
	if st := term.Info().State; st != StateEnded {
		t.Errorf("state %s", st)
	}
}

func TestAStartThatFailsEndsTheTerminalWithWhy(t *testing.T) {
	m := NewManager(Options{Local: &LocalShell{
		Path: "powershell.exe",
		Start: func([]string, string, int, int) (LocalProcess, error) {
			return nil, errors.New("powershell.exe: executable file not found in %PATH%")
		},
	}})
	t.Cleanup(func() { m.CloseAll("test over", true) })
	term, err := m.Open("c1", "PowerShell", ByPerson)
	if err != nil {
		t.Fatal(err)
	}
	within(t, 5*time.Second, "ended", func() bool { return term.Info().State == StateEnded })
	if r := term.Info().Reason; !strings.Contains(r, "could not start") || !strings.Contains(r, "not found") {
		t.Errorf("reason %q", r)
	}
}

func TestTheDefaultStartOutsideWindowsSaysSo(t *testing.T) {
	m := NewManager(Options{Local: &LocalShell{Path: "powershell.exe"}})
	t.Cleanup(func() { m.CloseAll("test over", true) })
	term, err := m.Open("c1", "PowerShell", ByPerson)
	if err != nil {
		t.Fatal(err)
	}
	if isWindows {
		// The real start, which local_windows_test.go tests.
		term.m.Remove("c1", term.ID, "test over")
		return
	}
	within(t, 5*time.Second, "ended", func() bool { return term.Info().State == StateEnded })
	if r := term.Info().Reason; !strings.Contains(r, ErrNoLocalShell.Error()) {
		t.Errorf("reason %q", r)
	}
}

func TestARunWaitsForAShellStillLoadingItsProfile(t *testing.T) {
	m, _ := localManager(t, func(f *fakePS) { f.delay = 1500 * time.Millisecond })
	term, err := m.Open("c1", "PowerShell", ByModel)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := term.WaitConnected(ctx); err != nil {
		t.Fatal(err)
	}
	// Straight away, before the first prompt.
	res, err := term.Run(ctx, "echo ready", 10*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Finished || res.Output != "ready" || res.Status != "0" {
		t.Fatalf("run before the first prompt: %+v", res)
	}
}

func TestAShellWithoutTheIntegrationStillRuns(t *testing.T) {
	setWait(t, &localWait, 1*time.Second)
	m, _ := localManager(t, func(f *fakePS) { f.noMarks = true })
	term, err := m.Open("c1", "PowerShell", ByModel)
	if err != nil {
		t.Fatal(err)
	}
	within(t, 5*time.Second, "the wait for a mark given up", func() bool {
		term.mu.Lock()
		defer term.mu.Unlock()
		return term.state == StateConnected && !term.integrating
	})
	if term.Info().Integrated {
		t.Fatal("integrated without a mark")
	}
	res, err := term.Run(context.Background(), "echo a!b", 5*time.Second, term.PersonGen())
	if err != nil {
		t.Fatalf("run in a shell without integration: %v", err)
	}
	if res.Finished || res.Integrated || !strings.Contains(res.Output, "a!b") {
		t.Fatalf("run: %+v", res)
	}
}

func TestALocalTerminalIsResized(t *testing.T) {
	m, started := localManager(t, nil)
	term := localTerminal(t, m)
	f := <-started
	if got := f.lastSize(); got != [2]int{DefaultCols, DefaultRows} {
		t.Fatalf("started at %v", got)
	}
	term.ViewerSize("v1", 80, 24)
	within(t, 5*time.Second, "resized", func() bool { return f.lastSize() == [2]int{80, 24} })
	if info := term.Info(); info.Cols != 80 || info.Rows != 24 {
		t.Errorf("info %dx%d", info.Cols, info.Rows)
	}
}

func TestStartingAgainStartsANewShell(t *testing.T) {
	m, started := localManager(t, nil)
	term := localTerminal(t, m)
	first := <-started
	res, err := term.Run(context.Background(), "exit 0", 5*time.Second, term.PersonGen())
	if err != nil || res.Ended == "" {
		t.Fatalf("exit: %+v %v", res, err)
	}
	again, err := m.Reconnect("c1", term.ID, ByPerson)
	if err != nil {
		t.Fatal(err)
	}
	second := <-started
	if second == first || second.nonce == first.nonce {
		t.Fatal("the same shell, or the same nonce, again")
	}
	within(t, 5*time.Second, "integrated", func() bool { return again.Info().Integrated })
	settled(t, again)
	if res := run(t, again, "echo back"); res.Output != "back" {
		t.Fatalf("after starting again: %+v", res)
	}
}

func TestALocalManagersRefusalsNameItsTerminals(t *testing.T) {
	started := make(chan *fakePS, 16)
	m := NewManager(Options{MaxPerConversation: 1, Local: &LocalShell{
		Path: "powershell.exe",
		Start: func(argv []string, dir string, cols, rows int) (LocalProcess, error) {
			f := newFakePS(argv, dir)
			go f.run()
			started <- f
			return f, nil
		},
	}})
	t.Cleanup(func() { m.CloseAll("test over", true) })
	if _, err := m.Open("c1", "PowerShell", ByPerson); err != nil {
		t.Fatal(err)
	}
	_, err := m.Open("c1", "PowerShell", ByPerson)
	if err == nil || !strings.Contains(err.Error(), "PowerShell terminals") || strings.Contains(err.Error(), "connections") {
		t.Fatalf("second open: %v", err)
	}
	m.CloseAll("off", true)
	if _, err := m.Open("c2", "PowerShell", ByPerson); err == nil || err.Error() != "PowerShell is off" {
		t.Fatalf("open after close: %v", err)
	}
}

// An older pseudo console sends a mark ahead of the text it follows. The
// output is read off the screen, so it is the same either way.
func TestALocalRunsOutputIsRightWhicheverWayTheMarksArrive(t *testing.T) {
	long := "echo " + strings.Repeat("x", 150)
	prompts := map[string]string{
		"short": "",
		// A deep working directory: wider than the screen.
		"wide": `PS C:\Users\someone\AppData\Local\Temp\` + strings.Repeat("deep\\", 20) + `> `,
		// A prompt of two lines, as oh-my-posh draws one.
		"two lines": "~\\work on main\n> ",
	}
	for _, early := range []string{"", "d", "mid", "both"} {
		for name, prompt := range prompts {
			early, prompt := early, prompt
			t.Run("early="+early+"/prompt="+name, func(t *testing.T) {
				m, _ := localManager(t, func(f *fakePS) { f.early = early; f.promptText = prompt })
				term := localTerminal(t, m)
				for _, c := range []struct {
					cmd, out, status string
				}{
					{"echo hello world", "hello world", "0"},
					{"cmd /c exit 3", "", "3"},
					{"korean", "안녕하세요 한글", "0"},
					{"echo 한글 명령", "한글 명령", "0"},
					{"Get-Item C:\\no", "Get-Item : Cannot find path because it does not exist.", "1"},
					// A line wider than the screen, read back as one line.
					{long, strings.Repeat("x", 150), "0"},
					{"echo " + strings.Repeat("y", DefaultCols-1), strings.Repeat("y", DefaultCols-1), "0"},
					{"lines 3", "line 1\nline 2\nline 3", "0"},
				} {
					res := run(t, term, c.cmd)
					if !res.Finished || res.Output != c.out || res.Status != c.status {
						t.Errorf("run %q: %+v, want output %q status %s", c.cmd, res, c.out, c.status)
					}
				}
				// More lines than the screen has rows: the first ones are in
				// the scrollback by the time the command ends.
				res := run(t, term, "lines 75")
				got := strings.Split(res.Output, "\n")
				if len(got) != 75 || got[0] != "line 1" || got[74] != "line 75" {
					t.Errorf("75 lines: %d lines, first %q last %q", len(got), got[0], got[len(got)-1])
				}
			})
		}
	}
}

func TestADMarkCarriesThePromptThatFollowsIt(t *testing.T) {
	s := markScanner{nonce: "ab12"}
	enc := base64.StdEncoding.EncodeToString([]byte("\x1b[32mPS C:\\>\x1b[0m \r\n> "))
	evs := s.feed([]byte("out\x1b]133;D;3;p=" + enc + ";lc=ab12\x07PS"))
	if len(evs) != 1 || evs[0].kind != 'D' || evs[0].status != "3" || evs[0].prompt != "PS C:\\> \n> " {
		t.Fatalf("events %+v", evs)
	}
	// A prompt that is not base64, or not text, is no prompt; the mark
	// still counts.
	for _, p := range []string{"p=***", "p=" + base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe})} {
		evs := s.feed([]byte("\x1b]133;D;0;" + p + ";lc=ab12\x07"))
		if len(evs) != 1 || evs[0].status != "0" || evs[0].prompt != "" {
			t.Errorf("%s: %+v", p, evs)
		}
	}
	// Another nonce, or another field, is not ours.
	for _, p := range []string{"\x1b]133;D;0;p=UFM=;lc=ffff\x07", "\x1b]133;D;0;q=UFM=;lc=ab12\x07"} {
		if evs := s.feed([]byte(p)); len(evs) != 0 {
			t.Errorf("%q: %+v", p, evs)
		}
	}
}

func TestPromptRowsCountsWrappedAndSeparateLines(t *testing.T) {
	for _, c := range []struct {
		prompt string
		cols   int
		want   int
	}{
		{"PS C:\\> ", 100, 1},
		{strings.Repeat("x", 100), 100, 1},
		{strings.Repeat("x", 101), 100, 2},
		{"top line\n> ", 100, 2},
		{strings.Repeat("한", 60) + "> ", 100, 2},
		{"", 100, 1},
	} {
		if got := promptRows(c.prompt, c.cols); got != c.want {
			t.Errorf("promptRows(%q, %d) = %d, want %d", c.prompt, c.cols, got, c.want)
		}
	}
}
