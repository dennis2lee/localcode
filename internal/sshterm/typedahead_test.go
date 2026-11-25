package sshterm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Text the person types while a command runs, and does not send, waits in
// the terminal when the command does not read it, and the shell draws it
// right after its next prompt: bash 3.2 and zsh 5.9, measured, in the
// same write as the prompt. A run typed then was typed onto it. The
// terminal reads the line at that prompt for it, once the prompt is drawn:
// what is drawn of it is theirs until they erase it, send it or press
// Ctrl+C, and a line with none of it on is empty. These play the shell.

// commandRuns is the person sending the line: a command runs, and the
// prompt after it has not come.
func (s *playedShell) commandRuns() {
	s.term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
	s.prints("\r\n")
}

// ended is the command ending: the terminal's own echo of what was typed
// while it ran, the mark and the prompt, and what the shell drew after
// the prompt.
func (s *playedShell) ended(echo, drawn string) {
	s.prints(echo + s.promptText(0) + drawn)
}

// erase is the person pressing Backspace n times at the prompt, and the
// shell taking a character off the line each time.
func (s *playedShell) erase(n int) {
	for i := 0; i < n; i++ {
		s.term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
		s.prints("\b \b")
	}
}

func refusedForThem(t *testing.T, s *playedShell, what string) {
	t.Helper()
	if err := s.run(); !errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("%s: %v", what, err)
	}
}

