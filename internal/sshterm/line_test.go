package sshterm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The command line, every order of keys.
//
// One rule decides whether a run may be typed: no text may be on the line
// the shell will read it on. The terminal cannot see that line. It has what
// it sent, in order, and the prompts that have come back so far, and a
// prompt can be late by any amount. Cases found one at a time each had the
// same shape (a prompt's mark clearing text that was typed after the key
// that brought the prompt), so this goes through all of them: every order
// of the model typing, the person typing, either pressing Ctrl+C, the
// person pressing Enter, and a prompt arriving, to five steps, against a
// shell that takes Ctrl+C as an interrupt and one that reads it as a
// character.
//
// The test plays the shell. It keeps what a shell would have on its line
// after reading everything sent so far, and hands the terminal the shell's
// output a prompt at a time, as late as the order says. No clock is in it.

// playedShell is the shell's side: its line, and what it has printed that
// the terminal has not been given yet.
type playedShell struct {
	term *Terminal
	// intr is whether the shell takes Ctrl+C as an interrupt.
	intr bool
	// text is whether anything is on the shell's command line.
	text bool
	// out is what it has printed, in order. A prompt ends a piece.
	out []played
	// sending is a line the person sent whose prompt has not arrived.
	sending bool
}

type played struct {
	data   string
	prompt bool
	sent   bool // the prompt after a line the person sent
}

// newPlayedShell is a shell at its first prompt, in a terminal of m's. The
// caller ends the terminal: thirty thousand of them kept until the test is
// over are gigabytes.
func newPlayedShell(m *Manager, intr bool) *playedShell {
	term := newTerminal(m, "c", "t1", "h", ByModel)
	term.mu.Lock()
	term.state = StateConnected
	term.mu.Unlock()
	s := &playedShell{term: term, intr: intr}
	s.out = append(s.out, played{data: s.promptText(0), prompt: true})
	s.arrive()
	return s
}

func (s *playedShell) promptText(status int) string {
	return "\x1b]133;D;" + itoa(status) + ";lc=" + s.term.marks.nonce + "\x07$ "
}

// typed is the shell reading text: on its line, and echoed.
func (s *playedShell) typed(text string) {
	s.text = true
	s.out = append(s.out, played{data: text})
}

// ctrlC is the shell reading the key.
func (s *playedShell) ctrlC() {
	if !s.intr {
		s.typed("^C")
		return
	}
	s.text = false
	s.out = append(s.out, played{data: "^C\r\n" + s.promptText(130), prompt: true})
}

// enter is the shell reading a line end: the line is run, and a prompt
// follows.
func (s *playedShell) enter() {
	s.text = false
	s.sending = true
	s.out = append(s.out, played{data: "\r\n" + s.promptText(0), prompt: true, sent: true})
}

// late reports a prompt the shell has printed that has not arrived.
func (s *playedShell) late() bool {
	for _, p := range s.out {
		if p.prompt {
			return true
		}
	}
	return false
}

// arrive hands the terminal the shell's output up to and with the next
// prompt, and has the terminal take the prompt as drawn.
func (s *playedShell) arrive() {
	for len(s.out) > 0 {
		p := s.out[0]
		s.out = s.out[1:]
		s.term.output([]byte(p.data))
		if p.prompt {
			if p.sent {
				s.sending = false
			}
			break
		}
	}
	s.quiet()
}

// quiet is output having been quiet for as long as the terminal waits to
// take a prompt as drawn. The timer the terminal armed for that is
// stopped: nothing here runs on a goroutine of its own.
func (s *playedShell) quiet() {
	s.term.mu.Lock()
	if s.term.settle != nil {
		s.term.settle.Stop()
	}
	s.term.lastOutput = time.Now().Add(-time.Minute)
	s.term.mu.Unlock()
	s.term.settled()
	s.term.mu.Lock()
	if s.term.settle != nil {
		s.term.settle.Stop()
	}
	s.term.mu.Unlock()
}

