package sshterm

// Cases a review of the change that reads the line for text typed while a
// command ran found, each reproduced before it was fixed.

import (
	"errors"
	"strings"
	"testing"
)

// Alt+Enter, Alt+Ctrl+J, Alt+Ctrl+M and Alt+Ctrl+O while a command
// runs do not execute the line in bash, zsh or PowerShell, but aheadKey calls
// dropAhead() on all four.
func TestALineEndWithAltLeavesWhatWasTyped(t *testing.T) {
	keys := []struct {
		name  string
		key   PersonKey
		drawn string
	}{
		{"Alt+Enter in bash", PersonKey{Key: "Enter", Alt: true}, "rm -rf x"},
		{"Alt+Enter in zsh (newline inserted)", PersonKey{Key: "Enter", Alt: true}, "rm -rf x\r\n"},
		{"Alt+Ctrl+M", PersonKey{Key: "m", Ctrl: true, Alt: true}, "rm -rf x"},
		{"Alt+Ctrl+J", PersonKey{Key: "j", Ctrl: true, Alt: true}, "rm -rf x"},
		{"Alt+Ctrl+O", PersonKey{Key: "o", Ctrl: true, Alt: true}, "rm -rf x"},
	}
	for _, tc := range keys {
		t.Run(tc.name, func(t *testing.T) {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(nil, "rm -rf x", "")
			s.term.PersonInput(&tc.key, "", "")
			s.ended("rm -rf x", tc.drawn)
			refusedForThem(t, s, "run onto text left on the line after "+tc.name)
		})
	}
}

// applySize clears promptKnown when a smaller terminal cuts the
// line, but leaves aheadJudge set, so settled() later runs judgeAhead() on the
// truncated or clamped screen and records it as an empty prompt.
func TestASmallerScreenBeforeTheLineIsReadKeepsItTheirs(t *testing.T) {
	t.Run("narrower screen cuts off typed-ahead tail before settle", func(t *testing.T) {
		s := playedFor(t, true)
		prompt := strings.Repeat("p", MinCols-4) + "$ "
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf x", "")
		// Mark + prompt + typed-ahead text arrive; settle timer has not run yet.
		s.term.output([]byte("\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07" + prompt + "rm -rf x"))
		s.term.ViewerSize("v", MinCols, DefaultRows)
		s.quiet()
		refusedForThem(t, s, "run onto typed-ahead text cut off by a narrower screen before settle")
	})

	t.Run("shorter screen cuts off cursor row before settle", func(t *testing.T) {
		s := playedFor(t, true)
		for i := 0; i < MinRows+2; i++ {
			s.prints("line\r\n")
		}
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf x", "")
		// Cursor lands below MinRows, with typed-ahead text; settle has not run yet.
		s.term.output([]byte("\r\n" + s.promptText(0) + "rm -rf x"))
		s.term.ViewerSize("v", DefaultCols, MinRows)
		s.quiet()
		refusedForThem(t, s, "run onto typed-ahead text cut off by a shorter screen before settle")
	})
}

// When prompt + typed-ahead text fills the row width exactly, vt
// leaves the cursor at col cols-1 (atPhantom=true) on top of the last
// character. aheadShownFrom scans only cols < pos.X, skips the final cell,
// fails to match aheadText, and records the entire typed line as the prompt.
func TestTextFillingTheRowIsReadToTheLastColumn(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	// "$ " is 2 cols; DefaultCols-2 chars fill cols 2..DefaultCols-1 exactly.
	typed := "rm -rf " + strings.Repeat("a", DefaultCols-2-len("rm -rf ")-1) + "b"
	s.term.PersonInput(nil, typed, "")
	s.ended("", typed)
	refusedForThem(t, s, "run onto typed-ahead text that ends at the right margin")
}