func allowed(t *testing.T, s *playedShell, what string) {
	t.Helper()
	if err := s.run(); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func TestTextTypedWhileACommandRanIsOnTheNextLine(t *testing.T) {
	t.Run("drawn after the prompt, on the row of its echo, as bash does", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf x", "")
		s.ended("rm -rf x", "rm -rf x")
		refusedForThem(t, s, "run onto what was typed while the command ran")
		s.erase(len("rm -rf x"))
		allowed(t, s, "run once the person erased it")
	})
	t.Run("drawn after the prompt, on a row of its own, as zsh does", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf x", "")
		s.ended("rm -rf x\r\n", "rm -rf x")
		refusedForThem(t, s, "run onto what was typed while the command ran")
		s.erase(3)
		refusedForThem(t, s, "run with part of it erased")
		s.erase(len("rm -rf x") - 3)
		allowed(t, s, "run once the person erased it")
	})
	t.Run("pasted", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "", "rm -rf x")
		s.ended("rm -rf x", "rm -rf x")
		refusedForThem(t, s, "run onto what was pasted while the command ran")
	})
	t.Run("pasted with a line end, bracketed", func(t *testing.T) {
		// A program that turned bracketed paste on: the line end stays in
		// the paste, and the shell keeps it all on the line, over two rows.
		s := playedFor(t, true)
		s.commandRuns()
		s.prints("\x1b[?2004h")
		s.term.PersonInput(nil, "", "x\r")
		s.ended("", "x\r\n")
		refusedForThem(t, s, "run onto a bracketed paste with a line end")
	})
	t.Run("read by the command", func(t *testing.T) {
		// q to a pager: the pager read it, and nothing waits.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "q", "")
		s.ended("", "")
		allowed(t, s, "run after a key the command read")
	})
	t.Run("the command read the start of it", func(t *testing.T) {
		// q quit the pager, and what came after it waited for the shell.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "qls", "")
		s.ended("", "ls")
		refusedForThem(t, s, "run onto the rest of it")
		s.erase(1)
		refusedForThem(t, s, "run with a character of it left")
		s.erase(1)
		allowed(t, s, "run once the person erased it")
	})
	t.Run("wide characters", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "한글", "")
		s.ended("", "한글")
		refusedForThem(t, s, "run onto Hangul typed while the command ran")
		for i := 0; i < 2; i++ {
			s.term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
			s.prints("\b\b  \b\b")
		}
		allowed(t, s, "run once the person erased it")
	})
	t.Run("longer than the row", func(t *testing.T) {
		// The rows before the cursor's are not read: the line is theirs
		// until they send it or press Ctrl+C, erased or not.
		s := playedFor(t, true)
		long := strings.Repeat("a", DefaultCols)
		s.commandRuns()
		s.term.PersonInput(nil, long, "")
		s.ended("", long)
		refusedForThem(t, s, "run onto a long line typed while the command ran")
		s.erase(len(long))
		refusedForThem(t, s, "run with a long line erased")
		s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
		s.prints("^C\r\n" + s.promptText(130))
		allowed(t, s, "run after the prompt their Ctrl+C brought")
	})
	t.Run("spaces", func(t *testing.T) {
		// ASCII spaces alone change nothing a command line runs: drawn or
		// not, the line reads as empty. A space that is no ASCII space (a
		// no-break space, an ideographic one) is part of a command's name to
		// the shell, and counts as text.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "   ", "")
		s.ended("   ", "   ")
		allowed(t, s, "run after ASCII spaces typed while the command ran")
		for _, sp := range []string{"\u00a0", "\u3000"} {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(nil, sp, "")
			s.ended(sp, sp)
			refusedForThem(t, s, "run after a space that is no ASCII space")
			s.term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
			n := runeCells(sp)
			s.prints(strings.Repeat("\b", n) + strings.Repeat(" ", n) + strings.Repeat("\b", n))
			allowed(t, s, "run once the person erased it")
		}
	})
	t.Run("completed by the shell", func(t *testing.T) {
		// Tab: what the shell draws is not what was typed.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "cd /tm\t", "")
		s.ended("cd /tm", "cd /tmp/")
		refusedForThem(t, s, "run onto a line the shell completed")
	})
	t.Run("a wide character erased", func(t *testing.T) {
		// Where the terminal's line editing does not count UTF-8,
		// Backspace takes one byte of it off, and the shell draws what is
		// left as it can: not what was typed, less a character.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "한", "")
		s.term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
		s.ended("", "\ufffd")
		refusedForThem(t, s, "run onto what is left of an erased wide character")
		// A key, then: read or not.
		s = playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "한", "")
		s.term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
		s.ended("", "")
		refusedForThem(t, s, "run after Backspace on a wide character, read or not")
	})
	t.Run("edited as the terminal edits a line", func(t *testing.T) {
		// Backspace and Ctrl+U where a command reads whole lines: the
		// terminal's line editing applies them before the shell reads.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf lsx", "")
		s.term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
		s.ended("rm -rf lsx\b \b", "rm -rf ls")
		refusedForThem(t, s, "run onto an edited line")
		s.erase(len("rm -rf ls"))
		allowed(t, s, "run once the person erased it")

		// Ctrl+W: a word is not the same to the terminal and to zsh, and
		// the line is judged as after a key.
		s = playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "scp a$ b", "")
		s.term.PersonInput(&PersonKey{Key: "w", Ctrl: true}, "", "")
		s.ended("", "scp a$ ")
		refusedForThem(t, s, "run onto a line Ctrl+W edited")
		s.erase(len("scp a$ "))
		refusedForThem(t, s, "run with it erased: nothing said where the prompt ends")

		s = playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf x", "")
		s.term.PersonInput(&PersonKey{Key: "u", Ctrl: true}, "", "")
		s.term.PersonInput(nil, "ls", "")
		s.ended("", "ls")
		refusedForThem(t, s, "run onto what was typed after Ctrl+U")
		s.erase(2)
		allowed(t, s, "run once the person erased it")
	})
}

