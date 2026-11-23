package sshterm

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// What the second review found, kept as tests. Each reproduced on the
// commit it reviewed.

// Remote output that put the cursor past a screen made narrower set a tab
// stop past the table: the emulator panicked in the reader goroutine,
// which ends the whole program.
func TestRemoteOutputCannotEndTheProgram(t *testing.T) {
	m := NewManager(Options{Home: t.TempDir(), DataDir: t.TempDir(), LocalUser: "me"})
	for name, c := range map[string][2]string{
		"saved wide, restored narrow, tab set": {"\x1b[100G\x1b7", "\x1b8\x1bH"},
		"full screen opened wide, quit narrow": {"\x1b[100G\x1b[?1049h", "\x1b[?1049l\x1bH"},
		"full screen opened wide, tab cleared": {"\x1b[100G\x1b[?1049h", "\x1b[?1049l\x1b[0g"},
		"full screen opened wide, text after":  {"\x1b[100G\x1b[?1049h", "\x1b[?1049lhello\r\n"},
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panic reached the caller: %v", name, r)
				}
			}()
			term := newTerminal(m, "c", "t1", "h", ByModel)
			defer term.end("done")
			term.output([]byte(c[0]))
			term.ViewerSize("v", 40, 10)
			term.output([]byte(c[1]))
		}()
	}
	// A shuffle of escape sequences, sizes and reads, from a fixed seed.
	pieces := []string{"\x1b[", "\x1b]", "\x1bP", "\x1b\\", "\x07", ";", "?", "h", "l", "m", "H", "J", "K", "r", "s", "u", "L", "M", "P", "@", "X", "S", "T", "G", "b", "g", "1049", "47", "99999", "0", "3", "한", "😀", "\r", "\n", "\t", "\x1b7", "\x1b8", "\x1bH", "\x1b[3J", "\x1b[999;999H", "\x1b[?7l", "\x1b[1;5r"}
	rng := rand.New(rand.NewSource(7))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		term := newTerminal(m, "c", "t1", "h", ByModel)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic reached the caller: %v", r)
				}
			}()
			for i := 0; i < 200; i++ {
				var b strings.Builder
				for j := rng.Intn(10) + 1; j > 0; j-- {
					b.WriteString(pieces[rng.Intn(len(pieces))])
				}
				term.output([]byte(b.String()))
				if rng.Intn(20) == 0 {
					term.ViewerSize("v", 40+rng.Intn(260), 10+rng.Intn(110))
				}
				if rng.Intn(10) == 0 {
					term.Snapshot()
				}
			}
		}()
		term.end("done")
	}
}

// Thirteen bytes asking for a hundred million repeats held the terminal's
// lock for seconds, and the session list of every conversation with it.
func TestALongRepeatHoldsNothingUp(t *testing.T) {
	m := NewManager(Options{Home: t.TempDir(), DataDir: t.TempDir(), LocalUser: "me"})
	term := newTerminal(m, "c", "t1", "h", ByModel)
	m.mu.Lock()
	m.conv("c").terms = append(m.conv("c").terms, term)
	m.mu.Unlock()
	defer term.end("done")
	done := make(chan struct{})
	go func() {
		term.output([]byte(strings.Repeat("a\x1b[99999999b", 50)))
		close(done)
	}()
	asked := make(chan struct{})
	go func() {
		m.Asking()
		m.LiveTerminals()
		close(asked)
	}()
	select {
	case <-asked:
	case <-time.After(time.Second):
		t.Fatal("the session list waited on a terminal drawing")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the repeats were not capped")
	}
}

// A run waiting when the person pressed Hold returned what they typed
// while holding it.
func TestHoldEndsAWaitingRunWithNothingOfWhatCameAfter(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	go func() {
		time.Sleep(300 * time.Millisecond)
		term.SetHold(true)
		time.Sleep(200 * time.Millisecond)
		term.PersonInput(nil, "MYSECRETANSWER", "")
		term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
	}()
	res, err := term.Run(context.Background(), "ask", 10*time.Second, term.PersonGen())
	if !errors.Is(err, ErrHeld) || strings.Contains(res.Output, "MYSECRETANSWER") {
		t.Fatalf("err=%v output=%q", err, res.Output)
	}
}