// When a wrapped typed-ahead line or a multi-line bracketed paste
// ends with spaces on the cursor's row, aheadShownFrom returns pos.X > 0
// (ignoring spaces because word stays false), recording a non-zero prompt on
// that row while unsent text sits on the row above.
func TestTextReadToTheLeftEdgeGoesOnAbove(t *testing.T) {
	t.Run("wrapped line whose second row is a space", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		// 98 chars fill row 0 after "$ ", and the trailing space wraps to row 1 col 0.
		long := "rm -rf x #" + strings.Repeat("a", DefaultCols-2-len("rm -rf x #")) + " "
		s.term.PersonInput(nil, long, "")
		s.ended("", long)
		refusedForThem(t, s, "run onto wrapped typed-ahead text ending with a space on the next row")
	})

	t.Run("bracketed paste whose second line is spaces", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.prints("\x1b[?2004h")
		s.term.PersonInput(nil, "", "rm -rf x\r  ")
		s.ended("", "rm -rf x\r\n  ")
		refusedForThem(t, s, "run onto a bracketed paste ending with spaces on its last row")
	})
}

// A multi-line bracketed paste while a command runs stays
// aheadTextOnly (or aheadShown with Tab / a shownAhead key) instead of
// aheadUnknown, letting judgeAhead read row 2 as an empty prompt.
func TestABracketedPasteOverSeveralLinesIsTheirs(t *testing.T) {
	t.Run("bracketed paste with Tab ending in prompt text", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.prints("\x1b[?2004h")
		s.term.PersonInput(nil, "", "rm -rf\tx\r$ ")
		s.ended("", "rm -rf\tx\r\n$ ")
		refusedForThem(t, s, "run onto a bracketed paste with Tab whose last row matches the prompt")
	})

	t.Run("bracketed paste ending in prompt text followed by End", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.prints("\x1b[?2004h")
		s.term.PersonInput(nil, "", "rm -rf x\r$ ")
		s.term.PersonInput(&PersonKey{Key: "End"}, "", "")
		s.ended("", "rm -rf x\r\n$ ")
		refusedForThem(t, s, "run onto a bracketed paste whose last row matches the prompt after End")
	})

	t.Run("bracketed paste with continuation prompt as in PowerShell", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.prints("\x1b[?2004h")
		s.term.PersonInput(nil, "", "rm -rf x\ry")
		s.ended("", "rm -rf x\r\n>> y")
		refusedForThem(t, s, "run onto a bracketed paste with a continuation prompt")
		// Erasing only the second row's character leaves row 1 on the line.
		s.erase(1)
		refusedForThem(t, s, "run onto a bracketed paste after erasing only the second row")
	})

	t.Run("bracketed paste followed by Ctrl+U", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.prints("\x1b[?2004h")
		s.term.PersonInput(nil, "", "rm -rf x\rfoo")
		s.term.PersonInput(&PersonKey{Key: "u", Ctrl: true}, "", "")
		// Canonical mode / bash unix-line-discard kills only the current line,
		// leaving the earlier line in the buffer.
		s.ended("", "rm -rf x")
		refusedForThem(t, s, "run onto the first line of a bracketed paste left after Ctrl+U")
	})
}

// aheadKey clears aheadText on Ctrl+U even on a local PowerShell
// terminal, where ConPTY / PSReadLine (default Windows mode) does not clear
// the line on Ctrl+U.
func TestCtrlUInPowerShellLeavesTheLine(t *testing.T) {
	old := personYield
	personYield = 0
	t.Cleanup(func() { personYield = old })
	m := NewManager(Options{Local: &LocalShell{Path: "powershell.exe"}})
	s := newPlayedShell(m, true)
	t.Cleanup(func() { s.term.end("done") })

	s.commandRuns()
	s.term.PersonInput(nil, "rm -rf x", "")
	s.term.PersonInput(&PersonKey{Key: "u", Ctrl: true}, "", "")
	// PowerShell does not drop "rm -rf x" on Ctrl+U and draws it at the prompt.
	s.ended("", "rm -rf x")
	refusedForThem(t, s, "run onto typed-ahead text left after Ctrl+U in PowerShell")
}

// aheadShownFrom uses strings.TrimSpace, treating Unicode spaces
// (U+00A0 NBSP, U+3000 ideographic space) as ignorable shell whitespace even
// though bash, zsh and PowerShell treat them as part of the command word.
func TestSpacesThatAreNoASCIISpaceAreText(t *testing.T) {
	for _, tc := range []struct {
		name, sp string
	}{
		{"NBSP U+00A0", "\u00a0"},
		{"ideographic space U+3000", "\u3000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(nil, tc.sp, "")
			s.ended("", tc.sp)
			refusedForThem(t, s, "run onto "+tc.name+" typed while a command ran")
		})
	}
}

