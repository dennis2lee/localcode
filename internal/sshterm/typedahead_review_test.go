package sshterm

// Cases a review of the change that reads the line for text typed while a
// command ran found, each reproduced before it was fixed.

import (
	"strings"
	"testing"
	"time"
)

// Alt+Enter, Alt+Ctrl+J, Alt+Ctrl+M and Alt+Ctrl+O while a command runs
// send no line in bash, zsh or PowerShell: what was typed is still there.
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

// A smaller terminal that cuts the line before it is read: what was cut
// is not on the screen to be read.
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

// Text that fills the row exactly leaves the cursor on its last character
// in the last column.
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

// A bracketed paste over several lines while a command runs: its last row
// can read as the prompt, and the rows above it are on the line.
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

// Ctrl+U in a local PowerShell terminal: PSReadLine leaves the line as it
// is.
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

// Enter at the new prompt before it is read: the line goes, and what is
// typed while it runs is the next one's.
func TestEnterBeforeTheLineIsReadLeavesNothingOfIt(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(&PersonKey{Key: "Escape"}, "", "")
	s.term.output([]byte(s.promptText(0)))
	s.commandRuns()
	s.term.PersonInput(nil, "q", "")
	s.ended("", "")
	allowed(t, s, "run after a key the next command read")
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

// The last column: the cursor sits there on the last character written or
// over a blank cell, and the screen does not say which. Text that reaches
// it is the person's until their Enter or Ctrl+C.
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
	refusedForThem(t, s, "run with it cleared: the last column is not read")
	s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	s.prints("^C\r\n" + s.promptText(130))
	allowed(t, s, "run after the prompt their Ctrl+C brought")
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

// From the fourth review.

// The model typing while a command the shell started from a line sent
// ahead runs: what the person typed after that line is still theirs.
func TestModelTypingDuringSentAheadCommandKeepsPersonsText(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model func(s *playedShell) error
	}{
		{"Type", func(s *playedShell) error { return s.term.Type("y", false, s.term.PersonGen()) }},
		{"Keys", func(s *playedShell) error { return s.term.Keys("y", s.term.PersonGen()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(nil, "ask\rrm -rf x", "")
			nonce := s.term.marks.nonce
			// Command 1 ends; shell immediately starts "ask" (C mark), which
			// prints a question so typeaheadOnly() is false.
			s.prints("\r\n\x1b]133;D;0;lc=" + nonce + "\x07$ \x1b]133;C;lc=" + nonce + "\x07Continue? ")
			if err := tc.model(s); err != nil {
				t.Fatal(err)
			}
			// "ask" ends without reading stdin; shell draws both on the line.
			s.ended("rm -rf xy\r\n", "rm -rf xy")
			refusedForThem(t, s, "run onto person's text followed by model "+tc.name+" during sent-ahead command")
		})
	}
}

// A line sent ahead that runs longer than the wait for a prompt, in a
// shell with no start mark (bash 3.2): what was typed after it is theirs at
// the prompt after it.
func TestTextAfterSlowSentAheadCommandWithoutStartMark(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(nil, "sleep 1\rrm -rf x", "")
	// Command 1 finishes in bash 3.2 (no C mark); bash draws the prompt,
	// echoes "sleep 1\r\n", and runs sleep 1 for > 150ms (simulated by prints/quiet).
	s.prints("\r\n" + s.promptText(0) + "sleep 1\r\n")
	// sleep 1 finishes and bash draws the prompt with the unsent text after it.
	s.prints(s.promptText(0) + "rm -rf x")
	refusedForThem(t, s, "run onto text typed after a slow sent-ahead command without a C mark")
}

// Ctrl+C to a command the shell started from a line sent ahead: the
// terminal drops what waits, and the next prompt's line is empty. It is
// held all the same: the terminal cannot tell text that waited behind a
// line sent ahead from text after a line a command read, which the shell
// has on its line (the seventh review). Accepted.
func TestCtrlCDuringSentAheadCommandKeepsTheHold(t *testing.T) {
	s := playedFor(t, true)
	nonce := s.term.marks.nonce
	emptyRowPrompt := "\x1b]133;D;0;lc=" + nonce + "\x07me@test\r\n"
	s.prints("\r\n" + emptyRowPrompt)

	s.commandRuns()
	s.term.PersonInput(nil, "cat\rhello", "")
	// Command 1 ends and the shell starts "cat" (C mark).
	s.prints("\r\n" + emptyRowPrompt + "\x1b]133;C;lc=" + nonce + "\x07")
	// Person interrupts "cat", dropping what was typed ahead.
	s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	// "cat" exits to a fresh prompt with nothing on the command line.
	s.prints("^C\r\n" + emptyRowPrompt)
	refusedForThem(t, s, "run on an empty line after Ctrl+C during a sent-ahead command")
}

// A prompt that draws a right-hand part to the last column and puts the
// cursor back: what was typed is still read.
func TestSavedRestoredRightPromptAtLastColumnDoesNotHideTypedAheadText(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	typed := "rm -rf x"
	s.term.PersonInput(nil, typed, "")
	rprompt := "[main]"
	pad := DefaultCols - 2 - len(typed) - len(rprompt)
	// Shell draws "$ rm -rf x", saves cursor at col 10, draws right prompt
	// flush to col DefaultCols-1 (setting atPhantom=true), and restores cursor
	// to col 10 with ESC 8 (leaving atPhantom=true).
	s.ended("", typed+"\x1b7"+strings.Repeat(" ", pad)+rprompt+"\x1b8")
	refusedForThem(t, s, "run onto typed-ahead text when right prompt wrote last column between ESC 7 and ESC 8")
}

// A character a command left in the last column, and text ending one
// column before it: the text is not taken for the prompt.
func TestLeftoverCharInLastColumnDoesNotHideTextEndingBeforeIt(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	// "$ " (2) + 97 chars fill cols 0..98, leaving cursor at col 99
	// over the '=' the progress line left there.
	typed := "rm -rf x #" + strings.Repeat("a", DefaultCols-3-len("rm -rf x #"))
	s.term.PersonInput(nil, typed, "")
	s.ended(strings.Repeat("=", DefaultCols)+"\r", typed)
	refusedForThem(t, s, "run onto typed-ahead text ending at Width()-2 when col Width()-1 has leftover output")
}

// A pseudo console that places the cursor on the last column, over a
// space the person typed there.
func TestTrailingSpaceInLastColumnWithCursorPlacedIsRead(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	typed := "rm -rf x" + strings.Repeat("a", DefaultCols-2-len("rm -rf x")-1) + " "
	s.term.PersonInput(nil, typed, "")
	row := s.term.Snapshot().CursorY + 1
	s.ended("", typed+"\x1b["+itoa(row)+";"+itoa(DefaultCols)+"H")
	refusedForThem(t, s, "run onto text ending with a space in the last column with cursor placed by CUP")
}

// zsh moving to a new row before its prompt (PROMPT_SP) after output with
// no line end, then a recalled line or a paste over two rows ending in
// "$ ".
func TestZshPromptSPAfterPartialLineDoesNotHideRecalledOrPastedText(t *testing.T) {
	// zsh PROMPT_SP sequence: "%" + (cols-1) spaces + CR + EL.
	promptSP := "%" + strings.Repeat(" ", DefaultCols-1) + "\r\x1b[K"

	t.Run("wrapped recalled command after partial-line command", func(t *testing.T) {
		s := playedFor(t, true)
		nonce := s.term.marks.nonce
		// Command 1 outputs "hi" with no trailing newline; zsh PROMPT_SP wraps
		s.commandRuns()
		s.prints("hi\x1b]133;D;0;lc=" + nonce + "\x07" + promptSP + "$ ")

		// Command 2 ends with a normal newline; ArrowUp recalls a 2-row line
		// whose second row reads as "$ ".
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "ArrowUp"}, "", "")
		recalled := "rm -rf x #" + strings.Repeat("a", DefaultCols-2-len("rm -rf x #")) + "$ "
		s.prints("\x1b]133;D;0;lc=" + nonce + "\x07" + promptSP + "$ " + recalled)
		refusedForThem(t, s, "run onto a wrapped recalled command after a partial-line command in zsh")
	})

	t.Run("multi-line bracketed paste after partial-line command", func(t *testing.T) {
		s := playedFor(t, true)
		nonce := s.term.marks.nonce
		s.commandRuns()
		s.prints("hi\x1b]133;D;0;lc=" + nonce + "\x07" + promptSP + "$ ")

		s.commandRuns()
		s.prints("\x1b[?2004h")
		s.term.PersonInput(nil, "", "rm -rf x\r$ ")
		s.prints("\x1b]133;D;0;lc=" + nonce + "\x07" + promptSP + "$ rm -rf x\r\n$ ")
		refusedForThem(t, s, "run onto a multi-line bracketed paste after a partial-line command in zsh")
	})
}