// Clearing the screen with its scrollback (clear sends ED 3) takes it out
// of what the model can read.
func TestClearingTheScrollbackClearsWhatTheModelCanRead(t *testing.T) {
	m := NewManager(Options{Home: t.TempDir(), DataDir: t.TempDir(), LocalUser: "me"})
	term := newTerminal(m, "c", "t1", "h", ByModel)
	defer term.end("done")
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "SECRET %d\r\n", i)
	}
	term.output([]byte(b.String()))
	if r, _ := term.Read(100, false); !strings.Contains(strings.Join(r.Scrollback, "\n"), "SECRET 1") {
		t.Fatal("no scrollback to clear")
	}
	term.output([]byte("\x1b[H\x1b[2J\x1b[3J"))
	r, _ := term.Read(100, false)
	if all := strings.Join(append(r.Scrollback, r.Screen...), "\n"); strings.Contains(all, "SECRET") {
		t.Fatalf("still readable:\n%s", all)
	}
}

// A hashed known_hosts entry and the host written in another case: the
// changed key was asked about as a new host.
func TestHostNamesAreLowerCasedForRulesAndKnownHosts(t *testing.T) {
	key := newSigner(t).PublicKey()
	impostor := newSigner(t).PublicKey()
	home := t.TempDir()
	auth := func(k ssh.PublicKey) string { return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) }
	writeFile(t, filepath.Join(home, ".ssh", "known_hosts"), hashedName(t, "prod.example.com")+" "+auth(key)+"\n")
	cfg, _ := loadConfig(home, "")
	sp, _ := ParseSpec("PROD.Example.com")
	if sp.Host != "prod.example.com" {
		t.Fatalf("spec host %q", sp.Host)
	}
	target := cfg.resolve(sp, "me", home)
	h := newHostKeys(Options{Home: home, DataDir: t.TempDir()}, target)
	asked := false
	ask := func(context.Context, HostKeyQuestion) (bool, error) { asked = true; return false, nil }
	err := h.callback(context.Background(), target.KnownName(), ask)("", nil, impostor)
	if asked || err == nil || !strings.Contains(err.Error(), ErrHostKeyChanged.Error()) {
		t.Fatalf("asked=%v err=%v", asked, err)
	}
}

func TestHistorySubstitutionIsRefused(t *testing.T) {
	for _, c := range []string{"^echo ^", "  ^a^b", `echo "'"; !echo`, `echo "it's" !echo`, `echo "a!b"`, `echo "hi!"`, `echo $'it\'s' !echo`} {
		if err := checkHistory(c); err == nil {
			t.Errorf("%q was taken", c)
		}
	}
	for _, c := range []string{`echo "it's fine"`, `echo 'a^b'`, `[ ! -d x ]`, `echo a^b`, `echo $!;`, `kill -9 $!; true`, `echo ${!x}`, `echo $'a\'b'`} {
		if err := checkHistory(c); err != nil {
			t.Errorf("%q: %v", c, err)
		}
	}
}

func realBash(t *testing.T) *Terminal {
	t.Helper()
	addr, home := startRealSSHD(t, "/bin/bash")
	me, _ := user.Current()
	m := testManager(t, home)
	term, err := m.Open("c1", me.Username+"@"+addr, ByModel)
	if err != nil {
		t.Fatal(err)
	}
	if err := term.WaitConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	within(t, 15*time.Second, "integration on", func() bool { return term.Info().Integrated })
	settled(t, term)
	return term
}