func TestKeysTypedWhileACommandRan(t *testing.T) {
	t.Run("read by the command", func(t *testing.T) {
		// Arrows and Page Down to a pager: what the shell would make of a
		// key left for it the screen cannot be read for, so the line is
		// theirs until their Enter or Ctrl+C, read by the command or not.
		s := playedFor(t, true)
		s.commandRuns()
		for _, k := range []string{"ArrowDown", "PageDown", "ArrowUp"} {
			s.term.PersonInput(&PersonKey{Key: k}, "", "")
		}
		s.term.PersonInput(nil, "q", "")
		s.ended("", "")
		refusedForThem(t, s, "run after keys typed while the command ran")
		s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
		s.prints("^C\r\n" + s.promptText(130))
		allowed(t, s, "run after the prompt their Ctrl+C brought")
	})
	t.Run("left for the shell, which drew what they did", func(t *testing.T) {
		// Up: the shell put the last command on the line.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "ArrowUp"}, "", "")
		s.ended("^[[A", "sleep 1")
		refusedForThem(t, s, "run onto a command the shell recalled")
		s.term.PersonInput(&PersonKey{Key: "u", Ctrl: true}, "", "")
		s.prints("\r" + strings.Repeat(" ", 9) + "\r$ ")
		refusedForThem(t, s, "run with the line cleared: nothing said where the prompt ends")
		s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
		s.prints("^C\r\n" + s.promptText(130))
		allowed(t, s, "run after the prompt their Ctrl+C brought")
	})
	t.Run("text, and Home left for the shell", func(t *testing.T) {
		// The cursor where the prompt ends, the text right of it.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "ls", "")
		s.term.PersonInput(&PersonKey{Key: "Home"}, "", "")
		s.ended("ls^[[H\r\n", "ls\b\b")
		refusedForThem(t, s, "run onto text with the cursor moved before it")
	})
	t.Run("a prompt that is not the last one's", func(t *testing.T) {
		for _, prompt := range []string{"~/x $ ", "# "} {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(&PersonKey{Key: "ArrowLeft"}, "", "")
			s.prints(s.promptText(0)[:len(s.promptText(0))-2] + prompt)
			refusedForThem(t, s, "run after a key, at a prompt the screen cannot compare: "+prompt)
		}
	})
	t.Run("a recalled line whose last row reads as the last prompt", func(t *testing.T) {
		// Up recalled a long command, wrapped to a row that reads as the
		// prompt: the row above reaches the last column.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "ArrowUp"}, "", "")
		s.ended("", strings.Repeat("a", DefaultCols-2)+"$ ")
		refusedForThem(t, s, "run onto a recalled line that wrapped to a row like the prompt")
		// The same with a space in the last column, which looks like a
		// blank cell.
		s = playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "ArrowUp"}, "", "")
		s.ended("", strings.Repeat("a", DefaultCols-3)+" $ ")
		refusedForThem(t, s, "run onto a recalled line that wrapped at a space")
	})
	t.Run("Ctrl+O, which ends no line in the terminal", func(t *testing.T) {
		// The terminal keeps the text before it for the shell (macOS takes
		// the key for its own; Linux passes it on), and bash then runs it.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "rm", "")
		s.term.PersonInput(&PersonKey{Key: "o", Ctrl: true}, "", "")
		s.ended("rm", "rm")
		refusedForThem(t, s, "run onto text typed before Ctrl+O")
	})
	t.Run("a key the shell waits after", func(t *testing.T) {
		// Escape and Ctrl+X: the shell waits for the next key with nothing
		// shown, and the run's first character would have gone with it.
		for _, k := range []PersonKey{{Key: "Escape"}, {Key: "x", Ctrl: true}, {Key: "b", Alt: true}} {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(&k, "", "")
			s.ended("", "")
			refusedForThem(t, s, "run after "+k.Key+" left for the shell")
			s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
			s.prints("^C\r\n" + s.promptText(130))
			allowed(t, s, "run after the prompt their Ctrl+C brought")
		}
	})
}

// Ctrl+D to a command that reads input (cat, a REPL), at the start of a
// line: the end of its input, nothing left for the shell.
func TestCtrlDToACommandEndsItsInput(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(nil, "hello\r", "")
	s.term.PersonInput(&PersonKey{Key: "d", Ctrl: true}, "", "")
	s.ended("hello\r\nhello\r\n", "")
	allowed(t, s, "run after Ctrl+D ended a command's input")
}