// Ctrl+L left for the shell with a recalled line: the screen is cleared
// and the line drawn again from the top.
func TestCtrlLTypedAheadDoesNotHideAWrappedLine(t *testing.T) {
	t.Run("unsafe: ArrowUp + Ctrl+L with ED 2 and blank lines on screen", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		// Three blank lines put markY at row 4 with only row 0 non-empty.
		s.prints("\r\n\r\n\r\n")
		s.term.PersonInput(&PersonKey{Key: "ArrowUp"}, "", "")
		s.term.PersonInput(&PersonKey{Key: "l", Ctrl: true}, "", "")
		recalled := "rm -rf x #" + strings.Repeat("a", DefaultCols-2-len("rm -rf x #")) + "$ "
		// Shell draws "$ " + recalled (rows 4 and 5 non-empty: 3 non-empty rows
		// total), then Ctrl+L sends \x1b[H\x1b[2J (pushing 3 lines to scrollback)
		s.ended("", recalled+"\x1b[H\x1b[2J$ "+recalled)
		refusedForThem(t, s, "run onto a wrapped recalled line redrawn by Ctrl+L (ED 2)")
	})

	t.Run("unsafe: ArrowUp + Ctrl+L with ED 0", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "ArrowUp"}, "", "")
		s.term.PersonInput(&PersonKey{Key: "l", Ctrl: true}, "", "")
		recalled := "rm -rf x #" + strings.Repeat("a", DefaultCols-2-len("rm -rf x #")) + "$ "
		// \x1b[H\x1b[J does not push to scrollback; redrawn line ends at row 1,
		s.ended("", recalled+"\x1b[H\x1b[J$ "+recalled)
		refusedForThem(t, s, "run onto a wrapped recalled line redrawn by Ctrl+L (ED 0)")
	})

}