// A line the person typed and moved back to the start of (Ctrl+A) is
// still theirs: the model's command was typed in front of it, and the
// shell ran a line nobody approved.
func TestThePersonsLineIsTheirsWhereverTheCursorIs(t *testing.T) {
	old := personYield
	personYield = 400 * time.Millisecond
	defer func() { personYield = old }()
	term := realBash(t)
	term.PersonInput(nil, " PERSONS_HALF_LINE", "")
	term.PersonInput(&PersonKey{Key: "a", Ctrl: true}, "", "")
	time.Sleep(700 * time.Millisecond)
	if _, err := term.Run(context.Background(), "echo MODEL", 10*time.Second, term.PersonGen()); !errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("got %v", err)
	}
	// Ctrl+U at the start of the line erases nothing: still theirs.
	term.PersonInput(&PersonKey{Key: "u", Ctrl: true}, "", "")
	time.Sleep(700 * time.Millisecond)
	if _, err := term.Run(context.Background(), "echo MODEL", 10*time.Second, term.PersonGen()); !errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("after Ctrl+A Ctrl+U: %v", err)
	}
	// From the end, Ctrl+U empties it, and the line is free again.
	term.PersonInput(&PersonKey{Key: "e", Ctrl: true}, "", "")
	term.PersonInput(&PersonKey{Key: "u", Ctrl: true}, "", "")
	time.Sleep(700 * time.Millisecond)
	if res := run(t, term, "echo MODEL"); res.Output != "MODEL" {
		t.Fatalf("after Ctrl+U: %+v", res)
	}
}

// A command that stops at a line ending like a prompt is not taken for a
// nested shell to type into: the next run waits for its end.
func TestNoRunTypesIntoACommandThatHasNotEnded(t *testing.T) {
	old := lostAfter
	lostAfter = time.Second
	defer func() { lostAfter = old }()
	term := realBash(t)
	res := run(t, term, `printf 'copying 50%%'; sleep 4; echo; echo finished`)
	if res.Finished {
		t.Fatalf("first: %+v", res)
	}
	if _, err := term.Run(context.Background(), "echo SECOND", 5*time.Second, term.PersonGen()); !errors.Is(err, ErrBusy) {
		t.Fatalf("second run: %v", err)
	}
	if ok, _ := term.Wait(context.Background(), 10*time.Second, ""); !ok {
		t.Fatal("the command never ended")
	}
	if res := run(t, term, "echo after"); res.Output != "after" {
		t.Fatalf("after: %+v", res)
	}
}

// Keys that leave the line empty do not lock the model out: Ctrl+L draws
// the prompt at the top, and a new size makes the shell draw it again.
func TestAnEmptyLineIsEmptyWhereverThePromptIsDrawn(t *testing.T) {
	old := personYield
	personYield = 200 * time.Millisecond
	defer func() { personYield = old }()
	for _, shell := range []string{"/bin/bash", "/bin/zsh"} {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			addr, home := startRealSSHD(t, shell)
			me, _ := user.Current()
			m := testManager(t, home)
			term, _ := m.Open("c1", me.Username+"@"+addr, ByModel)
			term.WaitConnected(context.Background())
			within(t, 15*time.Second, "integrated", func() bool { return term.Info().Integrated })
			settled(t, term)
			run(t, term, "echo one; echo two; echo three")
			settled(t, term)
			term.PersonInput(&PersonKey{Key: "l", Ctrl: true}, "", "")
			time.Sleep(600 * time.Millisecond)
			if res := run(t, term, "echo after-clear"); res.Output != "after-clear" {
				t.Fatalf("after Ctrl+L: %+v", res)
			}
			settled(t, term)
			term.PersonInput(nil, "x", "")
			term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
			term.ViewerSize("v", 90, 30)
			time.Sleep(800 * time.Millisecond)
			if res := run(t, term, "echo after-resize"); res.Output != "after-resize" {
				t.Fatalf("after typing, erasing and a resize: %+v", res)
			}
		})
	}
}