// prints is the shell printing something outside the keys above.
func (s *playedShell) prints(data string) {
	s.term.output([]byte(data))
	s.quiet()
}

// echoes hands over what the shell has printed short of the next prompt:
// the echo of what was typed at the prompt that is on the screen.
func (s *playedShell) echoes() {
	for len(s.out) > 0 && !s.out[0].prompt {
		s.term.output([]byte(s.out[0].data))
		s.out = s.out[1:]
	}
	s.term.mu.Lock()
	if s.term.settle != nil {
		s.term.settle.Stop()
	}
	s.term.mu.Unlock()
}

// step does one thing, and reports whether the order allows it there.
func (s *playedShell) step(op string) bool {
	gen := s.term.PersonGen()
	switch op {
	case "arrive":
		if !s.late() {
			return false
		}
		s.arrive()
		return true
	}
	// With a line of the person's sent and its prompt not here, a command
	// is running as far as the terminal knows. What is typed then is not
	// this test's: see IMPROVEMENTS item 65.
	if s.sending {
		return false
	}
	if strings.HasPrefix(op, "model") {
		// The model acts ten seconds after the person's last key at the
		// soonest: what they typed at the prompt on the screen has been
		// echoed by then.
		s.echoes()
	}
	switch op {
	case "model types":
		if s.term.Type("m", false, gen) == nil {
			s.typed("m")
		}
	case "model ctrl+c":
		if s.term.Keys("ctrl+c", gen) == nil {
			s.ctrlC()
		}
	case "model ctrl+c, key":
		if s.term.Keys("ctrl+c m", gen) == nil {
			s.ctrlC()
			s.typed("m")
		}
	case "model key, ctrl+c":
		if s.term.Keys("m ctrl+c", gen) == nil {
			s.typed("m")
			s.ctrlC()
		}
	case "person types":
		s.term.PersonInput(nil, "p", "")
		s.typed("p")
	case "person ctrl+c":
		s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
		s.ctrlC()
	case "person enter":
		s.term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
		s.enter()
	}
	return true
}

// runs tries a run, and reports whether it was typed. A run waits for the
// prompt that answers a Ctrl+C, for so long. This is the run at the end of
// that wait, with the prompt still not here. Typed is told by the terminal
// taking a command as started, not by how the call came back: one that was
// neither typed nor refused in two seconds was waiting for something.
func (s *playedShell) runs() (typed bool, err error) {
	s.echoes()
	wait := interruptWait
	interruptWait = 0
	defer func() { interruptWait = wait }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = s.term.Run(ctx, "true", time.Millisecond, s.term.PersonGen())
	s.term.mu.Lock()
	typed = s.term.running
	s.term.mu.Unlock()
	return typed, err
}

var lineSteps = []string{
	"model types", "model ctrl+c", "model ctrl+c, key", "model key, ctrl+c",
	"person types", "person ctrl+c", "person enter", "arrive",
}

