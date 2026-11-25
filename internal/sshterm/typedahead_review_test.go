package sshterm

// Cases a review of the change that reads the line for text typed while a
// command ran found, each reproduced before it was fixed.

import (
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

// From the second review.

// A line that wraps, whose last row reads as the last prompt did: the row
// above it is the person's text too.
func TestAWrappedLineWhoseLastRowIsThePromptIsTheirs(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	typed := "rm -rf x;" + strings.Repeat("a", DefaultCols-2-len("rm -rf x;")) + "$ "
	s.term.PersonInput(nil, typed, "")
	s.ended("", typed)
	refusedForThem(t, s, "run onto a wrapped line whose second row reads as the prompt")

	// The command made the prompt empty, and what was typed is "$ ".
	s = playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(nil, "$ ", "")
	s.prints("\r\n\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07$ ")
	refusedForThem(t, s, "run onto text that reads as the last prompt at an empty one")
}

// Text and pastes typed while a full-screen program had the screen can be
// left for the shell (typed after the program's last read, before it gave
// the screen back): a Tab in them (pasted) and a paste with line ends are
// judged as anywhere. (The Tab key, as other keys there, is the program's.)
func TestWhatTheShellReadsAfterAFullScreenProgramIsJudgedAsAnywhere(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.prints("\x1b[?1049h")
	s.term.PersonInput(nil, "qcd /tm\t", "")
	s.prints("\x1b[?1049l" + s.promptText(0) + "cd /tmp/")
	refusedForThem(t, s, "run onto a line the shell completed after a full-screen program")

	for _, drawn := range []string{"rm -rf x\r\n> ", "rm -rf x\r\n$ "} {
		s := playedFor(t, true)
		s.commandRuns()
		s.prints("\x1b[?1049h\x1b[?2004h")
		s.term.PersonInput(nil, "", strings.ReplaceAll(strings.TrimSuffix(drawn, "> "), "\r\n", "\r"))
		s.prints("\x1b[?2004l\x1b[?1049l" + s.promptText(0) + drawn)
		refusedForThem(t, s, "run onto a paste over two lines after a full-screen program")
	}
}

// The last column: the cursor stays on the last character there. Text in
// it, one character or more, is the person's until erased.
func TestTheLastColumnIsReadAndErased(t *testing.T) {
	s := playedFor(t, true)
	prompt := strings.Repeat("p", DefaultCols-3) + "$ "
	s.prints("\r\n\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07" + prompt)
	s.commandRuns()
	s.term.PersonInput(nil, "x", "")
	s.prints("\r\n\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07" + prompt + "x")
	refusedForThem(t, s, "run onto one character in the last column")

	s = playedFor(t, true)
	s.commandRuns()
	typed := strings.Repeat(" ", DefaultCols-3) + "x"
	s.term.PersonInput(nil, typed, "")
	s.ended("", typed)
	s.term.PersonInput(&PersonKey{Key: "a", Ctrl: true}, "", "")
	s.prints("\x1b[3G")
	refusedForThem(t, s, "run onto text to the last column, the cursor moved to its start")

	s = playedFor(t, true)
	s.commandRuns()
	typed = "rm -rf " + strings.Repeat("a", DefaultCols-2-len("rm -rf ")-1) + "b"
	s.term.PersonInput(nil, typed, "")
	s.ended("", typed)
	refusedForThem(t, s, "run onto text to the last column")
	s.term.PersonInput(&PersonKey{Key: "u", Ctrl: true}, "", "")
	s.prints("\r$ \x1b[K")
	allowed(t, s, "run once the person cleared it")
}

// ASCII spaces the command read, at a prompt that is not the last one's:
// the spaces matched in the prompt are none.
func TestSpacesTheCommandReadAtAChangedPromptAreNone(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(nil, "   ", "")
	s.prints(s.promptText(0)[:len(s.promptText(0))-2] + "~/x * $ ")
	allowed(t, s, "run after spaces the command read")
}

// What was typed after a line sent ahead waits for the prompt after the
// command the shell starts with that line: it is not dropped there.
func TestTextAfterALineSentAheadWaitsForTheNextPrompt(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(nil, "pwd\rx", "")
	nonce := s.term.marks.nonce
	s.term.output([]byte("\r\n\x1b]133;D;0;lc=" + nonce + "\x07\x1b]133;C;lc=" + nonce + "\x07"))
	s.quiet()
	s.prints("pwd-output\r\n" + s.promptText(0) + "x")
	refusedForThem(t, s, "run onto text typed after a line sent ahead")
}

// The same in a shell that says nothing as a command starts (bash 3.2,
// no PS0): the mark of the command before, then the one after the line
// sent ahead.
func TestTextAfterALineSentAheadWaitsWithoutAStartMark(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(nil, "pwd\rx", "")
	s.term.output([]byte("\r\n" + s.promptText(0)))
	s.prints("pwd\r\npwd-output\r\n" + s.promptText(0) + "x")
	refusedForThem(t, s, "run onto text typed after a line sent ahead, no start mark")
}

// From the third review.

// Text ending one column short of the last: the cursor is in the last
// column over a blank cell, which is not the person's text.
func TestTextEndingBeforeTheLastColumnIsTheirs(t *testing.T) {
	t.Run("ordinary prompt, text ends at col Width()-2", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		// "$ " is 2 cols; DefaultCols-3 chars occupy cols 2..DefaultCols-2,
		// leaving cursor at DefaultCols-1 with cell DefaultCols-1 empty.
		typed := "rm -rf x #" + strings.Repeat("a", DefaultCols-3-len("rm -rf x #"))
		s.term.PersonInput(nil, typed, "")
		s.ended("", typed)
		refusedForThem(t, s, "run onto typed-ahead text ending one column before the right margin")
	})

	t.Run("ordinary prompt, text ending with a space ends at col Width()-2", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		typed := "rm -rf x #" + strings.Repeat("a", DefaultCols-4-len("rm -rf x #")) + " "
		s.term.PersonInput(nil, typed, "")
		s.ended("", typed)
		refusedForThem(t, s, "run onto typed-ahead text with trailing space ending one column before the right margin")
	})

	t.Run("wrapped text ends at col Width()-2 on the second row", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		typed := "rm -rf x #" + strings.Repeat("a", 2*DefaultCols-3-len("rm -rf x #"))
		s.term.PersonInput(nil, typed, "")
		s.ended("", typed)
		refusedForThem(t, s, "run onto wrapped typed-ahead text ending one column before the right margin")
	})

	t.Run("long prompt, one character ends at col Width()-2", func(t *testing.T) {
		s := playedFor(t, true)
		prompt := strings.Repeat("p", DefaultCols-4) + "$ "
		s.prints("\r\n\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07" + prompt)
		s.commandRuns()
		s.term.PersonInput(nil, "x", "")
		s.prints("\r\n\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07" + prompt + "x")
		refusedForThem(t, s, "run onto one character ending one column before the right margin")
	})
}

