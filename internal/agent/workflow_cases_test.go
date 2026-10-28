package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/events"
)

// With the workspace at the home directory, --user names that same
// directory, and a delete never reaches a file outside a workflow
// directory.
func TestDeletingAUserWorkflowFromTheHomeWorkspaceStaysInItsDirectory(t *testing.T) {
	m := &scriptedModel{reply: func(string) (string, map[string]any) { return "ok", nil }}
	loop := orchestrateLoop(t, m.server(t).URL)

	home := t.TempDir()
	loop.Memory = &MemoryConfig{Home: home}
	if _, err := loop.Store.CreateSessionIn("athome", "", "oracle", home, true); err != nil {
		t.Fatal(err)
	}

	// The daemon's working directory, holding an unrelated file whose name
	// happens to match.
	cwd := t.TempDir()
	victim := filepath.Join(cwd, "notes.json")
	if err := os.WriteFile(victim, []byte(`{"mine":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	if err := loop.SendMessage(context.Background(), "athome", "oracle", "/workflow delete notes --user"); err != nil {
		t.Fatal(err)
	}
	reply := lastAssistantText(loop.Store, "athome")
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("/workflow delete notes --user removed %s, a file outside any workflow directory (reply: %q)", victim, reply)
	}

	// With no home directory at all, --user names nothing, and both
	// writes refuse rather than resolve a bare file name.
	loop.Memory = &MemoryConfig{}
	// Both: os.UserHomeDir reads USERPROFILE on Windows and HOME elsewhere.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	for _, line := range []string{"/workflow delete notes --user", "/workflow save notes --user"} {
		loop.SendMessage(context.Background(), "main", "oracle", line)
		if out := lastAssistantText(loop.Store, "main"); !strings.Contains(out, "no home directory") && !strings.Contains(out, "nothing to save") {
			t.Errorf("%s with no home directory: %q", line, out)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("/workflow delete notes --user with no home directory removed %s", victim)
	}
}

// After /rewind undoes the turn that ran a workflow, a bare
// /workflow resume takes the last run that still counts.
func TestResumeAfterRewindTakesTheRunThatStillCounts(t *testing.T) {
	loop, _, home, _ := workflowLoop(t, func(p string) (string, map[string]any) {
		return "ok", nil
	})
	writeWorkflow(t, home, "first", `{"name":"first","plan":{"goal":"first goal","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"first prompt"}]}}`)
	writeWorkflow(t, home, "second", `{"name":"second","plan":{"goal":"second goal","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"second prompt"}]}}`)

	send := func(line string) string {
		t.Helper()
		if err := loop.SendMessage(context.Background(), "conv", "oracle", line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return lastAssistantText(loop.Store, "conv")
	}
	send("/workflow run first")
	runs := runIDs(loop, "conv")
	if len(runs) != 1 {
		t.Fatalf("runs = %v", runs)
	}
	firstRun := runs[0]
	send("/workflow run second")
	var secondRun string
	for _, id := range runIDs(loop, "conv") {
		if id != firstRun {
			secondRun = id
		}
	}
	t.Logf("rewind reply: %q", firstLine(send("/rewind")))

	out := send("/workflow resume")
	if strings.Contains(out, "Resumed run "+secondRun) {
		t.Errorf("after /rewind undid the turn that ran %s, \"/workflow resume\" resumed it anyway instead of %s:\n%s", secondRun, firstRun, firstLine(out))
	}
}

// A workflow run's own session opens with the goal it ran, arguments in,
// as its row in the conversation does.
func TestAWorkflowRunSessionShowsItsRenderedGoal(t *testing.T) {
	loop, _, home, _ := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	writeWorkflow(t, home, "rev", `{"name":"rev","args":"paths","plan":{"goal":"review {{args}}","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"look at {{args}}"}]}}`)
	if err := loop.SendMessage(context.Background(), "conv", "oracle", "/workflow run rev src/a.go"); err != nil {
		t.Fatal(err)
	}
	runs := runIDs(loop, "conv")
	if len(runs) != 1 {
		t.Fatalf("runs = %v", runs)
	}
	evs, _ := loop.Store.Events(runs[0], 0)
	for _, e := range evs {
		if e.Type == events.TypeUserMessage {
			if text, _ := e.Data["text"].(string); text != "review src/a.go" {
				t.Errorf("the run session opens with %q, want the goal it ran, %q", text, "review src/a.go")
			}
			break
		}
	}
}

// A /workflow run that is let go has no report yet. Its reply says where
// the report will be instead of claiming one.
func TestALetGoWorkflowRunDoesNotClaimAReport(t *testing.T) {
	var started atomic.Bool
	loop, _, home, _ := workflowLoop(t, func(p string) (string, map[string]any) {
		if strings.Contains(p, "slowly") {
			started.Store(true)
			time.Sleep(1200 * time.Millisecond)
		}
		return "ok", nil
	})
	writeWorkflow(t, home, "slow", `{"name":"slow","plan":{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"slowly"}]}}`)
	done := make(chan struct{})
	go func() {
		loop.SendMessage(context.Background(), "conv", "oracle", "/workflow run slow")
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !started.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := loop.Tasks.DetachChildOf("conv"); !ok {
		t.Fatal("nothing to let go of")
	}
	<-done
	reply := lastAssistantText(loop.Store, "conv")
	if strings.Contains(reply, "wrote this report") && !strings.Contains(reply, "Orchestration finished") {
		t.Errorf("a let-go workflow run's reply claims a report that does not exist yet:\n%s", reply)
	}
	run := loop.Store.Children("conv")[0].ID
	if !strings.Contains(reply, "was let go") || !strings.Contains(reply, "/tasks "+run) {
		t.Errorf("a let-go workflow run's reply does not say it was let go and where its report will be:\n%s", reply)
	}
	deadline = time.Now().Add(3 * time.Second)
	for len(loop.Tasks.Running()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

// /workflow list offers only names /workflow run accepts. A file named
// otherwise is listed with the reason.
func TestListedWorkflowNamesCanBeRun(t *testing.T) {
	loop, _, home, _ := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	writeWorkflow(t, home, "Review_PR", `{"name":"Review_PR","plan":{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"x"}]}}`)
	send := func(line string) string {
		if err := loop.SendMessage(context.Background(), "conv", "oracle", line); err != nil {
			t.Fatal(err)
		}
		return lastAssistantText(loop.Store, "conv")
	}
	list := send("/workflow list")
	run := send("/workflow run Review_PR")
	if strings.Contains(list, "* Review_PR (user)") && !strings.Contains(list, "cannot run here") && strings.Contains(run, "Not run") {
		t.Errorf("/workflow list offers Review_PR as runnable:\n%s\nbut /workflow run Review_PR says:\n%s", list, run)
	}
}

// The router takes /workflow followed by any whitespace, so the daemon
// must hold the same forms while a turn runs (heldworkflow_test.go).
func TestWorkflowRoutesOtherWhitespace(t *testing.T) {
	loop, _, _, _ := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	for _, text := range []string{"/workflow\nrun nosuch", "/workflow run\nnosuch", "/workflow save\tmine"} {
		if err := loop.SendMessage(context.Background(), "conv", "oracle", text); err != nil {
			t.Fatal(err)
		}
		got := lastAssistantText(loop.Store, "conv")
		if !strings.Contains(got, `no workflow called "nosuch"`) && !strings.Contains(got, "nothing to save") {
			t.Errorf("%q was not routed as a /workflow subcommand: %q", text, got)
		}
	}
}
