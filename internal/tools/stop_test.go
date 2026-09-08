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

// Pressing stop has to reach a tool that is already running.
//
// The turn's context was already threaded to every tool, and bash binds
// its process group to it — but the two searches walked the tree with no
// context at all. Stopping during one ended the turn on paper, while the
// walk carried on to the end of the disk holding the turn's goroutine and
// the session's busy flag. A search is work, and somebody who presses
// stop is asking for the work to end.

// bigTree builds a directory deep and wide enough that a walk over it
// takes long enough to be cancelled part way, without being slow enough
// to matter in a test run.
func bigTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < 60; i++ {
		dir := filepath.Join(root, "pkg", "a"+strings.Repeat("x", i%7), "b", "c")
		dir = filepath.Join(dir, "d"+string(rune('a'+i%26)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		for j := 0; j < 40; j++ {
			name := filepath.Join(dir, "f"+string(rune('a'+j%26))+".go")
			if err := os.WriteFile(name, []byte("package p\n// needle\n"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
	}
	return root
}

func TestAStoppedTurnStopsTheSearch(t *testing.T) {
	root := bigTree(t)
	// Every shape of search, including the one that does not go through a
	// walk of ours: a pattern with no "**" is filepath.Glob, which has
	// nothing to check a context at, and it measured at 950ms on a
	// project-sized tree while ignoring the stop completely.
	for _, c := range []struct {
		name  string
		tool  Tool
		input string
		smart bool
	}{
		{"glob", Glob{}, `{"pattern":"**/*.go"}`, false},
		{"glob, smart", Glob{}, `{"pattern":"**/*.go"}`, true},
		{"glob with no **", Glob{}, `{"pattern":"pkg/*/*/*/*/*.go"}`, false},
		{"glob with no **, smart", Glob{}, `{"pattern":"pkg/*/*/*/*/*.go"}`, true},
		{"grep", Grep{}, `{"pattern":"needle","path":"."}`, false},
		{"grep, smart", Grep{}, `{"pattern":"needle","path":"."}`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			ctx = WithWorkingDir(ctx, root)
			if c.smart {
				ctx = WithSmartAgent(ctx, true)
			}
			// Already stopped when the tool is entered, which is the
			// state a walk part way through reaches on the next entry.
			cancel()

			done := make(chan Result, 1)
			go func() { done <- c.tool.Execute(ctx, json.RawMessage(c.input)) }()
			select {
			case res := <-done:
				if !strings.Contains(res.Content, "cancelled") {
					t.Errorf("a stopped %s returned %q", c.name, res.Content)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("a stopped %s never returned", c.name)
			}
		})
	}
}

// And a search nobody stopped still answers.
func TestASearchThatIsNotStoppedStillAnswers(t *testing.T) {
	root := bigTree(t)
	ctx := WithWorkingDir(context.Background(), root)
	// Every pattern the tests above stop is run here without a stop, so
	// a pattern that matches nothing cannot make those pass vacuously.
	for _, pattern := range []string{`{"pattern":"**/*.go"}`, `{"pattern":"pkg/*/*/*/*/*.go"}`} {
		res := (Glob{}).Execute(ctx, json.RawMessage(pattern))
		if res.IsError {
			t.Errorf("glob %s: %s", pattern, res.Content)
		}
		if !strings.Contains(res.Content, ".go") {
			t.Errorf("glob %s matched nothing, so stopping it proves nothing", pattern)
		}
	}
	if res := (Glob{}).Execute(ctx, json.RawMessage(`{"pattern":"**/*.go"}`)); res.IsError {
		t.Errorf("glob: %s", res.Content)
	}
	if res := (Grep{}).Execute(ctx, json.RawMessage(`{"pattern":"needle","path":"."}`)); res.IsError {
		t.Errorf("grep: %s", res.Content)
	}
}

// Stopped part way through, not before it starts.
//
// The test above cancels on entry, which an implementation that looked at
// the context once at the top would also pass. This one lets the search
// get going and stops it in flight, which only a search that checks as it
// goes can answer promptly.
func TestASearchIsStoppedInFlight(t *testing.T) {
	root := bigTree(t)
	for _, c := range []struct {
		name  string
		tool  Tool
		input string
	}{
		// The two that walk. A pattern with no "**" is not here: it
		// finishes this tree in under a millisecond, so a race against a
		// cancel would be decided by whichever won, and what that case
		// promises is tested directly in TestGlobOrStopAbandonsTheGlob.
		{"glob", Glob{}, `{"pattern":"**/*.go"}`},
		{"grep", Grep{}, `{"pattern":"needle","path":"."}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			ctx = WithWorkingDir(ctx, root)
			done := make(chan Result, 1)
			go func() { done <- c.tool.Execute(ctx, json.RawMessage(c.input)) }()
			// Long enough for the search to be under way, short enough
			// that it cannot have finished: the whole tree walks in about
			// a second.
			time.Sleep(2 * time.Millisecond)
			cancel()
			select {
			case res := <-done:
				if !strings.Contains(res.Content, "cancelled") {
					t.Errorf("a %s stopped in flight answered %.60q", c.name, res.Content)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("a %s stopped in flight never returned", c.name)
			}
		})
	}
}

// One file can be most of a search. The walk checks the context between
// files, so a log of a few hundred megabytes is a single entry and used
// to be scanned to its end after the stop.
func TestAStoppedSearchDoesNotFinishOneHugeFile(t *testing.T) {
	root := t.TempDir()
	// Big enough that scanning it takes long enough to interrupt, and
	// with no match in it so nothing short-circuits.
	line := strings.Repeat("x", 120) + "\n"
	var b strings.Builder
	for b.Len() < 40<<20 {
		b.WriteString(line)
	}
	if err := os.WriteFile(filepath.Join(root, "huge.log"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithWorkingDir(ctx, root)
	done := make(chan Result, 1)
	go func() { done <- (Grep{}).Execute(ctx, json.RawMessage(`{"pattern":"needle","path":"."}`)) }()
	time.Sleep(2 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if !strings.Contains(res.Content, "cancelled") {
			t.Errorf("grep finished the file after the stop: %.60q", res.Content)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("grep never returned")
	}
}

// A pattern with no "**" goes to filepath.Glob, which offers nothing to
// check a context at, so the wait is beside it rather than inside it: on
// a stop the call returns and the glob is left to finish into the void.
//
// Abandoning it is safe in a way abandoning most work is not — it reads
// directory entries and returns a list, touching nothing and holding
// nothing — and the goroutine cannot block, because the channel it sends
// on is buffered.
func TestGlobOrStopAbandonsTheGlob(t *testing.T) {
	root := bigTree(t)
	pattern := filepath.Join(root, "pkg", "*", "*", "*", "*", "*.go")

	// Not stopped: the real answer.
	out, err := globOrStop(context.Background(), pattern)
	if err != nil {
		t.Fatalf("globOrStop: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("the pattern matched nothing, so this proves nothing")
	}

	// Stopped: the sentinel, with no answer.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := globOrStop(ctx, pattern)
	if !walkStopped(err) {
		t.Errorf("err = %v, want the stopped sentinel", err)
	}
	if got != nil {
		t.Errorf("a stopped glob returned %d paths", len(got))
	}
}