func TestNoRunIsTypedOntoTextInAnyOrderOfKeys(t *testing.T) {
	// No clock: the person's last key is no reason to refuse, and a
	// prompt that answers a Ctrl+C can be as late as the order has it.
	oldYield, oldWait := personYield, interruptWait
	personYield, interruptWait = 0, time.Hour
	defer func() { personYield, interruptWait = oldYield, oldWait }()

	m := NewManager(Options{Home: t.TempDir(), DataDir: t.TempDir(), LocalUser: "me"})
	// Five steps: 29,693 orders. Four under the race detector, which has
	// one goroutine to watch here and takes four times as long over it; the
	// suite's other lane goes to five.
	depth := 5
	if testing.Short() || raceDetector {
		depth = 4
	}
	tried, typedOnto, refusedEmpty, waited := 0, 0, 0, 0
	for _, intr := range []bool{true, false} {
		var walk func(order []string)
		walk = func(order []string) {
			if typedOnto+refusedEmpty+waited >= 20 {
				return // enough to say what is wrong
			}
			if len(order) > 0 {
				s := newPlayedShell(m, intr)
				for _, op := range order {
					if !s.step(op) {
						s.term.end("done")
						return // not an order that can happen: nor is any longer one
					}
				}
				tried++
				if s.late() && !s.text {
					// Nothing to hold a run to: it may wait for the prompt
					// that is on its way.
					s.term.end("done")
					goto longer
				}
				typed, err := s.runs()
				switch {
				case !typed && errors.Is(err, context.DeadlineExceeded):
					waited++
					if waited <= 5 {
						t.Errorf("interrupt=%v, %s: the run was neither typed nor refused: it waited", intr, strings.Join(order, "; "))
					}
				case typed && s.text:
					typedOnto++
					if typedOnto <= 5 {
						t.Errorf("interrupt=%v, %s: the run was typed onto text on the line", intr, strings.Join(order, "; "))
					}
				case !typed && !s.text && !s.late():
					refusedEmpty++
					if refusedEmpty <= 5 {
						t.Errorf("interrupt=%v, %s: the run was refused with the line empty and every prompt here: %v", intr, strings.Join(order, "; "), err)
					}
				}
				s.term.end("done")
			}
		longer:
			if len(order) == depth {
				return
			}
			for _, op := range lineSteps {
				walk(append(order[:len(order):len(order)], op))
			}
		}
		walk(nil)
	}
	if typedOnto > 0 || refusedEmpty > 0 || waited > 0 {
		t.Fatalf("of %d orders tried: %d typed onto text, %d refused an empty line, %d left waiting", tried, typedOnto, refusedEmpty, waited)
	}
	if tried < 1000 {
		t.Fatalf("only %d orders were tried", tried)
	}
	t.Logf("%d orders", tried)
}

// playedFor is a played shell for one test, with no wait after the
// person's keys.
func playedFor(t *testing.T, intr bool) *playedShell {
	t.Helper()
	old := personYield
	personYield = 0
	t.Cleanup(func() { personYield = old })
	s := newPlayedShell(NewManager(Options{Home: t.TempDir(), DataDir: t.TempDir(), LocalUser: "me"}), intr)
	t.Cleanup(func() { s.term.end("done") })
	return s
}

// run tries a run as the model would, and returns what it was told.
func (s *playedShell) run() error {
	_, err := s.term.Run(context.Background(), "echo ok", time.Millisecond, s.term.PersonGen())
	return err
}

// The keys a shell reads as Enter send the line as Enter does: with only
// Enter known, a line sent with Ctrl+M or Ctrl+J was taken as still typed
// on at the prompt after it. Ctrl+O sends it too, and bash then puts the
// next line of its history on the new prompt, which nobody typed.
func TestTheKeysAShellReadsAsEnterSendTheLine(t *testing.T) {
	for _, key := range []string{"m", "j"} {
		s := playedFor(t, true)
		s.term.PersonInput(nil, "ls", "")
		s.term.PersonInput(&PersonKey{Key: key, Ctrl: true}, "", "")
		s.prints("ls\r\n" + s.promptText(0))
		if err := s.run(); err != nil {
			t.Fatalf("after the person's ctrl+%s: %v", key, err)
		}
	}
	s := playedFor(t, true)
	s.term.PersonInput(nil, "ls", "")
	s.term.PersonInput(&PersonKey{Key: "o", Ctrl: true}, "", "")
	s.prints("ls\r\n" + s.promptText(0) + "make test")
	if err := s.run(); !errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("onto the line ctrl+o left on the prompt: %v", err)
	}
	s.term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
	s.prints("\r\n" + s.promptText(0))
	if err := s.run(); err != nil {
		t.Fatalf("after the person sent it: %v", err)
	}
}

