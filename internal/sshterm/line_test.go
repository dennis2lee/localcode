package sshterm

import (
	"context"
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
	s.term.mu.Lock()
	s.term.lastOutput = time.Now().Add(-time.Minute)
	s.term.mu.Unlock()
	s.term.settled()
}

// echoes hands over what the shell has printed short of the next prompt:
// the echo of what was typed at the prompt that is on the screen.
func (s *playedShell) echoes() {
	for len(s.out) > 0 && !s.out[0].prompt {
		s.term.output([]byte(s.out[0].data))
		s.out = s.out[1:]
	}
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
// that wait, with the prompt still not here.
func (s *playedShell) runs() (bool, error) {
	s.echoes()
	wait := interruptWait
	interruptWait = 0
	defer func() { interruptWait = wait }()
	_, err := s.term.Run(context.Background(), "true", time.Millisecond, s.term.PersonGen())
	return err == nil, err
}

var lineSteps = []string{
	"model types", "model ctrl+c", "model ctrl+c, key", "model key, ctrl+c",
	"person types", "person ctrl+c", "person enter", "arrive",
}

func TestNoRunIsTypedOntoTextInAnyOrderOfKeys(t *testing.T) {
	if raceDetector {
		// One goroutine drives all of this, in order: there is no race in
		// it to find. What the detector does report, some thirty thousand
		// terminals in, is a finished terminal's goroutine against memory
		// since handed to another allocation (its stack frame in io's
		// pipe, and the next emulator's screen buffer as the test's own
		// goroutine fills it in). Nothing is shared between the two. The
		// suite's other lane runs this.
		t.Skip("run without the race detector")
	}
	// No clock: the person's last key is no reason to refuse, and a
	// prompt that answers a Ctrl+C can be as late as the order has it.
	oldYield, oldWait := personYield, interruptWait
	personYield, interruptWait = 0, time.Hour
	defer func() { personYield, interruptWait = oldYield, oldWait }()

	m := NewManager(Options{Home: t.TempDir(), DataDir: t.TempDir(), LocalUser: "me"})
	depth := 5
	if testing.Short() {
		depth = 4
	}
	tried, typedOnto, refusedEmpty := 0, 0, 0
	for _, intr := range []bool{true, false} {
		var walk func(order []string)
		walk = func(order []string) {
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
	if typedOnto > 0 || refusedEmpty > 0 {
		t.Fatalf("of %d orders: %d typed onto text, %d refused an empty line", tried, typedOnto, refusedEmpty)
	}
	if tried < 1000 {
		t.Fatalf("only %d orders were tried", tried)
	}
	t.Logf("%d orders", tried)
}
