package sshterm

import (
	"context"
	"errors"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A remote that is slow.
//
// What the terminal believes about the command line (a prompt has been
// drawn, a Ctrl+C was answered) it works out from what has come back, and
// from a slow remote less has come back than has happened. Each belief
// below was first held too early: a run refused for text that was not on
// the line, or a command typed before the prompt it was meant for. A
// loaded machine did it to the tests now and then, and a slow remote does
// it to a person.

// slowPrompt is how long the test server's shell takes over a prompt: a
// few times the quiet that used to pass for one.
const slowPrompt = 4 * promptSettle

// noEmptyPrompt keeps the wait for a prompt that draws nothing out of a
// test that has a slow one: the two are told apart by the clock, and a
// test that can stall is not the place for a short one.
func noEmptyPrompt(t *testing.T) {
	t.Helper()
	old := promptWait
	promptWait = time.Minute
	t.Cleanup(func() { promptWait = old })
}

// The mark for the end of a command comes before the prompt, and a shell
// can take a while between the two: a prompt command of the person's, a
// status line from git on a big tree. Quiet after the mark was taken for
// the prompt, so the next run typed its command while the shell was still
// working the prompt out.
func TestNoRunIsTypedBeforeThePromptIsDrawn(t *testing.T) {
	noEmptyPrompt(t)
	s := startServer(t, func(s *testServer) { s.promptDelay = slowPrompt })
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	for i := 0; i < 3; i++ {
		if res := run(t, term, "echo again"); res.Output != "again" || res.Status != "0" {
			t.Fatalf("run %d: %+v", i, res)
		}
	}
	if early := s.gotEarly(); early != "" {
		t.Fatalf("typed while the shell was working out its prompt: %q", early)
	}
	// What is recorded as the prompt is the prompt.
	settled(t, term)
	term.mu.Lock()
	left, x := term.promptLeft, term.promptX
	term.mu.Unlock()
	if want := "me@test:~$ "; left != want || x != len(want) {
		t.Fatalf("the prompt was recorded as %q, the cursor at %d", left, x)
	}
}

// With the place before the prompt recorded as the prompt, a line the
// person typed on and erased never read as empty again, and every run was
// refused as theirs until they pressed Enter or Ctrl+C.
func TestAnErasedLineIsEmptyAfterASlowPrompt(t *testing.T) {
	noEmptyPrompt(t)
	old := personYield
	personYield = 50 * time.Millisecond
	defer func() { personYield = old }()
	s := startServer(t, func(s *testServer) { s.promptDelay = slowPrompt })
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	run(t, term, "echo one")
	settled(t, term)
	term.PersonInput(nil, "x", "")
	term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
	// With the prompt on the screen: a run that got in before it was drawn
	// found the line as the wrong record had it, and was let through.
	typedLine(t, term, "$")
	if res := runWhenFree(t, term, "echo after"); res.Output != "after" {
		t.Fatalf("after typing and erasing: %+v", res)
	}
}

// A prompt that draws nothing (PS1="") leaves the cursor where the mark
// did. It is taken as drawn once promptWait has passed with nothing drawn,
// and not before.
func TestAnEmptyPromptIsTakenAfterAWait(t *testing.T) {
	old := promptWait
	promptWait = 4 * promptSettle
	defer func() { promptWait = old }()
	s := startServer(t)
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	run(t, term, "noprompt")
	// How long after the mark the prompt was first seen as known. Looking
	// late only makes this longer.
	var after time.Duration
	within(t, 10*time.Second, "the empty prompt taken", func() bool {
		term.mu.Lock()
		defer term.mu.Unlock()
		after = time.Since(term.markAt)
		return term.promptKnown
	})
	if after < promptWait {
		t.Fatalf("a prompt that drew nothing was taken %s after the mark, before %s", after, promptWait)
	}
	if res := run(t, term, "echo still"); res.Output != "still" {
		t.Fatalf("at an empty prompt: %+v", res)
	}
}

// The same against the real shells, with a prompt command that takes a
// while. bash without PS0 (3.2, the one macOS ships) has no mark for where
// a command's output starts: a command typed before its prompt came back
// with the prompt and its own echo as output.
func TestRealShellsWithASlowPrompt(t *testing.T) {
	noEmptyPrompt(t)
	old := personYield
	personYield = 50 * time.Millisecond
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
			if filepath.Base(shell) == "zsh" {
				run(t, term, `__slow() { sleep 0.5 }; precmd_functions+=(__slow)`)
			} else {
				run(t, term, `PROMPT_COMMAND="$PROMPT_COMMAND; sleep 0.5"`)
			}
			for i := 0; i < 2; i++ {
				if res := run(t, term, "echo again"); res.Output != "again" || res.Status != "0" {
					t.Fatalf("run %d: %+v", i, res)
				}
			}
			settled(t, term)
			term.PersonInput(nil, "x", "")
			term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
			typedLine(t, term, map[string]string{"bash": "$", "zsh": "%"}[filepath.Base(shell)])
			if res := runWhenFree(t, term, "echo after"); res.Output != "after" {
				t.Fatalf("after typing and erasing: %+v", res)
			}
		})
	}
}

