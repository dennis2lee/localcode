package sshterm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// testManager is a manager whose home, data and agent are the test's own.
// It never reads the person's ~/.ssh, /etc/ssh or SSH agent.
func testManager(t *testing.T, home string) *Manager {
	t.Helper()
	m := NewManager(Options{
		Home:      home,
		DataDir:   filepath.Join(home, "data"),
		LocalUser: "me",
	})
	t.Cleanup(func() { m.CloseAll("test over", true) })
	return m
}

func spec(s *testServer) string { return "me@" + s.addr }

func within(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("not within %s: %s", d, what)
}

// connected opens a terminal to s and waits for it to be connected and
// integrated.
func connected(t *testing.T, m *Manager, s *testServer) *Terminal {
	t.Helper()
	term, err := m.Open("c1", spec(s), ByModel)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := term.WaitConnected(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	within(t, 10*time.Second, "integration on", func() bool { return term.Info().Integrated })
	settled(t, term)
	return term
}

// settled waits until the prompt has been drawn and the cursor rests
// after it.
func settled(t *testing.T, term *Terminal) {
	t.Helper()
	within(t, 5*time.Second, "the prompt settled", func() bool {
		term.mu.Lock()
		defer term.mu.Unlock()
		return term.promptKnown
	})
}

func run(t *testing.T, term *Terminal, cmd string) RunResult {
	t.Helper()
	res, err := term.Run(context.Background(), cmd, 10*time.Second, term.PersonGen())
	if err != nil {
		t.Fatalf("run %q: %v", cmd, err)
	}
	return res
}

func TestRunReturnsOutputAndStatus(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)

	res := run(t, term, "echo hello world")
	if res.Output != "hello world" || res.Status != "0" || !res.Finished {
		t.Fatalf("echo: %+v", res)
	}
	res = run(t, term, "false")
	if res.Output != "" || res.Status != "1" || !res.Finished {
		t.Fatalf("false: %+v", res)
	}
	res = run(t, term, "progress")
	if res.Output != "100%" {
		t.Fatalf("progress output %q, want the bar's last state", res.Output)
	}
	res = run(t, term, "color")
	if res.Output != "red plain" {
		t.Fatalf("color output %q", res.Output)
	}
	res = run(t, term, "korean")
	if res.Output != "안녕하세요 한글" {
		t.Fatalf("korean output %q", res.Output)
	}
	// Back to back, the second typed while the prompt is fresh.
	for i := 0; i < 5; i++ {
		if res := run(t, term, "echo again"); res.Output != "again" {
			t.Fatalf("run %d: output %q", i, res.Output)
		}
	}
}

func TestTheShellIsIntegratedWithTheTerminalsNonce(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	if !strings.Contains(s.got(), "__lc_n="+term.marks.nonce+";") {
		t.Fatalf("the integration line did not carry the nonce: %q", s.got())
	}
	// A mark with another nonce in a command's output is not the end.
	res := run(t, term, "forge")
	if !strings.Contains(res.Output, "real end") || res.Status != "0" {
		t.Fatalf("a forged mark ended the run: %+v", res)
	}
}

func TestNoIntegrationWhenTheShellIsNotBashOrZsh(t *testing.T) {
	s := startServer(t, func(s *testServer) { s.shell = "/usr/bin/fish" })
	m := testManager(t, testHome(t, s, true))
	term, err := m.Open("c1", spec(s), ByModel)
	if err != nil {
		t.Fatal(err)
	}
	if err := term.WaitConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	within(t, 10*time.Second, "shell known", func() bool { return term.Info().Shell == "fish" })
	if strings.Contains(s.got(), "__lc_n") {
		t.Fatal("the integration line was typed into fish")
	}
	// run still works, by quiet, with no status.
	res, err := term.Run(context.Background(), "echo hi", 10*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "hi") || res.Status != "" || res.Finished || res.Integrated {
		t.Fatalf("without integration: %+v", res)
	}
}

