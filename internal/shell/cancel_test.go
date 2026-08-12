package shell

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// A cancelled command has to return promptly even when it left a child
// behind.
//
// This is what "Esc does nothing while a bash tool is running" was.
// exec.CommandContext kills the process it started and nothing else, and
// CombinedOutput does not return until every holder of the output pipe has
// let go of it — so one backgrounded grandchild kept the tool call blocked
// for the grandchild's whole lifetime. Measured at 30.0s against a 30s
// sleep, with the shell itself already reported as killed.
func TestCancelReturnsWithoutWaitingForOrphans(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shell here is cmd.exe and `&` means something else")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// CombinedOutput, not Start/Wait, because the pipe is the whole
	// mechanism: Wait on a command with no output to collect returns as
	// soon as the process is reaped, and the bash tool collects output.
	done := make(chan struct{})
	go func() {
		defer close(done)
		Command(ctx, "sleep 30 & sleep 30").CombinedOutput()
	}()

	// The pid is only a process-group id once the process exists, so the
	// group has to be alive before the kill is asked for.
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelling the command did not make it return within 10s")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancel took %v; it should not be waiting on the orphan", elapsed)
	}
}

// The whole tree dies, not just the shell — otherwise a cancelled `npm run
// dev` goes on holding the port after the tool call has reported itself
// stopped.
func TestCancelKillsTheWholeTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /proc-style check for a process group here")
	}

	dir := t.TempDir()
	marker := dir + "/still-alive"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The grandchild waits, then reports that it outlived the kill.
	cmd := Command(ctx, "(sleep 2; touch "+marker+") & sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	cmd.Wait()

	// Past when the grandchild would have written it, had it survived.
	time.Sleep(2500 * time.Millisecond)
	if fileExists(marker) {
		t.Error("a backgrounded grandchild survived the cancel")
	}
}
