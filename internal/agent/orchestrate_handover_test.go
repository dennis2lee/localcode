package agent

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/tools"
)

// A run that was let go writes its final status into its conversation
// when it ends, so a daemon handing over keeps that conversation until
// then. Two daemons writing one log is what the handover exists to
// prevent, and a let-go run can outlast an in-app update.
func TestALetGoRunsConversationIsKeptThroughAHandover(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Int32
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.Contains(p, "hold") {
			started.Add(1)
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
		}
		return "ok", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)
	done := make(chan tools.Result, 1)
	go func() {
		done <- executeOrchestrate(context.Background(), loop,
			`{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"hold"}]}`)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	run, ok := loop.Tasks.DetachChildOf("main")
	if !ok {
		t.Fatal("nothing to let go of")
	}
	<-done

	owned := append(loop.Tasks.Running(), loop.Tasks.RunningParents()...)
	kept := slices.Contains(owned, "main")

	close(release)
	deadline = time.Now().Add(5 * time.Second)
	for len(loop.Tasks.Running()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	wrote := false
	evs, _ := loop.Store.Events("main", 0)
	for _, e := range evs {
		if e.Type == events.TypeTaskStatus && e.Data["task_id"] == run && e.Data["status"] == "completed" {
			wrote = true
		}
	}
	if wrote && !kept {
		t.Errorf("the let-go run %s wrote its status into main after the let go, but main was not among the sessions "+
			"a handing-over daemon keeps: %v", run, owned)
	}
}
