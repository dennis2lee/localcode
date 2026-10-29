package agent

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A child let go of in the instant it ends either is let go, and named, or
// hands its answer back, and is not named. "Only what was let go is"
// named, says DetachChildOf.
func TestAChildThatAnsweredIsNotNamedAsLetGo(t *testing.T) {
	loop, tasks := newBackgroundLoop(t, "http://127.0.0.1:1")
	const n = 3000
	misnamed := 0
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
		ids, ok := tasks.DetachChildOf(parent)
		var s spawn
		select {
		case s = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("spawn never returned")
		}
		if ok && s.err == nil && len(ids) == 1 && ids[0] == s.id {
			misnamed++
		}
	}
	if misnamed > 0 {
		t.Errorf("%d of %d children handed their answer back to the turn and were still named as let go", misnamed, n)
	}
}

// A child that has begun recording its end is not let go, by Detach or
// by DetachChildOf: its answer goes to the turn, it is not named, and no
// "detached" lands after its end.
func TestAChildRecordingItsEndIsNotLetGo(t *testing.T) {
	_, tasks := newBackgroundLoop(t, "http://127.0.0.1:1")
	tasks.mu.Lock()
	tasks.detach["t1"] = make(chan struct{})
	tasks.syncParent["t1"] = "parent"
	tasks.ending["t1"] = true
	tasks.mu.Unlock()
	if tasks.Detach("t1") {
		t.Error("Detach let go of a child recording its end")
	}
	if ids, ok := tasks.DetachChildOf("parent"); ok || len(ids) != 0 {
		t.Errorf("DetachChildOf = %v, %v; want nothing let go", ids, ok)
	}
}
