package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"localcode/internal/events"
)

// A synchronous child let go of in the instant it finishes ends with its
// own status last. "detached" written after "completed" is read as still
// working by both clients, so a finished child showed as running in the
// background for good. Races the real DetachChildOf against a child that
// finishes at the same moment, and counts the children left that way.
func TestLettingGoAsTheChildFinishesLeavesNoGhost(t *testing.T) {
	loop, tasks := newBackgroundLoop(t, "http://127.0.0.1:1")
	const n = 3000
	ghosts := 0
	for i := 0; i < n; i++ {
		parent := fmt.Sprintf("parent%d", i)
		if _, err := loop.Store.CreateSession(parent, "", "explore", true); err != nil {
			t.Fatal(err)
		}
		entered := make(chan struct{})
		finish := make(chan struct{})
		type spawn struct {
			id  string
			err error
		}
		done := make(chan spawn, 1)
		go func() {
			id, _, err := tasks.SpawnRun(context.Background(), parent, "goal", nil, func(ctx context.Context, taskID string) (string, error) {
				close(entered)
				<-finish
				return "finished", nil
			})
			done <- spawn{id, err}
		}()
		<-entered
		close(finish)
		tasks.DetachChildOf(parent)
		var s spawn
		select {
		case s = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("spawn never returned")
		}
		// The last task.status the parent recorded for this child.
		// Waits briefly for the child's own goroutine to finish writing.
		deadline := time.Now().Add(time.Second)
		var last string
		var sawCompleted bool
		for {
			evs, _ := loop.Store.Events(parent, 0)
			last, sawCompleted = "", false
			for _, ev := range evs {
				if ev.Type != events.TypeTaskStatus || ev.Data["task_id"] != s.id {
					continue
				}
				st, _ := ev.Data["status"].(string)
				last = st
				if st == "completed" {
					sawCompleted = true
				}
			}
			if sawCompleted || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if sawCompleted && last == "detached" {
			ghosts++
		}
	}
	if ghosts > 0 {
		t.Errorf("%d of %d children that completed are left with \"detached\" as their last status, which both clients show as still running", ghosts, n)
	}
}