// A recalled line whose last row reads as the last prompt: it ends further
// below its mark than the last prompt did, however it went to that row.
func TestARecalledLineEndingLikeThePromptIsTheirs(t *testing.T) {
	t.Run("space in the last column of the row above", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "ArrowUp"}, "", "")
		// "$ " (2) + "rm -rf x #" (10) + 87 'a's + " " at col 99 = 100 cols;
		// "$ " wraps onto row 1.
		recalled := "rm -rf x #" + strings.Repeat("a", DefaultCols-2-len("rm -rf x #")-1) + " $ "
		s.ended("", recalled)
		refusedForThem(t, s, "run onto a recalled line with a space in the last column before wrapping")
	})

	t.Run("wide character wrapped from the last column of the row above", func(t *testing.T) {
		s := playedFor(t, true)
		s.prints("\r\n\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07한$ ")
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "ArrowUp"}, "", "")
		// "한$ " (4) + 95 narrow chars reach col 98; "한" (width 2) does not
		// fit at col 99 and wraps to the next row, leaving col 99 blank.
		recalled := "rm -rf x #" + strings.Repeat("a", DefaultCols-4-len("rm -rf x #")-1) + "한$ "
		s.prints("\r\n\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07한$ " + recalled)
		refusedForThem(t, s, "run onto a recalled line whose wide character wrapped from the last column")
	})

	t.Run("multi-line recalled command with an explicit newline", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "ArrowUp"}, "", "")
		s.ended("", "rm -rf x; echo '\r\n$ ")
		refusedForThem(t, s, "run onto a multi-line recalled command whose last row reads as the prompt")
	})
}

// Output that reaches the last column above the prompt is not a line the
// prompt goes on from.
func TestKeysReadAfterOutputToTheLastColumnLeaveTheLineEmpty(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(&PersonKey{Key: "ArrowDown"}, "", "")
	s.term.PersonInput(nil, "q", "")
	s.ended(strings.Repeat("-", DefaultCols)+"\r\n", "")
	allowed(t, s, "run after keys the command read, with full-width command output above the prompt")
}

// A paste sent between bracketed paste's marks, left for a shell that has
// not turned it on: it reads the marks as keys.
func TestABracketedPasteLeftForAShellWithoutItIsTheirs(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.prints("\x1b[?2004h")
	s.term.PersonInput(nil, "", "rm -rf x")
	// Program exits before reading the paste; bash 3.2 receives
	// \x1b[200~rm -rf x\x1b[201~ and draws "0~rm -rf x1~".
	s.prints("\x1b[?2004l" + s.promptText(0) + "0~rm -rf x1~")
	refusedForThem(t, s, "run onto a single-line bracketed paste left for a shell without bracketed paste")
}

// The last column, the two ways a cursor sits on the person's last
// character there: a wrap pending after it (a space too, which looks like
// a blank cell), or a pseudo console placing the cursor on it itself.
func TestTheLastCharacterUnderTheCursorIsRead(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	typed := "rm -rf x" + strings.Repeat("a", DefaultCols-2-len("rm -rf x")-1) + " "
	s.term.PersonInput(nil, typed, "")
	s.ended("", typed)
	refusedForThem(t, s, "run onto text ending with a space in the last column")

	s = playedFor(t, true)
	s.commandRuns()
	typed = "rm -rf x" + strings.Repeat("a", DefaultCols-2-len("rm -rf x")-1) + "b"
	s.term.PersonInput(nil, typed, "")
	row := s.term.Snapshot().CursorY + 1
	s.ended("", typed+"\x1b["+itoa(row)+";"+itoa(DefaultCols)+"H")
	refusedForThem(t, s, "run onto text with the cursor placed on its last character")
}