// A line end sends what was typed before it to whatever reads it, and a
// key that signals the command has the terminal drop what waits. Either
// way nothing of it is left for the shell.
func TestALineEndOrASignalLeavesNothingWaiting(t *testing.T) {
	for _, k := range []PersonKey{{Key: "Enter"}, {Key: "c", Ctrl: true}, {Key: "\\", Ctrl: true}, {Key: "z", Ctrl: true}} {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "Escape"}, "", "")
		s.term.PersonInput(&k, "", "")
		s.ended("\r\n", "")
		allowed(t, s, "run after a key left for the shell, then "+k.Key)
	}
	// An answer typed with its line end: the command read the line.
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(&PersonKey{Key: "Escape"}, "", "")
	s.term.PersonInput(nil, "y\r", "")
	s.ended("y\r\n", "")
	allowed(t, s, "run after a line typed to the command")
	// The model's Ctrl+C to the command drops theirs too.
	s = playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(&PersonKey{Key: "Escape"}, "", "")
	if err := s.term.Keys("ctrl+c", s.term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	s.ended("^C\r\n", "")
	allowed(t, s, "run after the model's Ctrl+C to the command")
}

// Keys typed while a full-screen program has the screen are its own.
func TestKeysOnTheAlternateScreenDoNotCount(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.prints("\x1b[?1049h")
	s.term.PersonInput(&PersonKey{Key: "Escape"}, "", "")
	s.term.PersonInput(nil, ":q", "")
	s.prints("\x1b[?1049l")
	s.ended("", "")
	allowed(t, s, "run after keys to a full-screen program")
}

// Text typed into a full-screen program is read by it: the line at the
// prompt after it is empty, at a prompt that is not the last one's too (a
// status line that changed when the editor saved a file). A paste with
// line ends into it is the line's until a line end: the one that ends the
// editor's command takes it.
func TestTextIntoAFullScreenProgramIsItsOwn(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.prints("\x1b[?1049h")
	s.term.PersonInput(nil, "ihello", "")
	s.term.PersonInput(&PersonKey{Key: "Escape"}, "", "")
	s.term.PersonInput(nil, "ZZ", "")
	s.prints("\x1b[?1049l")
	s.prints(s.promptText(0)[:len(s.promptText(0))-2] + "~/x * $ ")
	allowed(t, s, "run after text into a full-screen program")

	s = playedFor(t, true)
	s.commandRuns()
	s.prints("\x1b[?1049h\x1b[?2004h")
	s.term.PersonInput(nil, "", "func main() {\r\treturn\r}\r")
	s.term.PersonInput(&PersonKey{Key: "Escape"}, "", "")
	s.term.PersonInput(nil, ":wq\r", "")
	s.prints("\x1b[?2004l\x1b[?1049l")
	s.prints(s.promptText(0)[:len(s.promptText(0))-2] + "~/x * $ ")
	allowed(t, s, "run after a paste into a full-screen program and the line that ended it")
}

// What the person types at the new prompt before it is drawn makes the
// line theirs as at any prompt: the screen is not read for what they typed
// while the command ran, which would take what they typed since for the
// prompt.
func TestTypingAtTheNewPromptKeepsTheLineTheirs(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(nil, "x", "")
	s.term.output([]byte(s.promptText(0) + "x"))
	// Ending as the prompt does: the row alone would read as a prompt.
	s.term.PersonInput(nil, "$", "")
	s.prints("$")
	start := time.Now()
	refusedForThem(t, s, "run onto a line typed on before and after the prompt")
	// Not after the three seconds a run gives a prompt to be drawn: the
	// line was theirs whatever the prompt.
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("the run waited %s to be refused", took)
	}
	s.erase(2)
	refusedForThem(t, s, "run with it erased: nothing said where the prompt ends")
	s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	s.prints("^C\r\n" + s.promptText(130))
	allowed(t, s, "run after the prompt their Ctrl+C brought")
}

