package sshterm

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
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
	for _, c := range []string{"^echo ^", "  ^a^b", `echo "'"; !echo`, `echo "it's" !echo`, `echo "a!b"`} {
		if err := CheckCommand(c); err == nil {
			t.Errorf("%q was taken", c)
		}
	}
	for _, c := range []string{`echo "it's fine"`, `echo 'a^b'`, `echo "hi!"`, `[ ! -d x ]`, `echo a^b`} {
		if err := CheckCommand(c); err != nil {
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
	// Ctrl+U empties it, and the line is free again.
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
