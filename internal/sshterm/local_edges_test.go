package sshterm

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Cases from the review of the PowerShell panel. A fake console controls
// the bytes and the order of the marks; it runs no PowerShell.

// scriptedLocal is a local terminal whose console draws initial and its
// prompt, then hands the first line typed to script.
func scriptedLocal(t *testing.T, initial, prompt string, script func(*fakePS, string)) *Terminal {
	t.Helper()
	m := NewManager(Options{Local: &LocalShell{Path: "powershell.exe", Start: func(argv []string, dir string, cols, rows int) (LocalProcess, error) {
		f := newFakePS(argv, dir)
		f.promptText = prompt
		go func() {
			f.write(initial)
			f.prompt(0)
			line, err := bufio.NewReader(f.inR).ReadString('\r')
			if err == nil {
				script(f, strings.TrimSuffix(line, "\r"))
			}
		}()
		return f, nil
	}}})
	t.Cleanup(func() { m.CloseAll("test over", true) })
	return localTerminal(t, m)
}

// The marks can come ahead of the text (Windows Server 2022). The prompt
// the command was typed at is then still at the cursor, and is not the
// prompt after it: the run waits for the output, here until its timeout.
func TestALocalRunIsNotEndedByThePromptItWasTypedAt(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	term := scriptedLocal(t, "", "", func(f *fakePS, line string) {
		f.write(f.mark("C", 0) + f.mark("D", 0))
		<-release
	})
	res, err := term.Run(context.Background(), "Write-Output hello", 700*time.Millisecond, term.PersonGen())
	if err != nil || !res.TimedOut {
		t.Fatalf("a run ended on the old prompt: %+v %v", res, err)
	}
}

// A screen made smaller while a run waits is applied after its output is
// read: a line wider than the new size comes back whole.
func TestALocalRunsOutputIsReadBeforeANewSize(t *testing.T) {
	ready, proceed := make(chan struct{}), make(chan struct{})
	payload := strings.Repeat("A", 70)
	term := scriptedLocal(t, "", "", func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0))
		close(ready)
		<-proceed
		f.write(payload + "\r\n")
		f.prompt(0)
	})
	go func() { <-ready; term.ViewerSize("viewer", 40, 24); close(proceed) }()
	res, err := term.Run(context.Background(), "Write-Output long-line", 5*time.Second, term.PersonGen())
	if err != nil || res.Output != payload {
		t.Fatalf("output %q (%d), want the %d characters; %v", res.Output, len(res.Output), len(payload), err)
	}
}

// A D mark carrying the longest prompt the integration sends is kept
// whole, status and all. A prompt is limited by its encoding: 300 Hangul
// characters are 900 bytes of UTF-8 and 1,200 of base64.
func TestALongPromptsMarkIsKept(t *testing.T) {
	for _, p := range []string{strings.Repeat("한", 300) + "> ", strings.Repeat("P", maxPromptPayload*3/4-2) + "> "} {
		b64 := base64.StdEncoding.EncodeToString([]byte(p))
		if len(b64) > maxPromptPayload {
			t.Fatalf("the test prompt encodes to %d, over %d", len(b64), maxPromptPayload)
		}
		s := markScanner{nonce: "0123456789abcdef"}
		got := s.feed([]byte(fmt.Sprintf("\x1b]133;D;7;p=%s;lc=%s\x07", b64, s.nonce)))
		if len(got) != 1 || got[0].status != "7" || got[0].prompt != p {
			t.Errorf("a %d-byte prompt's mark: %+v", len(p), got)
		}
	}
	if !strings.Contains(powershellIntegration("n"), "$b.Length -le "+itoa(maxPromptPayload)) {
		t.Error("the integration does not bound the prompt by its encoded length")
	}
}