// The prompt that answers the model's Ctrl+C says the shell took the key
// and dropped the line. run waited two seconds for it, counted from the
// key: from a remote that took longer the run was refused for text that
// was no longer there, and told to press the key it had just pressed. The
// prompt after it is waited for as any other is: the three seconds a run
// gives a prompt to be drawn start when the answer comes, not before it.
func TestRunWaitsForASlowAnswerToCtrlC(t *testing.T) {
	noEmptyPrompt(t)
	s := startServer(t, func(s *testServer) {
		s.interruptDelay = 3200 * time.Millisecond
		s.promptDelay = slowPrompt
	})
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	if err := term.Type("junk", false, term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	if res := run(t, term, "echo afterc"); res.Output != "afterc" || res.Status != "0" {
		t.Fatalf("run after a ctrl+c answered late: %+v", res)
	}
	if early := s.gotEarly(); early != "" {
		t.Fatalf("typed while the shell was working out its prompt: %q", early)
	}
}

// Where no prompt follows the key, the refusal says that: pressing it
// again is what the model is told for text it never tried to clear, and
// it has just done that.
func TestACtrlCThatBringsNoPromptIsSaidSo(t *testing.T) {
	old := interruptWait
	interruptWait = 2 * promptSettle
	defer func() { interruptWait = old }()
	s := startServer(t, func(s *testServer) { s.noInterrupt = true })
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	if err := term.Type("junk", false, term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	_, err := term.Run(context.Background(), "echo x", time.Second, term.PersonGen())
	if !errors.Is(err, ErrModelLine) || strings.Contains(err.Error(), "No prompt has followed") {
		t.Fatalf("run onto text the model did not clear: %v", err)
	}
	if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	_, err = term.Run(context.Background(), "echo x", time.Second, term.PersonGen())
	if !errors.Is(err, ErrModelLine) || !strings.Contains(err.Error(), "No prompt has followed the ctrl+c") {
		t.Fatalf("run after a ctrl+c the shell did not take: %v", err)
	}
	if strings.Contains(s.got(), "echo x") {
		t.Fatal("the command was typed onto the line")
	}
}

// The wait for that prompt ends when the terminal does.
func TestTheWaitForCtrlCEndsWithTheTerminal(t *testing.T) {
	s := startServer(t, func(s *testServer) { s.noInterrupt = true })
	m := testManager(t, testHome(t, s, true))
	term := connected(t, m, s)
	if err := term.Type("junk", false, term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	if err := term.Keys("ctrl+c", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := term.Run(context.Background(), "echo x", time.Second, term.PersonGen())
		done <- err
	}()
	term.Close("closed")
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotConnected) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(interruptWait / 2):
		t.Fatal("the run waited on a terminal that had ended")
	}
}
