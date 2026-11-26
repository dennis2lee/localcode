package sshterm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Local runs at full-width prompts and across screen clears, cases from
// the third review of the PowerShell panel.

// A full-width prompt ending in a space (PowerShell's "PS ...> " on a long
// path) leaves the cursor on its last cell, and a one-character command's
// echo is on the next row: none of it is output. A written space is not
// told from an empty cell on the screen; the prompt's width tells it.
func TestAFullWidthPromptEndingInASpaceKeepsTheEchoOut(t *testing.T) {
	prompt := strings.Repeat("P", DefaultCols-2) + "> "
	term := scriptedLocal(t, "", prompt, func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "ok\r\n")
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), "x", 5*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "ok" {
		t.Errorf("output = %q, want %q", res.Output, "ok")
	}
}

// The same with a command wider than the screen.
func TestAFullWidthPromptEndingInASpaceKeepsALongEchoOut(t *testing.T) {
	prompt := strings.Repeat("P", DefaultCols-2) + "> "
	cmd := strings.Repeat("a", DefaultCols) + "Z"
	term := scriptedLocal(t, "", prompt, func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "ok\r\n")
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), cmd, 5*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "ok" {
		t.Errorf("output = %q, want %q", res.Output, "ok")
	}
}

// A clear (Clear-Host) at a full-width prompt: what the command printed
// after it is the output. Clears are found in the output itself, not
// worked out from the rows.
func TestAClearAtAFullWidthPromptKeepsWhatCameAfter(t *testing.T) {
	prompt := strings.Repeat("P", DefaultCols-1) + ">"
	term := scriptedLocal(t, "old1\r\nold2\r\nold3\r\nold4\r\nold5\r\n", prompt, func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "\x1b[H\x1b[Jafter-clear\r\n")
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), "Clear-Host; Write-Output after-clear", 5*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "after-clear" {
		t.Errorf("output = %q, want %q", res.Output, "after-clear")
	}
}

// The same at a prompt that ends in a line end, whose cursor starts on a
// blank row.
func TestAClearAtAPromptEndingInALineEndKeepsWhatCameAfter(t *testing.T) {
	prompt := "PS C:\\work\n"
	term := scriptedLocal(t, "old1\r\nold2\r\nold3\r\nold4\r\nold5\r\n", prompt, func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "\x1b[H\x1b[Jafter-clear\r\n")
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), "Clear-Host; Write-Output after-clear", 5*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "after-clear" {
		t.Errorf("output = %q, want %q", res.Output, "after-clear")
	}
}

// A clear followed by more lines than the screen holds: the lines that
// scrolled off are output too.
func TestAClearFollowedByMoreThanAScreenKeepsEveryLine(t *testing.T) {
	const total = DefaultRows + 5
	var want []string
	var body strings.Builder
	for i := 1; i <= total; i++ {
		line := fmt.Sprintf("a%d", i)
		want = append(want, line)
		body.WriteString(line + "\r\n")
	}
	term := scriptedLocal(t, "old1\r\nold2\r\nold3\r\n", "", func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "\x1b[H\x1b[J" + body.String())
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), "Clear-Host; 1..35 | % { \"a$_\" }", 5*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	wantStr := strings.Join(want, "\n")
	if res.Output != wantStr {
		gotLines := strings.Split(res.Output, "\n")
		t.Errorf("got %d lines starting with %q, want %d lines starting with %q", len(gotLines), gotLines[0], len(want), want[0])
	}
}

// ESC[2J moves only the rows with text into the scrollback, and the new
// prompt can land on the row the command was typed on: the output is
// still what came after the clear.
func TestAnEraseWithBlankLinesAboveThePromptKeepsWhatCameAfter(t *testing.T) {
	term := scriptedLocal(t, "old1\r\n\r\nold2\r\n\r\n", "", func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "\x1b[2J\x1b[Hafter-clear\r\n")
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), "Clear-Host; Write-Output after-clear", 5*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "after-clear" {
		t.Errorf("output = %q, want %q", res.Output, "after-clear")
	}
}