// A single-line bracketed paste while a command runs, for a shell with
// bracketed paste on: drawn as text after the prompt, erased, the line
// should read as empty again.
func TestBracketedPasteErasedIsEmpty(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.prints("\x1b[?2004h")
	s.term.PersonInput(nil, "", "ls")
	s.ended("", "ls")
	refusedForThem(t, s, "run onto bracketed paste text")
	s.erase(2)
	allowed(t, s, "run once bracketed paste text is erased")
}

// Text typed ahead, the command's output without a newline, a slow prompt:
// the mark is mid-row, and the line must not read as empty before the
// prompt arrives.
func TestSlowPromptMidRowWithTypeahead(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(nil, "x", "")
	s.term.output([]byte("hi\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07"))
	s.term.mu.Lock()
	s.term.markAt = time.Now().Add(-3 * time.Second)
	s.term.mu.Unlock()
	s.quiet()
	refusedForThem(t, s, "run before a slow prompt drew what waited, mark mid-row")
	s.prints("\r\n$ x")
	refusedForThem(t, s, "run onto text a slow prompt drew")
}

// The hold, and what clears it, across a mark: a command the shell starts
// on its own comes between the prompt the person typed for and the next.

// startsOnItsOwn is the shell starting a command the terminal did not
// start (zsh's sched, a line sent ahead): its start mark.
func (s *playedShell) startsOnItsOwn() {
	s.prints("\r\n\x1b]133;C;lc=" + s.term.marks.nonce + "\x07")
}

func TestTheHoldOutlastsACommandTheShellStarts(t *testing.T) {
	t.Run("text never read at its prompt", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "x", "")
		s.term.output([]byte(s.promptText(0) + "x"))
		s.startsOnItsOwn()
		s.ended("", "x")
		refusedForThem(t, s, "run onto text never read at its prompt")
	})
	t.Run("text the screen could not be read for", func(t *testing.T) {
		// The cursor in the last column.
		s := playedFor(t, true)
		s.commandRuns()
		typed := strings.Repeat("a", DefaultCols-3)
		s.term.PersonInput(nil, typed, "")
		s.ended("", typed)
		refusedForThem(t, s, "run onto text to the last column")
		s.startsOnItsOwn()
		s.ended("", typed)
		refusedForThem(t, s, "run onto it after a command the shell started")
	})
	t.Run("text a smaller terminal cut before it was read", func(t *testing.T) {
		s := playedFor(t, true)
		for i := 0; i < MinRows+2; i++ {
			s.prints("line\r\n")
		}
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf x", "")
		s.term.output([]byte("\r\n" + s.promptText(0) + "rm -rf x"))
		s.term.ViewerSize("v", DefaultCols, MinRows)
		s.quiet()
		s.startsOnItsOwn()
		s.ended("", "rm -rf x")
		refusedForThem(t, s, "run onto text a resize cut, after a command the shell started")
	})
	t.Run("a line end typed in text, and text after it", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "pwd\rx", "")
		s.prints("\r\n" + s.promptText(0) + "pwd")
		s.prints("\r\npwd-output\r\n" + s.promptText(0) + "x")
		refusedForThem(t, s, "run onto text typed after a line end in text")
	})
	t.Run("a line end sent with the Enter key, and text after it", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "pwd", "")
		s.term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
		s.term.PersonInput(nil, "x", "")
		// The prompt, with the line sent ahead drawn on it, read before the
		// shell runs that line.
		s.prints("\r\n" + s.promptText(0) + "pwd")
		s.prints("\r\npwd-output\r\n" + s.promptText(0) + "x")
		refusedForThem(t, s, "run onto text typed after a line sent with Enter")
	})
}