// What is pasted into a command that is running is that command's to
// read. The part after a line end is not text on the shell's next line.
func TestAPasteIntoARunningCommandIsNotOnTheNextLine(t *testing.T) {
	s := playedFor(t, true)
	s.term.PersonInput(nil, "cat", "")
	s.term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
	s.term.PersonInput(nil, "", "line1\nline2")
	s.term.PersonInput(&PersonKey{Key: "d", Ctrl: true}, "", "")
	s.prints("cat\r\nline1\r\nline2\r\n" + s.promptText(0))
	if err := s.run(); err != nil {
		t.Fatalf("after a paste into a command that has ended: %v", err)
	}
}

// A shell with bracketed paste keeps a paste on its line, line ends and
// all. With the prompt's line starting at the left edge (PS1 ending in a
// line break) a pasted line and its line end left the cursor on an empty
// row at the prompt's column, which read as the prompt left it.
func TestABracketedPasteWithALineEndIsOnTheLine(t *testing.T) {
	s := playedFor(t, true)
	s.prints("\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07me@test\r\n\x1b[?2004h")
	s.term.PersonInput(nil, "", "rm -rf /tmp/x\n")
	s.prints("rm -rf /tmp/x\r\n")
	s.term.mu.Lock()
	typed, running := s.term.personTyped, s.term.running
	s.term.mu.Unlock()
	if !typed || running {
		t.Fatalf("a bracketed paste with a line end: personTyped=%v running=%v", typed, running)
	}
	if err := s.run(); !errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("run onto a pasted line the shell is holding: %v", err)
	}
	s.term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
	s.prints("\r\n\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07me@test\r\n")
	if err := s.run(); err != nil {
		t.Fatalf("after the person sent it: %v", err)
	}
}

// A program that is killed leaves the terminal on its alternate screen,
// and the shell draws its prompt there. The terminal does not follow a
// command line on that screen (keys go to a full-screen program as they
// are), so a run is typed on none.
func TestNoRunIsTypedOnTheAlternateScreen(t *testing.T) {
	for _, who := range []string{"person", "model", "nobody"} {
		s := playedFor(t, true)
		s.prints("\x1b[?1049h" + s.promptText(0))
		switch who {
		case "person":
			s.term.PersonInput(nil, "rm -rf x;", "")
		case "model":
			if err := s.term.Type("rm -rf x;", false, s.term.PersonGen()); err != nil {
				t.Fatal(err)
			}
		}
		s.prints("rm -rf x;")
		if err := s.run(); !errors.Is(err, ErrFullScreen) {
			t.Fatalf("run on the alternate screen, %s having typed: %v", who, err)
		}
		// Back on the main screen it is a prompt like any other.
		s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
		s.prints("^C\r\n\x1b[?1049l" + s.promptText(130))
		if err := s.run(); err != nil {
			t.Fatalf("back on the main screen: %v", err)
		}
	}
}

// A Ctrl+C that no prompt answered in time is not waited for again when
// the next one is pressed: the one prompt that comes answers that one.
func TestACtrlCThatWentUnansweredIsNotWaitedForAgain(t *testing.T) {
	old := interruptWait
	interruptWait = 2 * promptSettle
	defer func() { interruptWait = old }()
	s := playedFor(t, true)
	if err := s.term.Keys("ctrl+c", s.term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	// Longer than the wait. Longer does no harm.
	time.Sleep(interruptWait + 50*time.Millisecond)
	if err := s.term.Keys("ctrl+c", s.term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	interruptWait = time.Hour // from here, any press still counted would be waited for
	s.prints("^C\r\n" + s.promptText(130))
	s.term.mu.Lock()
	left, known := s.term.interrupts.pending(), s.term.promptKnown
	s.term.mu.Unlock()
	if left != 0 || !known {
		t.Fatalf("after the one prompt: a press still waited for (%s), prompt recorded=%v", left, known)
	}
	if err := s.run(); err != nil {
		t.Fatalf("run after it: %v", err)
	}
}
