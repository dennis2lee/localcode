package sshterm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Text sent ahead of a prompt.
//
// Ctrl+C at the prompt drops what was typed before it. What is typed
// after it is on the line of the prompt it brings. The mark of that prompt
// used to clear all the terminal knew of typed text, so text typed after
// the key (in the same call, or before the prompt had arrived from a slow
// remote) was taken as gone, and the next run was typed onto it: the shell
// ran one line made of both.
//
// line_test.go goes through every order of keys with the test playing the
// shell. These are a few of them against a server, end to end.

// "ctrl+c r m" in one key call left "rm" on the line, with nothing slow
// about the remote.
func TestKeysAfterTheModelsCtrlCAreOnTheNextLine(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	if err := term.Keys("ctrl+c x y", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	// The prompt the key brought has arrived, with the text on it.
	typedLine(t, term, "$ xy")
	if _, err := term.Run(context.Background(), "echo ok", 5*time.Second, term.PersonGen()); !errors.Is(err, ErrModelLine) {
		t.Fatalf("run onto keys pressed after ctrl+c: %v", err)
	}
	// The key again, alone, clears it.
	if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	if res := run(t, term, "echo clean"); res.Output != "clean" {
		t.Fatalf("after ctrl+c alone: %+v", res)
	}
	// Keys before the key go with the line it drops, and a second ctrl+c
	// drops what followed the first.
	for _, keys := range []string{"x y ctrl+c", "ctrl+c x ctrl+c"} {
		if err := term.Keys(keys, term.PersonGen()); err != nil {
			t.Fatal(err)
		}
		if res := run(t, term, "echo clean"); res.Output != "clean" {
			t.Fatalf("after %q: %+v", keys, res)
		}
	}
	if got := s.got(); strings.Contains(got, "xyecho") || strings.Contains(got, "xecho") {
		t.Fatalf("a run was typed onto the model's text: %q", got)
	}
}

// The same in a later call, from a remote that takes a while to answer
// the key: type's text arrives at the shell after the key, and the prompt's
// mark arrives here after the text was sent.
func TestTextAfterTheModelsCtrlCIsOnTheNextLine(t *testing.T) {
	s := startServer(t, func(s *testServer) { s.interruptDelay = 4 * promptSettle })
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	if err := term.Type("junk", false, term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	if err := term.Type("left; ", false, term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	typedLine(t, term, "$ left;")
	if _, err := term.Run(context.Background(), "echo ok", 5*time.Second, term.PersonGen()); !errors.Is(err, ErrModelLine) {
		t.Fatalf("run onto text typed after ctrl+c: %v", err)
	}
	if strings.Contains(s.got(), "left; echo") {
		t.Fatal("the run was typed onto the model's text")
	}
}

// Each Ctrl+C the shell takes brings a prompt of its own. With a second
// press on its way, the first prompt does not say the line is settled: a
// run typed then would take the second prompt for the end of its command.
func TestASecondCtrlCIsAnsweredByTheSecondPrompt(t *testing.T) {
	s := startServer(t, func(s *testServer) { s.interruptDelay = 4 * promptSettle })
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	for i := 0; i < 2; i++ {
		if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
			t.Fatal(err)
		}
	}
	if res := run(t, term, "echo ok"); res.Output != "ok" || res.Status != "0" {
		t.Fatalf("run after two ctrl+c: %+v", res)
	}

	// The person's two, and what they typed after the second.
	old := personYield
	personYield = 50 * time.Millisecond
	defer func() { personYield = old }()
	settled(t, term)
	term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	term.PersonInput(nil, "mine; ", "")
	typedLine(t, term, "$ mine;")
	time.Sleep(2 * personYield)
	if _, err := term.Run(context.Background(), "echo ok", 5*time.Second, term.PersonGen()); !errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("run onto text the person typed after two ctrl+c: %v", err)
	}
	if strings.Contains(s.got(), "mine; echo") {
		t.Fatal("the run was typed onto the person's text")
	}
}

// And for the person: what they type after their own Ctrl+C, before the
// prompt it brings has arrived, is theirs on that prompt's line.
func TestTextAfterThePersonsCtrlCIsTheirs(t *testing.T) {
	old := personYield
	personYield = 50 * time.Millisecond
	defer func() { personYield = old }()
	s := startServer(t, func(s *testServer) { s.interruptDelay = 4 * promptSettle })
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	term.PersonInput(nil, "junk", "")
	term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	term.PersonInput(nil, "mine; ", "")
	typedLine(t, term, "$ mine;")
	time.Sleep(2 * personYield)
	if _, err := term.Run(context.Background(), "echo ok", 5*time.Second, term.PersonGen()); !errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("run onto text the person typed after ctrl+c: %v", err)
	}
	if strings.Contains(s.got(), "mine; echo") {
		t.Fatal("the run was typed onto the person's text")
	}
	// Their Ctrl+C with nothing after it leaves the line free, once the
	// prompt it brings is here. (The ten seconds a run leaves the person
	// cover that in the product; this test's yield is shorter than its
	// server's answer.)
	term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	typedLine(t, term, "$")
	if res := runWhenFree(t, term, "echo free"); res.Output != "free" {
		t.Fatalf("after the person's ctrl+c: %+v", res)
	}
	// What they type at that prompt is not ahead of anything: typed on
	// and erased, the line is free, and stays free after the next command.
	settled(t, term)
	term.PersonInput(nil, "x", "")
	term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
	if res := runWhenFree(t, term, "echo one"); res.Output != "one" {
		t.Fatalf("after typing and erasing: %+v", res)
	}
	if res := run(t, term, "echo two"); res.Output != "two" {
		t.Fatalf("the run after that: %+v", res)
	}
}