func TestWhatClearsTheHoldAcrossACommandTheShellStarts(t *testing.T) {
	t.Run("a signal after a line sent ahead, and text the command read", func(t *testing.T) {
		// A command that lives through the signal (a REPL) reads what is
		// typed after it: nothing of the line before the signal is left.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "1\r", "")
		s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
		s.term.PersonInput(nil, "q", "")
		s.ended("", "")
		allowed(t, s, "run after text the command read after a signal")
	})
	t.Run("a line sent ahead with nothing after it", func(t *testing.T) {
		// The line runs; what is typed while it runs is the next prompt's,
		// and read as at any.
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "pwd\r", "")
		s.term.output([]byte("\r\n" + s.promptText(0)))
		s.startsOnItsOwn()
		s.term.PersonInput(nil, "q", "")
		s.ended("pwd-output\r\n", "")
		allowed(t, s, "run after a key the next command read")
	})
	t.Run("the person's Enter before the text was read", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "x", "")
		s.term.output([]byte(s.promptText(0) + "x"))
		s.commandRuns()
		s.term.PersonInput(nil, "q", "")
		s.ended("", "")
		allowed(t, s, "run after the line was sent and the next command read a key")
	})
}

// A signal key while a command runs drops what waits in the terminal, and
// leaves the hold: the shell keeps text drawn at the prompt before a
// command it started, a key left for it, the line Ctrl+O brings back (zsh
// does after a Ctrl+C to the command, measured), and the terminal cannot
// tell text that waited behind a line sent ahead from text after a line a
// command read. On the alternate screen a full-screen program reads the
// key raw, and nothing is dropped. From the sixth and seventh reviews.
func TestASignalToACommandLeavesTheHold(t *testing.T) {
	signal := func(t *testing.T, s *playedShell, who, key string) {
		t.Helper()
		if who == "person" {
			s.term.PersonInput(&PersonKey{Key: key, Ctrl: true}, "", "")
		} else if err := s.term.Keys("ctrl+"+key, s.term.PersonGen()); err != nil {
			t.Fatal(err)
		}
	}
	for _, who := range []string{"person", "model"} {
		t.Run(who+"/text drawn at the prompt", func(t *testing.T) {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(nil, "x", "")
			s.term.output([]byte(s.promptText(0) + "x"))
			s.startsOnItsOwn()
			signal(t, s, who, "c")
			s.ended("^C\r\n", "x")
			refusedForThem(t, s, "run onto text the shell kept past the "+who+"'s Ctrl+C")
		})
		t.Run(who+"/a key left for the shell", func(t *testing.T) {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(&PersonKey{Key: "Escape"}, "", "")
			s.ended("", "")
			refusedForThem(t, s, "run after a key left for the shell")
			s.startsOnItsOwn()
			signal(t, s, who, "c")
			s.ended("^C\r\n", "")
			refusedForThem(t, s, "run after the "+who+"'s Ctrl+C to a command after a key left for the shell")
		})
		t.Run(who+"/text read on the line, then Ctrl+Z", func(t *testing.T) {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(nil, "rm -rf x", "")
			s.ended("", "rm -rf x")
			refusedForThem(t, s, "run onto typed-ahead text at the first prompt")
			s.startsOnItsOwn()
			signal(t, s, who, "z")
			s.ended("^Z\r\n", "rm -rf x")
			refusedForThem(t, s, "run onto text the shell kept past the "+who+"'s Ctrl+Z")
		})
		t.Run(who+"/the line Ctrl+O brings back", func(t *testing.T) {
			s := playedFor(t, true)
			s.term.PersonInput(nil, "sleep 10", "")
			s.term.PersonInput(&PersonKey{Key: "o", Ctrl: true}, "", "")
			s.prints("\r\n\x1b]133;C;lc=" + s.term.marks.nonce + "\x07")
			signal(t, s, who, "c")
			s.ended("^C\r\n", "echo next")
			refusedForThem(t, s, "run onto the line Ctrl+O brought back past the "+who+"'s Ctrl+C")
		})
		t.Run(who+"/text after a line end a command read", func(t *testing.T) {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(nil, "y\rrm -rf x", "")
			s.ended("y\r\nrm -rf x", "rm -rf x")
			s.startsOnItsOwn()
			signal(t, s, who, "c")
			s.ended("^C\r\n", "rm -rf x")
			refusedForThem(t, s, "run onto text on the shell's line past the "+who+"'s Ctrl+C")
		})
		t.Run(who+"/Ctrl+O, and text after a line end the command read", func(t *testing.T) {
			s := playedFor(t, true)
			s.term.PersonInput(nil, "read x", "")
			s.term.PersonInput(&PersonKey{Key: "o", Ctrl: true}, "", "")
			s.term.PersonInput(nil, "y\rfoo", "")
			s.ended("read x\r\ny\r\nfoo", "echo nextfoo")
			s.startsOnItsOwn()
			signal(t, s, who, "c")
			s.ended("^C\r\n", "echo nextfoo")
			refusedForThem(t, s, "run onto the line Ctrl+O brought back and text past the "+who+"'s Ctrl+C")
		})
		t.Run(who+"/a reading still to come, and a line end a command read", func(t *testing.T) {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(nil, "rm -rf x", "")
			s.term.output([]byte(s.promptText(0) + "rm -rf x"))
			s.startsOnItsOwn()
			s.term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
			s.ended("\r\n", "rm -rf x")
			s.startsOnItsOwn()
			signal(t, s, who, "c")
			s.ended("^C\r\n", "rm -rf x")
			refusedForThem(t, s, "run onto text still to be read past the "+who+"'s Ctrl+C")
		})
		t.Run(who+"/text after a line sent ahead", func(t *testing.T) {
			// What waited behind the line is dropped, and the line is held.
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(nil, "cat\rx", "")
			s.prints("\r\n" + s.promptText(0) + "cat")
			s.startsOnItsOwn()
			signal(t, s, who, "c")
			s.ended("^C\r\n", "")
			refusedForThem(t, s, "run after the "+who+"'s Ctrl+C to the line sent ahead")
		})
		t.Run(who+"/Ctrl+Z to a full-screen program, and text left for the shell", func(t *testing.T) {
			// The Tab still counts: no signal dropped it. The shell
			// completed the name.
			s := playedFor(t, true)
			s.commandRuns()
			s.prints("\x1b[?1049h")
			s.term.PersonInput(nil, "rm -rf x\t", "")
			signal(t, s, who, "z")
			s.prints("\x1b[?1049l")
			s.ended("", "rm -rf xyz")
			refusedForThem(t, s, "run onto text left after the "+who+"'s Ctrl+Z on the alternate screen")
		})
		t.Run(who+"/Ctrl+C to a full-screen program Ctrl+O ran", func(t *testing.T) {
			// The program reads the key, and no signal comes of it.
			s := playedFor(t, true)
			s.term.PersonInput(nil, "vim foo", "")
			s.term.PersonInput(&PersonKey{Key: "o", Ctrl: true}, "", "")
			s.prints("\r\n\x1b]133;C;lc=" + s.term.marks.nonce + "\x07\x1b[?1049h")
			signal(t, s, who, "c")
			s.term.PersonInput(nil, ":q\r", "")
			s.prints("\x1b[?1049l")
			s.ended("", "echo next")
			refusedForThem(t, s, "run onto the line Ctrl+O brought back after vim")
		})
	}
}