// On a Windows console Ctrl+\ is no signal: what the model typed ahead
// of it is still on the next prompt's line (measured: see signalKey), and
// the next run is refused rather than typed onto it.
func TestCtrlBackslashOnAWindowsConsoleKeepsTheTextTypedAhead(t *testing.T) {
	sent := make(chan string, 1)
	m := NewManager(Options{Local: &LocalShell{Path: "powershell.exe", Start: func(argv []string, dir string, cols, rows int) (LocalProcess, error) {
		f := newFakePS(argv, dir)
		go func() {
			f.prompt(0)
			r := bufio.NewReader(f.inR)
			line, err := r.ReadString('\r')
			if err != nil {
				return
			}
			f.write(strings.TrimSuffix(line, "\r") + "\r\n" + f.mark("C", 0))
			ahead, err := r.ReadString('\x1c')
			if err != nil {
				return
			}
			f.prompt(0)
			f.write(strings.TrimSuffix(ahead, "\x1c") + "^\\")
			next, err := r.ReadString('\r')
			if err != nil {
				return
			}
			sent <- strings.TrimSuffix(ahead, "\x1c") + next
		}()
		return f, nil
	}}})
	t.Cleanup(func() { m.CloseAll("test over", true) })
	term := localTerminal(t, m)
	res, err := term.Run(context.Background(), "Start-Sleep -Seconds 1", 100*time.Millisecond, term.PersonGen())
	if err != nil || !res.TimedOut {
		t.Fatalf("first run: %+v %v", res, err)
	}
	if err := term.Type("New-Item marker; ", false, term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	if err := term.Keys("ctrl+\\", term.PersonGen()); err != nil {
		t.Fatal(err)
	}
	within(t, 2*time.Second, "the next prompt", func() bool { return !term.Info().Running })
	time.Sleep(3 * promptSettle)
	_, err = term.Run(context.Background(), "Write-Output approved", time.Second, term.PersonGen())
	if !errors.Is(err, ErrModelLine) {
		t.Errorf("the next run: %v, want %v", err, ErrModelLine)
	}
	select {
	case line := <-sent:
		t.Errorf("a run was typed onto the text typed ahead: %q", line)
	default:
	}
}

// A command that clears the screen: what it printed after the clear is
// its output, though the prompt is now above where it was typed.
func TestALocalRunAfterAClearKeepsWhatCameAfter(t *testing.T) {
	term := scriptedLocal(t, "old1\r\nold2\r\nold3\r\nold4\r\nold5\r\nold6\r\n", "", func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "\x1b[3J\x1b[2J\x1b[Hafter-clear\r\n")
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), "Clear-Host; Write-Output after-clear", 5*time.Second, term.PersonGen())
	if err != nil || res.Output != "after-clear" {
		t.Fatalf("output %q, %v", res.Output, err)
	}
}

// The same when the output after the clear puts the prompt below where
// the command was typed: what came after the clear is the output.
func TestALocalRunAfterAClearWithMoreLinesKeepsAllOfThem(t *testing.T) {
	term := scriptedLocal(t, "old1\r\nold2\r\nold3\r\n", "", func(f *fakePS, line string) {
		// Home and erase to the end: a clear that moves nothing into
		// the scrollback, as ESC[2J does in this emulator.
		f.write(line + "\r\n" + f.mark("C", 0) + "\x1b[H\x1b[Ja1\r\na2\r\na3\r\na4\r\na5\r\na6\r\n")
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), "Clear-Host; 1..6 | % { \"a$_\" }", 5*time.Second, term.PersonGen())
	if err != nil || res.Output != "a1\na2\na3\na4\na5\na6" {
		t.Fatalf("output %q, %v", res.Output, err)
	}
}

// A multi-line prompt is drawn only when all of it is: output that pauses
// at a line like its last ("> ") is not the end of the command.
func TestALocalRunWaitsForEveryRowOfAMultiLinePrompt(t *testing.T) {
	const prompt = "~\\work on main\n> "
	term := scriptedLocal(t, "", prompt, func(f *fakePS, line string) {
		// The marks first, as on Windows Server 2022, then output that
		// stops at "> " for longer than promptSettle.
		f.write(line + "\r\n" + f.mark("C", 0) + f.mark("D", 0) + "first-line\r\n> ")
		time.Sleep(4 * promptSettle)
		f.write("quoted\r\nsecond-line\r\n" + strings.ReplaceAll(prompt, "\n", "\r\n"))
	})
	res, err := term.Run(context.Background(), "Get-Doc", 5*time.Second, term.PersonGen())
	if err != nil || res.Output != "first-line\n> quoted\nsecond-line" {
		t.Fatalf("output %q, %v", res.Output, err)
	}
}