func TestUnknownHostIsAskedOfThePersonAndRemembered(t *testing.T) {
	s := startServer(t)
	home := testHome(t, s, false)
	m := testManager(t, home)
	term, err := m.Open("c1", spec(s), ByModel)
	if err != nil {
		t.Fatal(err)
	}
	var q *Question
	within(t, 5*time.Second, "a host key question", func() bool { q = term.Info().Question; return q != nil })
	if q.Kind != AskHostKey || q.HostKey.Fingerprint != ssh.FingerprintSHA256(s.hostKey.PublicKey()) {
		t.Fatalf("question %+v", q)
	}
	if err := term.Answer(q.ID, Answer{Accept: true}, "SHA256:wrong"); err == nil {
		t.Fatal("an accept without the fingerprint shown was taken")
	}
	if err := term.Answer(q.ID, Answer{Accept: true}, q.HostKey.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	own, err := os.ReadFile(filepath.Join(home, "data", "known_hosts"))
	if err != nil || !strings.Contains(string(own), knownName("127.0.0.1", s.port())) {
		t.Fatalf("the accepted key was not kept: %q %v", own, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "known_hosts")); err == nil {
		t.Fatal("the person's known_hosts was written")
	}
	// Next time, no question.
	again, err := m.Open("c1", spec(s), ByModel)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.WaitConnected(context.Background()); err != nil {
		t.Fatalf("second connection: %v", err)
	}
}

func TestRejectingTheHostKeyEndsTheConnection(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, false))
	term, _ := m.Open("c1", spec(s), ByModel)
	var q *Question
	within(t, 5*time.Second, "a question", func() bool { q = term.Info().Question; return q != nil })
	if err := term.Answer(q.ID, Answer{Accept: false}, ""); err != nil {
		t.Fatal(err)
	}
	err := term.WaitConnected(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not accepted") {
		t.Fatalf("got %v", err)
	}
}

func TestAChangedHostKeyIsRefusedWithoutAQuestion(t *testing.T) {
	s := startServer(t)
	home := testHome(t, s, false)
	other := newSigner(t)
	line := knownName("127.0.0.1", s.port()) + " " + string(ssh.MarshalAuthorizedKey(other.PublicKey()))
	os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte("garbage line that does not parse\n"+line), 0o600)
	m := testManager(t, home)
	term, _ := m.Open("c1", spec(s), ByModel)
	err := term.WaitConnected(context.Background())
	if err == nil || !strings.Contains(err.Error(), ErrHostKeyChanged.Error()) {
		t.Fatalf("got %v", err)
	}
}