// From the fifth review.

// Enter inside a full-screen program (a search in less, a line in vim)
// is the program's: text typed after it, read by the program, leaves the
// line empty.
func TestEnterInAFullScreenProgramIsItsOwn(t *testing.T) {
	t.Run("less search with Enter then q", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.prints("\x1b[?1049h")
		s.term.PersonInput(nil, "/foo", "")
		s.term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
		s.term.PersonInput(nil, "q", "")
		s.prints("\x1b[?1049l")
		s.ended("", "")
		allowed(t, s, "run after searching in less and quitting with q")
	})

	t.Run("vim edit with Enter then Escape ZZ", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.prints("\x1b[?1049h")
		s.term.PersonInput(nil, "ihello", "")
		s.term.PersonInput(&PersonKey{Key: "Enter"}, "", "")
		s.term.PersonInput(nil, "world", "")
		s.term.PersonInput(&PersonKey{Key: "Escape"}, "", "")
		s.term.PersonInput(nil, "ZZ", "")
		s.prints("\x1b[?1049l")
		s.ended("", "")
		allowed(t, s, "run after editing in vim with Enter and quitting with ZZ")
	})
	// A paste with a line end, to a program that has not turned bracketed
	// paste on: its line ends are the program's too.
	s := playedFor(t, true)
	s.commandRuns()
	s.prints("\x1b[?1049h")
	s.term.PersonInput(nil, "", "a\rb")
	s.term.PersonInput(nil, "ZZ", "")
	s.prints("\x1b[?1049l")
	s.ended("", "")
	allowed(t, s, "run after a paste with a line end into a full-screen program")
}

// A prompt drawn on the row after the mark, with the screen scrolled by
// one, can leave the cursor where the mark left it on the screen: that is
// a prompt drawn, not none.
func TestAPromptAfterAScrollIsNotTheMark(t *testing.T) {
	s := playedFor(t, true)
	s.prints(strings.Repeat("line\r\n", DefaultRows) + s.promptText(0))
	s.commandRuns()
	s.term.PersonInput(nil, "q", "")
	// Command prints 2 chars (same width as "$ ") on the bottom row without a
	// newline before the D mark; the shell then prints "\r\n$ ", scrolling one
	// line so the prompt's cursor lands at (2, DefaultRows-1) = (markX, markY).
	s.term.output([]byte("ok\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07\r\n$ "))
	s.term.mu.Lock()
	s.term.markAt = time.Now().Add(-3 * time.Second)
	s.term.mu.Unlock()
	s.quiet()
	allowed(t, s, "run on an empty line after a scrolled prompt landed at the mark's column")
}