// At the prompt, commands go through run: type takes no line end there,
// key no Enter, and what the model typed and did not run is not run with
// the next command.
func TestAtThePromptCommandsGoThroughRun(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	if err := term.Type("rm -rf x", true, term.PersonGen()); !errors.Is(err, ErrUseRun) {
		t.Fatalf("type with enter: %v", err)
	}
	if err := term.Type("rm -rf x\n", false, term.PersonGen()); !errors.Is(err, ErrUseRun) {
		t.Fatalf("type with a line end: %v", err)
	}
	for _, k := range []string{"Enter", "ctrl+j", "ctrl+m", "ctrl+o", "Tab", "Escape", "ctrl+x"} {
		if err := term.Keys(k, term.PersonGen()); !errors.Is(err, ErrUseRun) {
			t.Errorf("key %s: %v", k, err)
		}
	}
	if err := term.Type("half", false, term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := term.Run(context.Background(), "echo x", time.Second, term.PersonGen()); !errors.Is(err, ErrModelLine) {
		t.Fatalf("run onto the model's own text: %v", err)
	}
	// Erased, it is still refused: the screen does not say what the model
	// left on the line (spaces can push it out of sight).
	for i := 0; i < 4; i++ {
		if err := term.Keys("Backspace", term.PersonGen()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := term.Run(context.Background(), "echo x", time.Second, term.PersonGen()); !errors.Is(err, ErrModelLine) {
		t.Fatalf("run after erasing: %v", err)
	}
	// Ctrl+C discards the line.
	if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if res := run(t, term, "echo clean"); res.Output != "clean" {
		t.Fatalf("after clearing it: %+v", res)
	}
}

// ED 3 clears the scrollback where it comes: what follows it in the same
// read is kept.
func TestClearingTheScrollbackKeepsWhatCameAfter(t *testing.T) {
	m := NewManager(Options{Home: t.TempDir(), DataDir: t.TempDir(), LocalUser: "me"})
	term := newTerminal(m, "c", "t1", "h", ByModel)
	defer term.end("done")
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "OLD %d\r\n", i)
	}
	b.WriteString("\x1b[H\x1b[2J\x1b[3J")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "NEW %d\r\n", i)
	}
	term.output([]byte(b.String()))
	r, _ := term.Read(200, false)
	all := strings.Join(append(r.Scrollback, r.Screen...), "\n")
	if strings.Contains(all, "OLD") || !strings.Contains(all, "NEW 1\n") {
		t.Fatalf("scrollback:\n%s", all)
	}
}

// History expansion is off in a shell with integration: "!" and "^" in a
// command are the characters typed.
func TestHistoryExpansionIsOffInAnIntegratedShell(t *testing.T) {
	for _, shell := range []string{"/bin/bash", "/bin/zsh"} {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			addr, home := startRealSSHD(t, shell)
			me, _ := user.Current()
			m := testManager(t, home)
			term, _ := m.Open("c1", me.Username+"@"+addr, ByModel)
			term.WaitConnected(context.Background())
			within(t, 15*time.Second, "integrated", func() bool { return term.Info().Integrated })
			settled(t, term)
			run(t, term, "echo first")
			if res := run(t, term, `echo "hi!" !! a!b`); res.Output != "hi! !! a!b" {
				t.Fatalf("! expanded: %+v", res)
			}
			if res := run(t, term, "^first^second^ 2>/dev/null; echo done"); strings.Contains(res.Output, "second") {
				t.Fatalf("^ substituted: %+v", res)
			}
		})
	}
}

// A control character is a key by its name only: as itself it passed for
// text at the prompt, and zsh's ^O ran the line.
func TestAControlCharacterIsNotAKeyName(t *testing.T) {
	for _, k := range []string{"\x0f", "\x1b", "\x18", "\x04", "\x16", "\x7f"} {
		if _, err := ParseKeys(k); err == nil {
			t.Errorf("%q was taken", k)
		}
	}
	for _, k := range []string{"ctrl+o", "Escape", "a", "한"} {
		if _, err := ParseKeys(k); err != nil {
			t.Errorf("%q: %v", k, err)
		}
	}
}