// A command whose echo starts with a blank row (a wide character that
// wraps, leading spaces) and clears the screen.
func TestAClearAfterACommandWhoseFirstRowIsBlankKeepsWhatCameAfter(t *testing.T) {
	prompt := strings.Repeat("P", DefaultCols-1)
	term := scriptedLocal(t, "old1\r\nold2\r\nold3\r\nold4\r\n", prompt, func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "\x1b[H\x1b[Jafter-clear\r\n")
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), "  Clear-Host; Write-Output after-clear", 5*time.Second, term.PersonGen())
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "after-clear" {
		t.Errorf("output = %q, want %q", res.Output, "after-clear")
	}
}

// What a Windows pseudo console sends for Clear-Host, as measured: home,
// every row erased, ESC[3J, every row erased again, home, then the
// output. No ESC[2J.
func TestAPseudoConsolesClearIsFound(t *testing.T) {
	rows := strings.Repeat("\x1b[K\r\n", DefaultRows-1) + "\x1b[K"
	clear := "\x1b[?25l\x1b[m\x1b[H\x1b[?25h\x1b[?25l" + rows + "\x1b[H\x1b[?25h\x1b[?25l\x1b[3J" + rows + "\x1b[H\x1b[?25h"
	term := scriptedLocal(t, "before 1\r\nbefore 2\r\nbefore 3\r\n", "", func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + clear + "after-clear\r\nsecond\r\n")
		f.prompt(0)
	})
	start := time.Now()
	res, err := term.Run(context.Background(), "Clear-Host; Write-Output after-clear; Write-Output second", 5*time.Second, term.PersonGen())
	if err != nil || res.Output != "after-clear\nsecond" {
		t.Fatalf("output %q, %v", res.Output, err)
	}
	if took := time.Since(start); took >= promptWait {
		t.Errorf("took %s: the prompt after the clear was not seen", took)
	}
}

// Each way of clearing a screen is found: what came after it is the
// output, and the prompt after it is seen at once.
func TestEveryClearIsFound(t *testing.T) {
	for name, clear := range map[string]string{
		"erase all":            "\x1b[2J\x1b[H",
		"erase the scrollback": "\x1b[3J\x1b[H",
		"home and erase":       "\x1b[H\x1b[J",
		"home, colour, erase":  "\x1b[H\x1b[0m\x1b[J",
		"home, cursor, erase":  "\x1b[1;1H\x1b[?25l\x1b[0J\x1b[?25h",
		"row only":             "\x1b[1H\x1b[J",
		"zeros":                "\x1b[0;0H\x1b[J",
		"HVP":                  "\x1b[f\x1b[J",
		"HVP 1;1":              "\x1b[1;1f\x1b[J",
		"reset":                "\x1bc",
	} {
		t.Run(name, func(t *testing.T) {
			term := scriptedLocal(t, "old1\r\nold2\r\nold3\r\nold4\r\nold5\r\n", "", func(f *fakePS, line string) {
				f.write(line + "\r\n" + f.mark("C", 0) + clear + "after-clear\r\n")
				f.prompt(0)
			})
			start := time.Now()
			res, err := term.Run(context.Background(), "Clear-Host; Write-Output after-clear", 5*time.Second, term.PersonGen())
			if err != nil || res.Output != "after-clear" || time.Since(start) >= promptWait {
				t.Fatalf("output %q after %s, %v", res.Output, time.Since(start), err)
			}
		})
	}
}

// A clear of the alternate screen (a full-screen program that came and
// went) is not a clear of the screen the run's output is on.
func TestAClearOfTheAlternateScreenIsNot(t *testing.T) {
	term := scriptedLocal(t, "old1\r\nold2\r\nold3\r\n", "", func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "\x1b[?1049h\x1b[2J\x1b[Halt-content\x1b[?1049lreal-output\r\n")
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), "Run-Tui", 5*time.Second, term.PersonGen())
	if err != nil || res.Output != "real-output" {
		t.Fatalf("output %q, %v", res.Output, err)
	}
}

// A prompt whose last row the terminal wrapped at a wide character, and
// that ends in a space at the screen's edge.
func TestAPromptWrappedAtAWideCharacterEndingAtTheEdge(t *testing.T) {
	prompt := strings.Repeat("a", DefaultCols-1) + "한" + strings.Repeat("b", DefaultCols-3) + " "
	term := scriptedLocal(t, "", prompt, func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "ok\r\n")
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), "x", 5*time.Second, term.PersonGen())
	if err != nil || res.Output != "ok" {
		t.Fatalf("output %q, %v", res.Output, err)
	}
}
