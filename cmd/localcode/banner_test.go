package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn and answers with what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	os.Stdout = saved
	return <-done
}

// The banner names no version.
//
// The number it printed was this binary's, and this binary is not always
// what ends up running: where it cannot replace itself it hands over to a
// newer copy a moment later. It said v0.133.0 over a session served by
// v0.141.0 for as long as somebody had an MSI install — and the version
// on a banner exists to answer "what am I running", which is the one
// question it was answering wrongly.
func TestTheBannerNamesNoVersion(t *testing.T) {
	saved := version
	version = "9.9.9"
	t.Cleanup(func() { version = saved })

	out := captureStdout(t, printBanner)

	if strings.Contains(out, "9.9.9") {
		t.Errorf("the banner names a version:\n%s", out)
	}
	if strings.Contains(out, "v9.9.9") || strings.Contains(out, " · v") {
		t.Errorf("the banner still has the version's punctuation around it:\n%s", out)
	}
	// It still says what the program is, which is the half worth keeping.
	for _, want := range []string{"l o c a l c o d e", "Multi LLM coding agent"} {
		if !strings.Contains(out, want) {
			t.Errorf("the banner no longer says %q:\n%s", want, out)
		}
	}
}

// And the wait after it is narrated.
//
// Building a daemon takes as long as its slowest part, and one MCP server
// that does not answer outlasts everything else put together. The
// terminal printed the banner and then nothing at all for however long
// that took, with no way to tell a slow start from a stuck one.
func TestAStartupStepIsPrintedWhereSomebodyIsWaiting(t *testing.T) {
	out := captureStdout(t, func() { printStartupStep("connecting to MCP server github") })
	if !strings.Contains(out, "connecting to MCP server github") {
		t.Errorf("the step was not printed: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("the step does not end its line: %q", out)
	}
	// No cursor tricks, for the reason the banner gives: a startup
	// message is not worth a portability risk on a Windows terminal.
	if strings.ContainsAny(out, "\x1b\r") {
		t.Errorf("the step writes escape codes or carriage returns: %q", out)
	}
}

// And the terminal's daemon is the one that gets them. The headless
// daemon deliberately does not: it is meant to run unattended, where
// these would be noise in a log file, which is the same reason it skips
// the banner.
func TestTheTerminalPassesItsStartupStepsAlong(t *testing.T) {
	src := modesSource(t)
	start := strings.Index(src, "func runEmbedded(")
	if start < 0 {
		t.Fatal("runEmbedded is gone")
	}
	body := src[start:]
	if end := strings.Index(body[1:], "\nfunc "); end >= 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "buildDaemon(context.Background(), configPath, printStartupStep)") {
		t.Error("runEmbedded builds its daemon without saying what it is doing; the banner is up and the prompt is not")
	}

	start = strings.Index(src, "func runDaemon(")
	body = src[start:]
	if end := strings.Index(body[1:], "\nfunc "); end >= 0 {
		body = body[:end]
	}
	if strings.Contains(body, "printStartupStep") {
		t.Error("the headless daemon prints startup steps, which is noise in the log file it runs into")
	}
}