// A line sent while a command runs and nothing asks for input waits in
// the terminal and is read by the shell as its next command: refused.
// A program that asks, or a nested shell at its prompt, takes one.
func TestNoLineIsLeftForTheShellToRunNext(t *testing.T) {
	old := personYield
	personYield = 100 * time.Millisecond
	defer func() { personYield = old }()
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	if res, _ := term.Run(context.Background(), "sleep 2s", 300*time.Millisecond, term.PersonGen()); !res.TimedOut {
		t.Fatalf("%+v", res)
	}
	if err := term.Type("touch x", true, term.PersonGen()); !errors.Is(err, ErrTypeahead) {
		t.Fatalf("type with enter: %v", err)
	}
	if err := term.Type("touch x", false, term.PersonGen()); err != nil {
		t.Fatalf("type without enter: %v", err)
	}
	for _, k := range []string{"Enter", "ctrl+j", "ctrl+m", "ctrl+o"} {
		if err := term.Keys(k, term.PersonGen()); !errors.Is(err, ErrTypeahead) {
			t.Errorf("key %s: %v", k, err)
		}
	}
	if ok, _ := term.Wait(context.Background(), 5*time.Second, ""); !ok {
		t.Fatal("never ended")
	}
	// What was typed ahead is on the new prompt's line: run does not add
	// to it, until the model's Ctrl+C and the prompt after it.
	time.Sleep(300 * time.Millisecond)
	if _, err := term.Run(context.Background(), "echo approved", time.Second, term.PersonGen()); !errors.Is(err, ErrModelLine) {
		t.Fatalf("run onto the typeahead: %v", err)
	}
	if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	if res := run(t, term, "echo approved"); res.Output != "approved" || res.Status != "0" {
		t.Fatalf("after ctrl+c: %+v", res)
	}
	// A program asking takes an answer.
	go func() { term.Run(context.Background(), "ask", 5*time.Second, term.PersonGen()) }()
	within(t, 5*time.Second, "the question", func() bool {
		r, _ := term.Read(0, false)
		return strings.Contains(strings.Join(r.Screen, "\n"), "[Y/n]")
	})
	if err := term.Type("y", true, term.PersonGen()); err != nil {
		t.Fatalf("answering a question: %v", err)
	}
}

// The model's Ctrl+C clears its text only when the shell takes it as an
// interrupt, which the prompt after it says. With "stty intr undef" the
// key is a character, and the text is still on the line.
func TestTheModelsCtrlCCountsWhenTheShellTakesIt(t *testing.T) {
	for _, shell := range []string{"/bin/bash", "/bin/zsh"} {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			addr, home := startRealSSHD(t, shell)
			me, _ := user.Current()
			m := testManager(t, home)
			term, _ := m.Open("c1", me.Username+"@"+addr, ByModel)
			term.WaitConnected(context.Background())
			within(t, 15*time.Second, "integrated", func() bool { return term.Info().Integrated })
			settled(t, term)
			// Right after a Ctrl+C that is an interrupt, run waits for the
			// prompt and reports its own command.
			if err := term.Type("junk", false, term.PersonGen()); err != nil {
				t.Fatal(err)
			}
			if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
				t.Fatal(err)
			}
			if res := run(t, term, "echo afterc"); res.Output != "afterc" || res.Status != "0" {
				t.Fatalf("run right after ctrl+c: %+v", res)
			}
			// With the interrupt key undefined, it is not one.
			run(t, term, "stty intr undef")
			mark := filepath.Join(home, "PWNED")
			if err := term.Type("touch "+mark+"; ", false, term.PersonGen()); err != nil {
				t.Fatal(err)
			}
			if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
				t.Fatal(err)
			}
			if _, err := term.Run(context.Background(), "echo approved", 5*time.Second, term.PersonGen()); !errors.Is(err, ErrModelLine) {
				t.Fatalf("run onto text a ctrl+c did not clear: %v", err)
			}
			if _, err := os.Stat(mark); err == nil {
				t.Fatal("the model's leftover text ran")
			}
		})
	}
}