// Ctrl+D after text: the terminal passes the text on as it is, and
// Backspace or Ctrl+U after it erase nothing of it.
func TestCtrlDAfterTextPassesItOn(t *testing.T) {
	t.Run("Backspace after Ctrl+D", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf x", "")
		s.term.PersonInput(&PersonKey{Key: "d", Ctrl: true}, "", "")
		s.term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
		// Canonical TTY committed "rm -rf x" at VEOF; Backspace could not erase
		// it, so the shell reads and draws "rm -rf x".
		s.ended("", "rm -rf x")
		refusedForThem(t, s, "run onto typed-ahead text committed by Ctrl+D before Backspace")
	})

	t.Run("Ctrl+U after Ctrl+D", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf x", "")
		s.term.PersonInput(&PersonKey{Key: "d", Ctrl: true}, "", "")
		s.term.PersonInput(&PersonKey{Key: "u", Ctrl: true}, "", "")
		// Canonical TTY committed "rm -rf x" at VEOF; Ctrl+U only kills the
		// empty edit buffer after VEOF, so the shell draws "rm -rf x".
		s.ended("", "rm -rf x")
		refusedForThem(t, s, "run onto typed-ahead text committed by Ctrl+D before Ctrl+U")
	})
}

// Text read off the screen as on the line is the person's past a command
// the shell starts on its own, as text typed at the prompt is.
func TestTextReadOnTheLineOutlastsACommandTheShellStarts(t *testing.T) {
	t.Run("ordinary typed-ahead text after a shell-started command", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf x", "")
		s.ended("", "rm -rf x")
		refusedForThem(t, s, "run onto typed-ahead text at the first prompt")
		s.startsOnItsOwn()
		s.ended("", "rm -rf x")
		refusedForThem(t, s, "run onto typed-ahead text after a shell-started command")
	})

	t.Run("wrapped typed-ahead line reaching col 0 after a shell-started command", func(t *testing.T) {
		s := playedFor(t, true)
		long := strings.Repeat("a", DefaultCols)
		s.commandRuns()
		s.term.PersonInput(nil, long, "")
		s.ended("", long)
		refusedForThem(t, s, "run onto wrapped typed-ahead line at the first prompt")
		s.startsOnItsOwn()
		s.ended("", long)
		refusedForThem(t, s, "run onto wrapped typed-ahead line after a shell-started command")
	})

	t.Run("row-filling line wrapped by zsh space-CR-EL leaving cursor at col 0", func(t *testing.T) {
		s := playedFor(t, true)
		typed := "rm -rf x #" + strings.Repeat("a", DefaultCols-2-len("rm -rf x #"))
		s.commandRuns()
		s.term.PersonInput(nil, typed, "")
		s.ended("", typed+" \r\x1b[K")
		refusedForThem(t, s, "run onto row-filling typed-ahead line at the first prompt")
		s.startsOnItsOwn()
		s.ended("", typed)
		refusedForThem(t, s, "run onto row-filling typed-ahead line after a shell-started command")
	})

	t.Run("typed-ahead text cut off by a smaller screen after settle, then a shell-started command", func(t *testing.T) {
		s := playedFor(t, true)
		for i := 0; i < MinRows+2; i++ {
			s.prints("line\r\n")
		}
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf x", "")
		s.ended("", "rm -rf x")
		s.term.ViewerSize("v", DefaultCols, MinRows)
		s.startsOnItsOwn()
		s.ended("", "rm -rf x")
		refusedForThem(t, s, "run onto typed-ahead text cut off after settle and followed by a shell-started command")
	})
}

// Backspace after a bracketed paste: the terminal erases the paste's
// closing mark, not the text.
func TestBackspaceAfterABracketedPasteErasesNoText(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.prints("\x1b[?2004h")
	s.term.PersonInput(nil, "", "rm -rf x")
	s.term.PersonInput(&PersonKey{Key: "Backspace"}, "", "")
	// Shell with bracketed paste on still has the pasted text on the line
	// because canonical Backspace removed '~' from "\x1b[201~", not 'x'.
	s.ended("", "rm -rf x")
	refusedForThem(t, s, "run onto bracketed-pasted text after Backspace while running")
}

