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
