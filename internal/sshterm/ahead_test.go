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
// on that prompt's line, and what the model types after the person's is
// the model's.
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

	// Until the prompt that answers their Ctrl+C is here, the line is
	// still the person's, and the model types nothing onto it.
	term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	time.Sleep(2 * personYield)
	if err := term.Type("left; ", false, term.PersonGen()); !errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("type before the person's ctrl+c was answered: %v", err)
	}
	typedLine(t, term, "$")
	// On an empty line their Ctrl+C leaves nothing of theirs, and what
	// the model types before its prompt is here is the model's, on the
	// line of that prompt.
	term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	time.Sleep(2 * personYield)
	if err := term.Type("left; ", false, term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	typedLine(t, term, "$ left;")
	if _, err := term.Run(context.Background(), "echo ok", 5*time.Second, term.PersonGen()); !errors.Is(err, ErrModelLine) {
		t.Fatalf("run onto text the model typed after the person's ctrl+c: %v", err)
	}
	if got := s.got(); strings.Contains(got, "mine; echo") || strings.Contains(got, "left; echo") {
		t.Fatalf("a run was typed onto text: %q", got)
	}
}

// A Ctrl+C that no prompt answered was not an interrupt: what was typed
// after it is on the line it was typed on, and goes when that line is run.
// It is not carried to the prompt after.
func TestTextAfterACtrlCTheShellDidNotTakeGoesWithItsLine(t *testing.T) {
	oldWait, oldYield := interruptWait, personYield
	interruptWait, personYield = 2*promptSettle, 50*time.Millisecond
	defer func() { interruptWait, personYield = oldWait, oldYield }()
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
// as the model's is. Where the shell reads the key as a character, their
// line is still theirs: the key is on it. Once they have erased it all,
// what they typed after the key is not carried to a later prompt.
func TestThePersonsCtrlCCountsWhenTheShellTakesIt(t *testing.T) {
	oldWait, oldYield := interruptWait, personYield
	interruptWait, personYield = 2*promptSettle, 50*time.Millisecond
	defer func() { interruptWait, personYield = oldWait, oldYield }()
	s := startServer(t, func(s *testServer) { s.noInterrupt = true })
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	term.PersonInput(nil, "x", "")
	term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	term.PersonInput(nil, "y", "")
	typedLine(t, term, "$ x^Cy")
	// Long enough that the key was no interrupt. Longer does no harm.
	time.Sleep(interruptWait + 2*personYield)
	if _, err := term.Run(context.Background(), "echo ok", time.Second, term.PersonGen()); !errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("run onto a line the person's ctrl+c did not clear: %v", err)
	}
	for i := 0; i < 3; i++ {
		term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
	}
	if res := runWhenFree(t, term, "echo one"); res.Output != "one" {
		t.Fatalf("after the person erased the line: %+v", res)
	}
	if res := run(t, term, "echo two"); res.Output != "two" {
		t.Fatalf("the run after that: %+v", res)
	}
	if strings.Contains(s.got(), "\x03echo") {
		t.Fatal("a run was typed onto the person's line")
	}
}