func TestPasswordIsAskedOfThePerson(t *testing.T) {
	s := startServer(t, func(s *testServer) { s.userKey = nil; s.password = "hunter2" })
	m := testManager(t, testHome(t, s, true))
	term, _ := m.Open("c1", spec(s), ByModel)
	answer := func(pw string) {
		var q *Question
		within(t, 5*time.Second, "a password question", func() bool { q = term.Info().Question; return q != nil && q.Kind == AskPassword })
		if err := term.Answer(q.ID, Answer{Accept: true, Values: [][]byte{[]byte(pw)}}, ""); err != nil {
			t.Fatal(err)
		}
		within(t, 5*time.Second, "the question gone", func() bool { q := term.Info().Question; return q == nil || q.ID != q.ID })
	}
	answer("wrong")
	answer("hunter2")
	if err := term.WaitConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestKeyboardInteractiveIsAskedOfThePerson(t *testing.T) {
	s := startServer(t, func(s *testServer) { s.userKey = nil; s.kbdCode = "123456" })
	m := testManager(t, testHome(t, s, true))
	term, _ := m.Open("c1", spec(s), ByModel)
	var q *Question
	within(t, 5*time.Second, "prompts", func() bool { q = term.Info().Question; return q != nil })
	if q.Kind != AskPrompts || len(q.Prompts) != 1 || q.Prompts[0].Text != "Verification code: " || !q.Prompts[0].Echo {
		t.Fatalf("question %+v", q)
	}
	if err := term.Answer(q.ID, Answer{Accept: true, Values: [][]byte{[]byte("123456")}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAnEncryptedKeyAsksForItsPassphraseOnlyWhenTheServerTakesIt(t *testing.T) {
	s := startServer(t)
	home := testHome(t, s, true)
	writeKey(t, filepath.Join(home, ".ssh", "id_ed25519"), s.userKey, []byte("secret phrase"))
	m := testManager(t, home)
	term, _ := m.Open("c1", spec(s), ByModel)
	var q *Question
	within(t, 5*time.Second, "a passphrase question", func() bool { q = term.Info().Question; return q != nil })
	if q.Kind != AskPassphrase || !strings.HasSuffix(q.Key, "id_ed25519") {
		t.Fatalf("question %+v", q)
	}
	if err := term.Answer(q.ID, Answer{Accept: true, Values: [][]byte{[]byte("secret phrase")}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := term.WaitConnected(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A key the server does not take is never unlocked.
	s2 := startServer(t, func(s *testServer) { s.password = "pw" })
	home2 := testHome(t, s2, true)
	writeKey(t, filepath.Join(home2, ".ssh", "id_ed25519"), newSigner(t), []byte("x"))
	m2 := testManager(t, home2)
	term2, _ := m2.Open("c1", spec(s2), ByModel)
	within(t, 5*time.Second, "a question", func() bool { q = term2.Info().Question; return q != nil })
	if q.Kind != AskPassword {
		t.Fatalf("asked %s for a key the server refused", q.Kind)
	}
}

func TestAJumpHostIsGoneThrough(t *testing.T) {
	target := startServer(t)
	jump := startServer(t)
	jump.userKey = target.userKey
	home := testHome(t, target, true)
	known := knownName("127.0.0.1", target.port()) + " " + string(ssh.MarshalAuthorizedKey(target.hostKey.PublicKey())) +
		knownName("127.0.0.1", jump.port()) + " " + string(ssh.MarshalAuthorizedKey(jump.hostKey.PublicKey()))
	os.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte(known), 0o600)
	cfg := "Host inside\n  HostName 127.0.0.1\n  Port " + itoa(target.port()) + "\n  User me\n  ProxyJump me@" + jump.addr + "\n"
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(cfg), 0o600)
	m := testManager(t, home)
	term, err := m.Open("c1", "inside", ByModel)
	if err != nil {
		t.Fatal(err)
	}
	if err := term.WaitConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	within(t, 10*time.Second, "integrated", func() bool { return term.Info().Integrated })
	if res := run(t, term, "echo through"); res.Output != "through" {
		t.Fatalf("output %q", res.Output)
	}
}

func TestProxyCommandIsRefused(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host p\n  ProxyCommand nc %h %p\n"), 0o600)
	m := testManager(t, home)
	term, _ := m.Open("c1", "p", ByModel)
	err := term.WaitConnected(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ProxyCommand") {
		t.Fatalf("got %v", err)
	}
}

func TestATimedOutRunLeavesTheCommandRunning(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	res, err := term.Run(context.Background(), "sleep 2s", 300*time.Millisecond, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.Finished {
		t.Fatalf("%+v", res)
	}
	if !term.Info().Running {
		t.Fatal("the command was not left running")
	}
	if _, err := term.Run(context.Background(), "echo x", time.Second, term.PersonGen()); !errors.Is(err, ErrBusy) {
		t.Fatalf("a run while one is running: %v", err)
	}
	ok, err := term.Wait(context.Background(), 5*time.Second, "")
	if !ok || err != nil {
		t.Fatalf("wait: %v %v", ok, err)
	}
	if res := run(t, term, "echo after"); res.Output != "after" {
		t.Fatalf("after: %q", res.Output)
	}
}

func TestStoppingTheTurnInterruptsTheCommand(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	if _, err := term.Run(ctx, "sleep 30s", 30*time.Second, term.PersonGen()); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	within(t, 5*time.Second, "the command interrupted", func() bool { return !term.Info().Running })
	if !strings.Contains(s.got(), "\x03") {
		t.Fatal("no Ctrl+C reached the shell")
	}
}

func TestRunReturnsWhenACommandAsks(t *testing.T) {
	old, oldYield := inputAfter, personYield
	inputAfter, personYield = 500*time.Millisecond, 300*time.Millisecond
	defer func() { inputAfter, personYield = old, oldYield }()
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	res := run(t, term, "secret")
	if !res.Asking || res.Finished {
		t.Fatalf("password prompt: %+v", res)
	}
	if !m.TakeWants("c1") {
		t.Fatal("the panel was not told the person is wanted")
	}
	// The person answers in the panel.
	for _, c := range "pw12" {
		term.PersonInput(nil, string(c), "")
	}
	term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
	within(t, 5*time.Second, "the command done", func() bool { return !term.Info().Running })
	if strings.Contains(readAll(term), "pw12") {
		t.Fatal("the password showed on the screen")
	}
	time.Sleep(400 * time.Millisecond)
	settled(t, term)

	res = run(t, term, "ask")
	if !res.Asking {
		t.Fatalf("question: %+v", res)
	}
}

func readAll(term *Terminal) string {
	r, _ := term.Read(1000, false)
	return strings.Join(r.Scrollback, "\n") + "\n" + strings.Join(r.Screen, "\n")
}

func TestAFullScreenProgramEndsTheWait(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	res := run(t, term, "fullscreen")
	if !res.Alt {
		t.Fatalf("%+v", res)
	}
	r, _ := term.Read(0, true)
	if !r.Info.Alt || !strings.Contains(strings.Join(r.Screen, "\n"), "FULL SCREEN") {
		t.Fatalf("read: %+v", r)
	}
	if err := term.Keys("q", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	within(t, 5*time.Second, "back at the prompt", func() bool { i := term.Info(); return !i.Alt && !i.Running })
}

func TestANestedShellIsNoticed(t *testing.T) {
	old := lostAfter
	lostAfter = 500 * time.Millisecond
	defer func() { lostAfter = old }()
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	res := run(t, term, "nest")
	if !res.Lost || res.Finished {
		t.Fatalf("entering the nested shell: %+v", res)
	}
	// In the nested shell run goes on, without statuses.
	res, err := term.Run(context.Background(), "echo inner", 10*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "inner") || res.Status != "" {
		t.Fatalf("inside: %+v", res)
	}
	// Leaving it brings the marks back.
	run(t, term, "exit")
	res = run(t, term, "false")
	if res.Status != "1" || !res.Finished {
		t.Fatalf("after leaving: %+v", res)
	}
}

func TestThePersonComesFirst(t *testing.T) {
	old := personYield
	personYield = 400 * time.Millisecond
	defer func() { personYield = old }()
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)

	gen := term.PersonGen()
	term.PersonInput(nil, "ls", "")
	if _, err := term.Run(context.Background(), "echo x", time.Second, term.PersonGen()); !errors.Is(err, ErrPersonActive) {
		t.Fatalf("while the person types: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	// What was approved before the person typed is not run.
	if _, err := term.Run(context.Background(), "echo x", time.Second, gen); !errors.Is(err, ErrChanged) {
		t.Fatalf("approved before the person typed: %v", err)
	}
	// Their half line stays theirs.
	for _, try := range []func() error{
		func() error {
			_, err := term.Run(context.Background(), "echo x", time.Second, term.PersonGen())
			return err
		},
		func() error { return term.Type("y", true, term.PersonGen()) },
		func() error { return term.Keys("Enter", term.PersonGen()) },
	} {
		if err := try(); !errors.Is(err, ErrLineNotEmpty) {
			t.Fatalf("with the person's text on the line: %v", err)
		}
	}
	// Erased, the line is free again.
	term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
	term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
	time.Sleep(500 * time.Millisecond)
	if res := run(t, term, "echo free"); res.Output != "free" {
		t.Fatalf("after erasing: %+v", res)
	}

	// Hold keeps the screen and the input private.
	term.SetHold(true)
	if _, err := term.Read(0, true); !errors.Is(err, ErrHeld) {
		t.Fatalf("read while held: %v", err)
	}
	if _, err := term.Wait(context.Background(), time.Second, ""); !errors.Is(err, ErrHeld) {
		t.Fatalf("wait while held: %v", err)
	}
	if err := term.Type("x", false, term.PersonGen()); !errors.Is(err, ErrHeld) {
		t.Fatalf("type while held: %v", err)
	}
	term.SetHold(false)
	if res := run(t, term, "echo released"); res.Output != "released" {
		t.Fatalf("%+v", res)
	}
}

func TestARunSaysThePersonTypedDuringIt(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	go func() {
		time.Sleep(300 * time.Millisecond)
		term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	}()
	res := run(t, term, "sleep 5s")
	if !res.Person || res.Status != "130" {
		t.Fatalf("%+v", res)
	}
}

func TestRunRefusesWhatTheShellWouldNotRunAsWritten(t *testing.T) {
	for _, c := range []string{"echo a\nrm x", "echo \x1b[A", "ls\tx", "echo hi!there", `echo "!!"`, "", "  "} {
		if err := CheckCommand(c); err == nil {
			t.Errorf("%q was taken", c)
		}
	}
	for _, c := range []string{"echo 'hi!there'", "[ ! -f x ]", "a != b", "echo hi!", `echo \!x`, "echo 안녕"} {
		if err := CheckCommand(c); err != nil {
			t.Errorf("%q: %v", c, err)
		}
	}
}

func TestTheRemoteCannotTypeIntoItsOwnInput(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	before := len(s.got())
	run(t, term, "titlereport")
	time.Sleep(200 * time.Millisecond)
	after := s.got()[before:]
	if strings.Contains(after, "rm -rf") || strings.Contains(after, "\x1b]52") {
		t.Fatalf("the remote's report came back as input: %q", after)
	}
}

func TestAShellThatStopsReadingDoesNotFreezeTheScreen(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	done := make(chan RunResult, 1)
	go func() {
		res, _ := term.Run(context.Background(), "flood 200000", 20*time.Second, term.PersonGen())
		done <- res
	}()
	// The screen answers while the replies pile up.
	for i := 0; i < 20; i++ {
		got := make(chan struct{})
		go func() { term.Snapshot(); close(got) }()
		select {
		case <-got:
		case <-time.After(2 * time.Second):
			t.Fatal("the screen froze")
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case res := <-done:
		if !strings.Contains(res.Output, "flooded") {
			t.Fatalf("%+v", res)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the run never ended")
	}
}

func TestTheShellExitingEndsTheTerminalAndKeepsItsScreen(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	run(t, term, "echo last words")
	term.Run(context.Background(), "exit 3", 5*time.Second, term.PersonGen())
	within(t, 5*time.Second, "ended", func() bool { return term.Info().State == StateEnded })
	if r := term.Info().Reason; !strings.Contains(r, "status 3") {
		t.Fatalf("reason %q", r)
	}
	if !strings.Contains(readAll(term), "last words") {
		t.Fatal("the screen was not kept")
	}
	if _, err := term.Run(context.Background(), "echo x", time.Second, term.PersonGen()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("run after the end: %v", err)
	}
	// Reconnect takes the same tab.
	again, err := m.Reconnect("c1", term.ID, ByPerson)
	if err != nil || again.ID != term.ID || again.Epoch == term.Epoch {
		t.Fatalf("reconnect: %v", err)
	}
	if err := again.WaitConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMissedKeepalivesEndTheTerminal(t *testing.T) {
	s := startServer(t)
	home := testHome(t, s, true)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host *\n  ServerAliveInterval 1\n  ServerAliveCountMax 2\n"), 0o600)
	m := testManager(t, home)
	term := connected(t, m, s)
	s.noKeepalive.set(true)
	within(t, 10*time.Second, "ended", func() bool { return term.Info().State == StateEnded })
	if !strings.Contains(term.Info().Reason, "keepalive") {
		t.Fatalf("reason %q", term.Info().Reason)
	}
}

func TestTheSmallestVisibleViewerSetsTheSize(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	term.ViewerSize("a", 120, 40)
	term.ViewerSize("b", 90, 50)
	within(t, 5*time.Second, "the size sent", func() bool { return s.lastSize() == [2]int{90, 40} })
	term.ViewerSize("b", 0, 0)
	within(t, 5*time.Second, "the size sent", func() bool { return s.lastSize() == [2]int{120, 40} })
	term.ViewerSize("a", 0, 0)
	if i := term.Info(); i.Cols != 120 || i.Rows != 40 {
		t.Fatalf("with no viewer the size changed: %dx%d", i.Cols, i.Rows)
	}
}

func TestLimitsCountLiveTerminals(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	m.opts.MaxPerConversation = 2
	a := connected(t, m, s)
	connected(t, m, s)
	if _, err := m.Open("c1", spec(s), ByPerson); !errors.Is(err, ErrLimit) {
		t.Fatalf("third: %v", err)
	}
	a.Close("closed")
	third, err := m.Open("c1", spec(s), ByPerson)
	if err != nil {
		t.Fatalf("after one ended: %v", err)
	}
	third.WaitConnected(context.Background())
	if len(m.List("c1")) != 3 {
		t.Fatalf("the ended one was not kept: %d", len(m.List("c1")))
	}
}

func TestClosingTheConversationClosesItsTerminals(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	m.CloseConversation("c1", "archived")
	within(t, 5*time.Second, "ended", func() bool { return term.Info().State == StateEnded })
	if len(m.List("c1")) != 0 {
		t.Fatal("still listed")
	}
	// One still connecting is not left behind either.
	s2 := startServer(t)
	m2 := testManager(t, testHome(t, s2, false)) // waits on a host key question
	pending, _ := m2.Open("c2", spec(s2), ByModel)
	within(t, 5*time.Second, "a question", func() bool { return pending.Info().Question != nil })
	m2.CloseConversation("c2", "archived")
	if err := pending.WaitConnected(context.Background()); err == nil {
		t.Fatal("a closed conversation's connection went on")
	}
	if len(m2.LiveTerminals()) != 0 {
		t.Fatal("a live terminal was left")
	}
}