// What was typed for an earlier command is nothing to a later one the
// shell starts on its own. No run comes between them: a run's line drops
// it, and the sixth review found this test passing for that alone.
func TestAnEarlierCommandsTypingIsNothingToALaterOne(t *testing.T) {
	t.Run("a key, then Ctrl+C at the prompt", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(&PersonKey{Key: "ArrowDown"}, "", "")
		s.ended("", "")
		refusedForThem(t, s, "run after ArrowDown while command ran")
		s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
		s.prints("^C\r\n" + s.promptText(130))
		s.startsOnItsOwn()
		s.term.PersonInput(nil, "q", "")
		s.ended("", "")
		allowed(t, s, "run after plain text read by a shell-started command")
	})

	t.Run("a bracketed paste, then bracketed paste off", func(t *testing.T) {
		s := playedFor(t, true)
		s.prints("\x1b[?2004h")
		s.commandRuns()
		s.term.PersonInput(nil, "", "hello")
		s.ended("", "")
		s.prints("\x1b[?2004l")
		s.startsOnItsOwn()
		s.term.PersonInput(nil, "q", "")
		s.ended("", "")
		allowed(t, s, "run after plain text read by shell-started command with bracketed paste off")
	})

	t.Run("text the prompt ends with", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "$", "")
		s.ended("", "")
		s.startsOnItsOwn()
		s.term.PersonInput(nil, " ", "")
		s.ended("", "")
		allowed(t, s, "run after space read by shell-started command")
	})

	t.Run("text, then Ctrl+D at the start of a line", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "q", "")
		s.ended("", "")
		s.startsOnItsOwn()
		s.term.PersonInput(&PersonKey{Key: "d", Ctrl: true}, "", "")
		s.ended("", "")
		allowed(t, s, "run after Ctrl+D ended a shell-started command's input")
	})
}

// Ctrl+U after a bracketed paste kills it with its marks, where the shell
// has bracketed paste on at its prompt.
func TestCtrlUAfterABracketedPasteKillsIt(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.prints("\x1b[?2004h")
	s.term.PersonInput(nil, "", "rm -rf x")
	s.term.PersonInput(&PersonKey{Key: "u", Ctrl: true}, "", "")
	s.ended("", "")
	allowed(t, s, "run after Ctrl+U killed a bracketed paste")
}

// From the sixth review.

// Text with a character no shell draws as typed, a cell to each: one that
// is not graphic (U+200B, U+FEFF) or marks the one before it (a combining
// accent). bash draws it as it is, in no cell of its own, and zsh draws
// markup, measured; the screen cannot be read for the text either way.
// Text that is drawn as typed, Hangul decomposed as macOS file names are
// and jamo alone among it, is read as any.
func TestTextNotDrawnAsTypedHoldsTheLine(t *testing.T) {
	for _, c := range []struct{ name, typed, drawn string }{
		{"U+200B, bash", "rm -rf x\u200b", "rm -rf x\u200b"},
		{"U+200B, zsh", "rm -rf x\u200b", "rm -rf x\x1b[7m<200b>\x1b[27m"},
		{"U+200B before a space, bash", "rm -rf x\u200b ", "rm -rf x\u200b "},
		{"U+FEFF, bash", "rm -rf x\ufeff", "rm -rf x\ufeff"},
		{"a combining accent, zsh", "rm cafe\u0301", "rm cafe\x1b[7m<0301>\x1b[27m"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(nil, c.typed, "")
			s.ended("", c.drawn)
			refusedForThem(t, s, "run onto "+c.name+" typed while the command ran")
		})
	}
	// At a prompt that ends in ">" (fish, a custom one), zsh's markup ends
	// as the prompt does: the row alone does not say the text was read.
	for _, c := range []struct{ name, typed, drawn string }{
		{"U+200B, zsh", "rm -rf x\u200b", "rm -rf x\x1b[7m<200b>\x1b[27m"},
		{"a combining accent, zsh", "rm cafe\u0301", "rm cafe\x1b[7m<0301>\x1b[27m"},
	} {
		t.Run(c.name+", a prompt ending in >", func(t *testing.T) {
			s := playedFor(t, true)
			mark := "\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07"
			s.prints("\r\n" + mark + "> ")
			s.commandRuns()
			s.term.PersonInput(nil, c.typed, "")
			s.prints(mark + "> " + c.drawn)
			refusedForThem(t, s, "run onto "+c.name+" at a prompt ending in >")
		})
	}
	// Hangul jamo alone (U+1160, U+1161) too, measured in both shells.
	for _, typed := range []string{"\u1112\u1161\u11ab", "한", "\u1160", "\u1161"} {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "rm "+typed, "")
		s.ended("", "rm "+typed)
		refusedForThem(t, s, "run onto Hangul typed while the command ran")
		s = playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, typed, "")
		s.ended("", "")
		allowed(t, s, "run after Hangul the command read")
	}
}

// From the seventh review.