// The key is the line's, whoever pressed it: what the person types after
// the model's Ctrl+C, before the prompt it brings has arrived, is theirs
// on that prompt's line.
func TestTextAfterTheOthersCtrlCIsOnTheNextLine(t *testing.T) {
	old := personYield
	personYield = 50 * time.Millisecond
	defer func() { personYield = old }()
	s := startServer(t, func(s *testServer) { s.interruptDelay = 4 * promptSettle })
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)

	if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	term.PersonInput(nil, "mine; ", "")
	typedLine(t, term, "$ mine;")
	time.Sleep(2 * personYield)
	if _, err := term.Run(context.Background(), "echo ok", 5*time.Second, term.PersonGen()); !errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("run onto text the person typed after the model's ctrl+c: %v", err)
	}

	// Their Ctrl+C drops it, and once the prompt it brings is here the
	// line is free.
	term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	typedLine(t, term, "$")
	if res := runWhenFree(t, term, "echo free"); res.Output != "free" {
		t.Fatalf("after the person's ctrl+c: %+v", res)
	}
	if got := s.got(); strings.Contains(got, "mine; echo") {
		t.Fatalf("a run was typed onto text: %q", got)
	}
}

// A Ctrl+C that no prompt answered was not an interrupt: what was typed
// after it is on the line it was typed on, and goes when that line is run.
// It is not carried to the prompt after.
func TestTextAfterACtrlCTheShellDidNotTakeGoesWithItsLine(t *testing.T) {
	setWait(t, &interruptWait, 2*promptSettle)
	setWait(t, &personYield, 50*time.Millisecond)
	s := startServer(t, func(s *testServer) { s.noInterrupt = true })
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	if err := term.Type("x", false, term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	// Long enough that the key was no interrupt. Longer does no harm.
	time.Sleep(interruptWait + 50*time.Millisecond)
	// The person runs the line as it is.
	term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
	typedLine(t, term, "$")
	if res := runWhenFree(t, term, "echo ok"); res.Output != "ok" {
		t.Fatalf("after the line was run: %+v", res)
	}
}

// The person's Ctrl+C is taken for an interrupt when a prompt answers it,
// as the model's is. Where the shell reads the key as a character, it is
// one more character of theirs on the line: on an empty line too, and
// after they have erased what shows, until they send the line.
func TestThePersonsCtrlCCountsWhenTheShellTakesIt(t *testing.T) {
	setWait(t, &interruptWait, 2*promptSettle)
	setWait(t, &personYield, 50*time.Millisecond)
	s := startServer(t, func(s *testServer) { s.noInterrupt = true })
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	refused := func(when string) {
		t.Helper()
		// Long enough that the key was no interrupt. Longer does no harm.
		time.Sleep(interruptWait + 2*personYield)
		if _, err := term.Run(context.Background(), "echo ok", time.Second, term.PersonGen()); !errors.Is(err, ErrLineNotEmpty) {
			t.Fatalf("run %s: %v", when, err)
		}
	}
	term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	typedLine(t, term, "$ ^C")
	refused("onto the key the shell read as a character, on an empty line")
	term.PersonInput(nil, "x", "")
	typedLine(t, term, "$ ^Cx")
	refused("onto what the person typed after it")
	term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
	term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
	typedLine(t, term, "$")
	refused("after they erased what shows: no prompt has said where the line starts")
	// Sent, the line is gone, whatever it was.
	term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
	if res := runWhenFree(t, term, "echo one"); res.Output != "one" {
		t.Fatalf("after the person sent the line: %+v", res)
	}
	if res := run(t, term, "echo two"); res.Output != "two" {
		t.Fatalf("the run after that: %+v", res)
	}
	if strings.Contains(s.got(), "\x03echo") {
		t.Fatal("a run was typed onto the person's line")
	}
}

// With text typed after the key, no prompt can clear the line, and a run
// does not wait for one before it is refused.
func TestARunDoesNotWaitForAPromptThatCannotClearTheLine(t *testing.T) {
	s := startServer(t, func(s *testServer) { s.noInterrupt = true })
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	if err := term.Keys("ctrl+c x", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := term.Run(context.Background(), "echo ok", time.Second, term.PersonGen())
	if !errors.Is(err, ErrModelLine) || strings.Contains(err.Error(), "No prompt has followed") {
		t.Fatalf("run onto a key pressed after ctrl+c: %v", err)
	}
	if took := time.Since(start); took > interruptWait/2 {
		t.Fatalf("the run waited %s to be refused", took)
	}
}

// A paste is typed text to a shell that has not turned bracketed paste on,
// and a line end in it sends the line: what was pasted went with it. (With
// bracketed paste on it stays on the line: line_test.go.)
func TestAPastedLineEndSendsTheLineWhereTheShellReadsItAsTyped(t *testing.T) {
	old := personYield
	personYield = 50 * time.Millisecond
	defer func() { personYield = old }()
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	term.PersonInput(nil, "", "echo pasted\n")
	if res := runWhenFree(t, term, "echo after"); res.Output != "after" {
		t.Fatalf("after a pasted line that ran: %+v", res)
	}
	// What follows the line end in the same paste is typed after it: on
	// the line of the prompt that comes when the pasted line has run.
	settled(t, term)
	term.PersonInput(nil, "", "echo pasted\necho left")
	typedLine(t, term, "$ echo left")
	time.Sleep(2 * personYield)
	if _, err := term.Run(context.Background(), "echo x", time.Second, term.PersonGen()); !errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("run onto what was pasted after a line end: %v", err)
	}
	term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
	if res := runWhenFree(t, term, "echo after"); res.Output != "after" {
		t.Fatalf("after the person sent the rest: %+v", res)
	}
}

// The model's Ctrl+C to a running command stops it, which is what a run
// that timed out tells it to press. It is not text typed ahead: the next
// run goes, and the prompt is waited for as any other.
func TestCtrlCToARunningCommandIsNoTextTypedAhead(t *testing.T) {
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	res, err := term.Run(context.Background(), "sleep 5s", 300*time.Millisecond, term.PersonGen())
	if err != nil || !res.TimedOut {
		t.Fatalf("sleep: %+v %v", res, err)
	}
	if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if !term.WaitPrompt(ctx, 6*time.Second) {
		t.Fatal("no prompt after the ctrl+c that stopped the command")
	}
	if res := run(t, term, "echo after"); res.Output != "after" || res.Status != "0" {
		t.Fatalf("after: %+v", res)
	}

	// Text typed ahead and then a Ctrl+C: the key drops the text with it.
	res, err = term.Run(context.Background(), "sleep 5s", 300*time.Millisecond, term.PersonGen())
	if err != nil || !res.TimedOut {
		t.Fatalf("sleep: %+v %v", res, err)
	}
	if err := term.Type("ls", false, term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	if !term.WaitPrompt(ctx, 6*time.Second) {
		t.Fatal("no prompt after typed-ahead text and a ctrl+c")
	}
	if res := run(t, term, "echo again"); res.Output != "again" {
		t.Fatalf("after: %+v", res)
	}
}

// A remote shell's terminal takes three keys as signals; a Windows
// console only Ctrl+C (measured: see signalKey).
func TestSignalKeysAreTheOnesATerminalTakes(t *testing.T) {
	remote := &Terminal{m: &Manager{}}
	local := &Terminal{m: &Manager{opts: Options{Local: &LocalShell{}}}}
	for keys, want := range map[string][2]bool{
		"ctrl+c": {true, true}, "ctrl+\\": {true, false}, "ctrl+z": {true, false},
		"ctrl+d": {false, false}, "c": {false, false}, "Enter": {false, false}, "ctrl+x": {false, false},
	} {
		evs, err := ParseKeys(keys)
		if err != nil {
			t.Fatalf("%s: %v", keys, err)
		}
		if got := remote.signalKey(evs[0]); got != want[0] {
			t.Errorf("remote signalKey(%s) = %v, want %v", keys, got, want[0])
		}
		if got := local.signalKey(evs[0]); got != want[1] {
			t.Errorf("local signalKey(%s) = %v, want %v", keys, got, want[1])
		}
	}
}