// A paste whose line end sent a line leaves the rest on the next line, as
// typed at that prompt, and what is typed while the line runs goes after
// it: the screen is not read for the second, which would take the first
// for the prompt.
func TestTextAfterAPastedLineEndAndWhileItRunsIsTheirs(t *testing.T) {
	s := playedFor(t, true)
	s.term.PersonInput(nil, "ls\rpwd", "")
	s.prints("ls\r\n")
	s.term.PersonInput(nil, "x", "")
	s.ended("", "pwdx")
	refusedForThem(t, s, "run onto what came after the line end and while it ran")
	s.erase(1)
	refusedForThem(t, s, "run with what was typed while it ran erased")
}

// A Ctrl+C at the new prompt before it is read: in a shell that reads the
// key as a character, no prompt follows it, and what the screen shows
// after the wait for one is not the command's leaving.
func TestACtrlCBeforeThePromptIsReadKeepsTheLineTheirs(t *testing.T) {
	setWait(t, &interruptWait, 2*promptSettle)
	s := playedFor(t, false)
	s.commandRuns()
	s.term.PersonInput(nil, "ls", "")
	s.term.output([]byte(s.promptText(0) + "ls"))
	s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	s.term.output([]byte("^C"))
	// Longer than the wait for the prompt the key would bring. Longer
	// does no harm.
	time.Sleep(interruptWait + 50*time.Millisecond)
	s.quiet()
	refusedForThem(t, s, "run onto a line with a Ctrl+C the shell read as a character")
}

// The model typing after the person while a command runs: the end of
// what waits is the model's, and what of the person's is left the screen
// cannot say. The model's typing ahead keeps the line from being read at
// all. Its answer to what looks like a question does not, and the line is
// read, as the person's until their Enter or Ctrl+C.
func TestTheModelTypingAfterThePersonKeepsTheLineTheirs(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(nil, "ls", "")
	if err := s.term.Type("$", false, s.term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	// Ending as the prompt does: the row alone would read as a prompt.
	s.ended("ls$", "ls$")
	refusedForThem(t, s, "run onto what both typed while the command ran")
	if err := s.term.Keys("ctrl+c", s.term.PersonGen()); !errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("the model's ctrl+c on a line with the person's text: %v", err)
	}
	s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	s.prints("^C\r\n" + s.promptText(130))
	allowed(t, s, "run after the prompt the person's Ctrl+C brought")

	for _, model := range []func(s *playedShell) error{
		func(s *playedShell) error { return s.term.Type("x", false, s.term.PersonGen()) },
		func(s *playedShell) error { return s.term.Keys("x", s.term.PersonGen()) },
	} {
		s := playedFor(t, true)
		s.commandRuns()
		s.prints("Continue? ")
		s.term.PersonInput(nil, "ls", "")
		if err := model(s); err != nil {
			t.Fatal(err)
		}
		s.ended("lsx\r\n", "lsx")
		refusedForThem(t, s, "run onto what both typed, the model's to a question")
		// They send it, and it runs. What they type then is a new line's.
		s.term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
		s.prints("\r\n")
		s.term.PersonInput(nil, "q", "")
		s.ended("", "")
		allowed(t, s, "run after the next command read what they typed")
	}
}

// A run made as the prompt comes waits for its line to be read, as it
// waits for any prompt to be drawn: it was refused at once, for text the
// command had read.
func TestARunWaitsForTheLineToBeRead(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(nil, "q", "")
	// The prompt, and no quiet yet: the terminal's own timer reads it.
	s.term.output([]byte(s.promptText(0)))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.term.Run(ctx, "echo ok", time.Millisecond, s.term.PersonGen()); err != nil {
		t.Fatalf("run made as the prompt came: %v", err)
	}
}

// runeCells is the cells each character of s takes: two for the
// ideographic space, one for the others here.
func runeCells(s string) int {
	if strings.ContainsRune(s, '\u3000') {
		return 2
	}
	return 1
}