// A paste of nothing but control characters sends nothing: no bracketed
// paste marks, which a shell with bracketed paste off at its prompt draws
// (bash 3.2: 0~1~).
func TestAPasteOfNothingSendsNothing(t *testing.T) {
	s := playedFor(t, true)
	s.commandRuns()
	s.prints("\x1b[?2004h")
	s.term.PersonInput(nil, "", "\x07")
	// A key the command reads after it, to know the paste went first.
	s.term.PersonInput(nil, "q", "")
	sent := func() string {
		s.term.inMu.Lock()
		defer s.term.inMu.Unlock()
		return string(s.term.inBuf)
	}
	within(t, 5*time.Second, "the key after the paste sent", func() bool { return strings.Contains(sent(), "q") })
	if got := sent(); strings.Contains(got, "\x1b[200~") {
		t.Fatalf("sent %q for a paste of nothing", got)
	}
	s.prints("\x1b[?2004l")
	s.ended("", "")
	allowed(t, s, "run after a paste of nothing")
}

// With `stty intr undef` the terminal turns Ctrl+C into no signal: the key
// is a character, nothing is dropped, and at the next prompt bash and zsh
// draw the text and ring the bell for the key (measured). The text is
// read off the screen as any.
func TestASignalKeyNoSignalComesOfLeavesTheTextToBeRead(t *testing.T) {
	for _, who := range []string{"person", "model"} {
		t.Run(who, func(t *testing.T) {
			s := playedFor(t, true)
			s.commandRuns()
			s.term.PersonInput(nil, "rm -rf x", "")
			if who == "person" {
				s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
			} else if err := s.term.Keys("ctrl+c", s.term.PersonGen()); err != nil {
				t.Fatal(err)
			}
			// The terminal's echo while the command ran, then the prompt.
			s.ended("rm -rf x^C", "rm -rf x\a")
			refusedForThem(t, s, "run onto text a Ctrl+C read as a character left ("+who+")")
		})
	}
	// A bracketed paste the signal dropped, at a prompt with bracketed
	// paste off: nothing of it, its marks included, is there.
	s := playedFor(t, true)
	s.commandRuns()
	s.prints("\x1b[?2004h")
	s.term.PersonInput(nil, "", "rm -rf x")
	s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	s.prints("\x1b[?2004l")
	s.ended("^C\r\n", "")
	allowed(t, s, "run after a Ctrl+C dropped a bracketed paste")
	// Where the key is a signal, what it dropped is not there.
	s = playedFor(t, true)
	s.commandRuns()
	s.term.PersonInput(nil, "rm -rf x", "")
	s.term.PersonInput(&PersonKey{Key: "c", Ctrl: true}, "", "")
	s.ended("^C\r\n", "")
	allowed(t, s, "run after a Ctrl+C dropped what was typed")
}

// None of the text on the line is the command having read it only where
// the row ends as the last prompt did. Output drawn since the mark (a
// background job's) over the text, or before a slow prompt, holds the
// line instead.
func TestOutputSinceTheMarkIsNotTheTextRead(t *testing.T) {
	t.Run("output after the text", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf x", "")
		s.term.output([]byte(s.promptText(0) + "rm -rf x"))
		s.prints("50% ")
		refusedForThem(t, s, "run onto text output was drawn after")
	})
	t.Run("output before a slow prompt", func(t *testing.T) {
		s := playedFor(t, true)
		s.commandRuns()
		s.term.PersonInput(nil, "x", "")
		s.term.output([]byte("\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07"))
		s.prints("xyz")
		refusedForThem(t, s, "run onto text not drawn yet, output since the mark")
	})
	t.Run("output that ends as the prompt does", func(t *testing.T) {
		// zsh's prompt ends in "%", and so does a progress line.
		s := playedFor(t, true)
		s.prints("\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07me@test ~ % ")
		s.commandRuns()
		s.term.PersonInput(nil, "rm -rf x", "")
		s.term.output([]byte("\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x07me@test ~ % rm -rf x"))
		s.prints("50%")
		refusedForThem(t, s, "run onto text a progress line ending in % was drawn after")
	})
	t.Run("no prompt on its row to compare with", func(t *testing.T) {
		// The last prompt was taken where output left the cursor at the
		// left edge: the line is held, read or not.
		s := playedFor(t, true)
		s.prints("line\r\n")
		s.commandRuns()
		s.term.PersonInput(nil, "q", "")
		s.ended("", "")
		refusedForThem(t, s, "run with no prompt to compare the row with")
	})
	t.Run("a prompt that changes at its start", func(t *testing.T) {
		// A clock, a directory: the end is what is compared.
		s := playedFor(t, true)
		s.prints("\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x0712:00 $ ")
		s.commandRuns()
		s.term.PersonInput(nil, "q", "")
		s.prints("\x1b]133;D;0;lc=" + s.term.marks.nonce + "\x0712:01 $ ")
		allowed(t, s, "run after text the command read, at a prompt with a clock")
	})
}
