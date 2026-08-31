package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// One verify_command at a time per directory.
//
// The check tool exists so a reviewer can find out whether the code
// actually runs without being handed a shell. Its whole design is that the
// command is the person's, fixed before any model saw it, and that it takes
// no arguments. What it did not account for is how many of it run at once.
//
// A debate panel is concurrent by construction (runReviews launches every
// reviewer in its own goroutine), check is in the reviewers' read-only tool
// list, and every child inherits the parent's workspace. So three reviewers
// each deciding to check the work is three `go test ./...` runs in one tree
// at the same time: a shared build cache, shared test binaries, shared
// output files, and three five-minute timeouts on a machine already busy
// running the model. An orchestrated fan-out would make it eight.
//
// Nothing here bounds what the command is, so nothing here can make it safe
// to run twice at once. Running it once at a time is the whole fix.

// concurrencyProbe brackets its own run in a shared log, so overlap is
// visible in the log's shape rather than inferred from a timing race:
// serialised runs read in,out,in,out and overlapping ones read in,in,...
func concurrencyProbe(dir string) string {
	log := filepath.Join(dir, "log")
	return "echo in >> " + log + "; sleep 0.2; echo out >> " + log
}

func TestTwoChecksInOneDirectoryDoNotRunAtOnce(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs a POSIX shell")
	}
	dir := t.TempDir()
	c := NewCheck(func() string { return concurrencyProbe(dir) })
	ctx := WithWorkingDir(context.Background(), dir)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Execute(ctx, nil)
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Fields(string(data))
	if len(lines) != 8 {
		t.Fatalf("log has %d entries, want 8: %v", len(lines), lines)
	}
	for i, want := range []string{"in", "out", "in", "out", "in", "out", "in", "out"} {
		if lines[i] != want {
			t.Fatalf("checks overlapped: log reads %v", lines)
		}
	}
}

// Different directories are different projects: two sessions working in two
// trees have nothing to contend over, and serialising them globally would
// make the second wait on the first for no reason.
func TestChecksInDifferentDirectoriesStillRunTogether(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs a POSIX shell")
	}
	a, b := t.TempDir(), t.TempDir()
	slow := func(d string) Check {
		return NewCheck(func() string { return "sleep 0.3 > " + filepath.Join(d, "out") })
	}

	started := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); slow(a).Execute(WithWorkingDir(context.Background(), a), nil) }()
	go func() { defer wg.Done(); slow(b).Execute(WithWorkingDir(context.Background(), b), nil) }()
	wg.Wait()

	// Serialised they take 0.6s; together, near 0.3s. Half a second is a
	// generous line that still fails if the two were made to queue.
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Errorf("two directories queued behind each other: %v", elapsed)
	}
}

// Waiting is not silent. A check that reads as slow because it queued
// behind two others, with nothing saying so, is the kind of number people
// draw conclusions from.
func TestAQueuedCheckSaysItWaited(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs a POSIX shell")
	}
	dir := t.TempDir()
	c := NewCheck(func() string { return "sleep 0.3" })
	ctx := WithWorkingDir(context.Background(), dir)

	var mu sync.Mutex
	var said []string
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := c.Execute(ctx, nil)
			mu.Lock()
			said = append(said, res.Content)
			mu.Unlock()
		}()
	}
	wg.Wait()

	waited := 0
	for _, s := range said {
		if strings.Contains(s, "after waiting for another check") {
			waited++
		}
	}
	if waited != 1 {
		t.Errorf("%d of 2 checks reported waiting, want exactly 1:\n%v", waited, said)
	}
}

// Esc during the queue still ends the call. Waiting for a five-minute test
// run to finish is exactly when somebody presses it.
func TestACancelledCheckDoesNotWaitForTheOneAhead(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs a POSIX shell")
	}
	dir := t.TempDir()
	c := NewCheck(func() string { return "sleep 2" })
	base := WithWorkingDir(context.Background(), dir)

	go c.Execute(base, nil)
	time.Sleep(50 * time.Millisecond) // let the first take the directory

	ctx, cancel := context.WithCancel(base)
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()

	started := time.Now()
	res := c.Execute(ctx, nil)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("a cancelled check waited %v for the one ahead of it", elapsed)
	}
	if !res.IsError || !strings.Contains(res.Content, "cancelled") {
		t.Errorf("a cancelled check did not say so: %q", res.Content)
	}
}

func TestCheckStillReportsPassAndFail(t *testing.T) {
	dir := t.TempDir()
	ok := NewCheck(func() string { return "echo fine" }).Execute(WithWorkingDir(context.Background(), dir), nil)
	if ok.IsError || !strings.Contains(ok.Content, "passed") {
		t.Errorf("a passing check: %q (isError=%v)", ok.Content, ok.IsError)
	}
	bad := NewCheck(func() string { return "exit 3" }).Execute(WithWorkingDir(context.Background(), dir), nil)
	if bad.IsError {
		t.Error("a failing check is not a failing tool")
	}
	if !strings.Contains(bad.Content, "failed") {
		t.Errorf("a failing check: %q", bad.Content)
	}
	var none Result
	none = NewCheck(func() string { return "" }).Execute(context.Background(), json.RawMessage(`{}`))
	if !none.IsError || !strings.Contains(none.Content, "no verify_command") {
		t.Errorf("an unconfigured check: %q", none.Content)
	}
}