// recordPrompt(x) slices promptRest from pos.X instead of x. With a
// right prompt (e.g. zsh RPROMPT), erasing typed-ahead text leaves more spaces
// between cursor and RPROMPT than recorded in promptRest, so lineEmpty() stays
// false forever until Enter or Ctrl+C.
func TestARightPromptStaysWhereItIsWhenTheTextIsErased(t *testing.T) {
	s := playedFor(t, true)
	rprompt := "[main]"
	pad := DefaultCols - 2 - len(rprompt)
	// Prompt with RPROMPT at the right edge, then "ls" typed ahead.
	s.commandRuns()
	s.term.PersonInput(nil, "ls", "")
	// Shell draws "$ ls", spaces up to RPROMPT, RPROMPT, and moves cursor back after "ls".
	s.prints(s.promptText(0) + "ls" + strings.Repeat(" ", pad-2) + rprompt + "\x1b[5G")
	refusedForThem(t, s, "run onto typed-ahead text with a right prompt")
	// Person erases "ls": cursor moves to col 2, cols 2..3 become spaces, RPROMPT stays.
	s.erase(2)
	allowed(t, s, "run once typed-ahead text is erased with a right prompt")
}

// When a command reads text whose end matches the end of the prompt,
// aheadShownFrom walks into the prompt itself and records promptX too far left,
// refusing runs on the empty line until Enter or Ctrl+C.
func TestAPromptEndingAsTheTextTheCommandReadIsEmpty(t *testing.T) {
	s := playedFor(t, true)
	s.prints("\r\n\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07me@test$ ")
	s.commandRuns()
	// Person typed "$ " into a command that read it; nothing waits for the shell.
	s.term.PersonInput(nil, "$ ", "")
	s.prints("\r\n\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07me@test$ ")
	if err := s.run(); err != nil && errors.Is(err, ErrLineNotEmpty) {
		t.Fatalf("empty line refused because prompt ended with read text: %v", err)
	}
}

// A command the shell starts on its own (zsh's sched, a line it read
// itself) starts with nothing left over of what was typed for the last
// prompt: read at that prompt, or never read there because the model
// typed too.
func TestACommandTheShellStartsLeavesNothingOfTheLastReading(t *testing.T) {
	t.Run("read at the last prompt", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "Escape"}, "", "")
		s.ended("", "")
		refusedForThem(t, s, "run after Escape left for the shell")
		s.prints("\r\n\x1b]133;C;lc=" + s.term.marks.nonce + "\x07")
		s.term.PersonInput(nil, "q", "")
		s.ended("", "")
		allowed(t, s, "run after a key the next command read")
	})
	t.Run("not read, the person's Enter coming first", func(t *testing.T) {
		// Enter at the new prompt before it is read: the line goes, and
		// what is typed while it runs is the next one's.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "Escape"}, "", "")
		s.term.output([]byte(s.promptText(0)))
		s.commandRuns()
		s.term.PersonInput(nil, "q", "")
		s.ended("", "")
		allowed(t, s, "run after a key the next command read")
	})
	t.Run("not read, the model having typed too", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "Escape"}, "", "")
		if err := s.term.Type("x", false, s.term.PersonGen()); err != nil {
			t.Fatal(err)
		}
		s.ended("x", "x")
		refusedForThem(t, s, "run after both typed while the command ran")
		s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
		s.prints("^C\r\n" + s.promptText(130))
		s.prints("\x1b]133;C;lc=" + s.term.marks.nonce + "\x07")
		s.term.PersonInput(nil, "q", "")
		s.ended("\r\n", "")
		allowed(t, s, "run after a key the next command read")
	})
}

// A prompt that ends in the last column leaves the cursor on its last
// character. With nothing of what was typed after it, the line is empty.
func TestAPromptToTheLastColumnWithNothingTypedOnIsEmpty(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(nil, "q", "")
	s.prints(s.promptText(0)[:len(s.promptText(0))-2] + strings.Repeat("p", DefaultCols-2) + "$ ")
	allowed(t, s, "run at a prompt to the last column, after a key the command read")
}
