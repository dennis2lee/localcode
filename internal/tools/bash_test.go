package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBashSuccess(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"command": "echo hello"})
	result := Bash{}.Execute(context.Background(), input)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if strings.TrimSpace(result.Content) != "hello" {
		t.Errorf("output = %q, want %q", result.Content, "hello")
	}
}

func TestBashNonZeroExit(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"command": "exit 1"})
	result := Bash{}.Execute(context.Background(), input)
	if !result.IsError {
		t.Error("expected an error for a nonzero exit command")
	}
}

func TestBashTimeout(t *testing.T) {
	b := Bash{Timeout: 50 * time.Millisecond}
	input, _ := json.Marshal(map[string]string{"command": "sleep 2"})

	start := time.Now()
	result := b.Execute(context.Background(), input)
	elapsed := time.Since(start)

	if !result.IsError {
		t.Error("expected a timeout error")
	}
	if elapsed > time.Second {
		t.Errorf("expected the command to be killed near the timeout, took %v", elapsed)
	}
}

// The defect this file's exit handling was rewritten for, end to end.
//
// A sweep of `grep -n` calls over a file list came back with a third of
// them marked failed and no output, the model read that as its command
// breaking, and it ran the whole sweep again. Every "failure" was a file
// that did not contain the symbol — the tool had answered the question
// correctly and then labelled the answer an error.
func TestAGrepThatFoundNothingIsNotAFailedCommand(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "namespace_lookup.h")
	if err := os.WriteFile(file, []byte("int unrelated;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	input, _ := json.Marshal(map[string]string{
		"command": "grep -n \"nslut_lookup_resp\" " + file,
	})
	result := Bash{}.Execute(context.Background(), input)

	if result.IsError {
		t.Errorf("a grep with no matches was reported as an error: %q", result.Content)
	}
	// And it has to say so, rather than coming back empty: an empty
	// result and a lost one are the same string.
	if !strings.Contains(result.Content, "no matches") {
		t.Errorf("content = %q, want it to say there were no matches", result.Content)
	}
}

// The other half of the same distinction. grep uses 1 for "I looked and
// found nothing" and 2 for "I could not look", and a search that never
// happened must not read like a search that came back clean.
func TestAGrepThatCouldNotReadTheFileIsStillAFailedCommand(t *testing.T) {
	input, _ := json.Marshal(map[string]string{
		"command": "grep -n x " + filepath.Join(t.TempDir(), "no-such-file"),
	})
	result := Bash{}.Execute(context.Background(), input)

	if !result.IsError {
		t.Errorf("grep could not open the file and that was not reported: %q", result.Content)
	}
	if strings.Contains(result.Content, "no matches") {
		t.Errorf("content = %q, want no claim that the file lacked matches", result.Content)
	}
}

// Where a second command could have produced the status, nothing is
// claimed about it. Naming the first program would be a guess, and a
// wrong one turns a failed build into a search that found nothing.
func TestAStatusFromAPipelineIsNotAttributedToTheFirstCommand(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"command": "exit 1 && echo unreachable"})
	result := Bash{}.Execute(context.Background(), input)

	if !result.IsError {
		t.Error("a status that could have come from either command was treated as an answer")
	}
}

// "exited with status" rather than "exit error": that a command exited
// non-zero is a fact, that it went wrong is an interpretation, and
// outside the table in internal/shell there are no grounds for it.
func TestAnOrdinaryFailureReportsItsStatusWithoutCallingItAnError(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"command": "exit 3"})
	result := Bash{}.Execute(context.Background(), input)

	if !result.IsError {
		t.Error("expected an error for a nonzero exit command")
	}
	if !strings.Contains(result.Content, "exited with status 3") {
		t.Errorf("content = %q, want it to name the status", result.Content)
	}
}

// A command that succeeded silently used to return the empty string,
// which is indistinguishable from a result that went astray. grep says
// "no matches" and glob "no files match" for the same reason.
func TestACommandThatPrintedNothingSaysSo(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"command": "true"})
	result := Bash{}.Execute(context.Background(), input)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "(no output)" {
		t.Errorf("content = %q, want %q", result.Content, "(no output)")
	}
}

// A command killed for running too long and one the person interrupted
// used to be the same sentence, "signal: killed", and they call for
// opposite things: the first wants narrowing and trying again, the
// second wants leaving alone. localcode set the deadline and holds the
// context, so it never had to guess which happened.
func TestATimeoutAndAnInterruptionDoNotReadTheSame(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"command": "sleep 5"})

	timedOut := Bash{Timeout: 100 * time.Millisecond}.Execute(context.Background(), input)
	if !timedOut.IsError {
		t.Error("expected a timeout to be an error")
	}
	if !strings.Contains(timedOut.Content, "timeout") {
		t.Errorf("timeout content = %q, want it to say the command ran out of time", timedOut.Content)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	cancelled := Bash{}.Execute(ctx, input)
	if !strings.Contains(cancelled.Content, "cancelled") {
		t.Errorf("cancelled content = %q, want it to say it was cancelled", cancelled.Content)
	}
	if strings.Contains(cancelled.Content, "timeout") {
		t.Errorf("cancelled content = %q, want no claim that it timed out", cancelled.Content)
	}
}

func TestBashRequiresPermission(t *testing.T) {
	if !(Bash{}.RequiresPermission(nil)) {
		t.Error("bash should always require permission")
	}
}

func TestBashSubjectExposesCommand(t *testing.T) {
	got := Bash{}.Subject(json.RawMessage(`{"command":"git status"}`))
	if got != "git status" {
		t.Errorf("Subject() = %q, want %q", got, "git status")
	}
}

func TestBashSubjectInvalidInputReturnsEmpty(t *testing.T) {
	got := Bash{}.Subject(json.RawMessage(`not json`))
	if got != "" {
		t.Errorf("Subject() = %q, want empty for malformed input", got)
	}
}

// One command's output is bounded, whatever the command decides to print.
//
// CombinedOutput accumulated everything with no ceiling, and the ceiling
// that exists runs later, on a string already fully in memory. A `find /`
// or a build with a warning per line was a daemon holding hundreds of
// megabytes for a result the model would never see more than about eighty
// kilobytes of.
func TestOneCommandsOutputIsBounded(t *testing.T) {
	w := &cappedWriter{limit: 100}
	for i := 0; i < 1000; i++ {
		if _, err := w.Write([]byte("0123456789")); err != nil {
			t.Fatal(err)
		}
	}
	out := w.bytes()
	if len(out) > 100+80 {
		t.Errorf("kept %d bytes of a 10000-byte command; the cap is 100", len(out))
	}
	if !strings.HasPrefix(string(out), "0123456789") {
		t.Errorf("the start was dropped: %q", out[:20])
	}
	if !strings.HasSuffix(string(out), "0123456789") {
		t.Errorf("the end was dropped, which is where a failure says why: %q", out[len(out)-20:])
	}
	if !strings.Contains(string(out), "not shown") {
		t.Errorf("the gap is silent: %q", out)
	}
}

// Output that fits is passed through exactly, with nothing added.
func TestOutputThatFitsIsUntouched(t *testing.T) {
	w := &cappedWriter{limit: 100}
	w.Write([]byte("hello\n"))
	w.Write([]byte("world\n"))
	if got := string(w.bytes()); got != "hello\nworld\n" {
		t.Errorf("got %q", got)
	}
}