// A prompt exactly the screen's width leaves the cursor on its last cell,
// and is drawn: the run does not wait out promptWait for it.
func TestALocalPromptAsWideAsTheScreenIsSeen(t *testing.T) {
	prompt := strings.Repeat("P", DefaultCols-1) + ">"
	term := scriptedLocal(t, "", prompt, func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "ok\r\n")
		f.prompt(0)
	})
	start := time.Now()
	res, err := term.Run(context.Background(), "Write-Output ok", 5*time.Second, term.PersonGen())
	if err != nil || res.Output != "ok" || time.Since(start) >= promptWait {
		t.Fatalf("output %q after %s, %v", res.Output, time.Since(start), err)
	}
}

// Output that ends in what the prompt ends in, and stops there for a
// while after the marks came first, is not the prompt: the row is not
// the prompt's whole.
func TestALocalRunIsNotEndedByOutputEndingLikeThePrompt(t *testing.T) {
	term := scriptedLocal(t, "", "> ", func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + f.mark("D", 0) + "<html>")
		time.Sleep(4 * promptSettle)
		f.write("<body>hello</body></html>\r\n> ")
	})
	res, err := term.Run(context.Background(), "Get-Page", 5*time.Second, term.PersonGen())
	if err != nil || res.Output != "<html><body>hello</body></html>" {
		t.Fatalf("output %q, %v", res.Output, err)
	}
}

// A run that times out before anything after its line is drawn returns
// nothing, not the rows above the prompt it was typed at.
func TestALocalRunThatTimesOutReturnsNothingFromBeforeIt(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	term := scriptedLocal(t, "earlier-line\r\n", "", func(f *fakePS, line string) {
		f.write(f.mark("C", 0) + f.mark("D", 0))
		<-release
	})
	res, err := term.Run(context.Background(), "Write-Output hello", 700*time.Millisecond, term.PersonGen())
	if err != nil || !res.TimedOut || res.Output != "" {
		t.Fatalf("%+v %v", res, err)
	}
}

// A wide character that does not fit in the last column of a row starts
// the next one, and the echo of a command with one takes a row more than
// its width says: none of it is output.
func TestALocalEchoWrappedAtAWideCharacterIsNotOutput(t *testing.T) {
	const prompt = "PS C:\\w>  " // 10 columns
	cmd := strings.Repeat("a", DefaultCols-11) + "한" + strings.Repeat("a", DefaultCols-1)
	term := scriptedLocal(t, "", prompt, func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "RESULT\r\n")
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), cmd, 5*time.Second, term.PersonGen())
	if err != nil || res.Output != "RESULT" {
		t.Fatalf("output %q, %v", res.Output, err)
	}
}

// A prompt as wide as the screen that ends in a wide character leaves
// the cursor on that character's second half, and a one-character
// command's echo is on the next row.
func TestALocalPromptAsWideAsTheScreenEndingInAWideCharacter(t *testing.T) {
	prompt := strings.Repeat("P", DefaultCols-2) + "한"
	term := scriptedLocal(t, "", prompt, func(f *fakePS, line string) {
		f.write(line + "\r\n" + f.mark("C", 0) + "ok\r\n")
		f.prompt(0)
	})
	res, err := term.Run(context.Background(), "x", 5*time.Second, term.PersonGen())
	if err != nil || res.Output != "ok" {
		t.Fatalf("output %q, %v", res.Output, err)
	}
}

func TestWrapRows(t *testing.T) {
	for _, c := range []struct {
		text string
		x0   int
		cols int
		want int
	}{
		{"abc", 0, 10, 1},
		{"abcdefghij", 0, 10, 1},
		{"abcdefghijk", 0, 10, 2},
		{"abcde", 7, 10, 2},
		{"aaaaaaaaa한", 0, 10, 2},
		{"aaaaaaaa한", 0, 10, 1},
		{"", 4, 10, 1},
	} {
		if got := len(wrapRows(c.text, c.x0, c.cols)); got != c.want {
			t.Errorf("wrapRows(%q, %d, %d): %d rows, want %d", c.text, c.x0, c.cols, got, c.want)
		}
	}
}
