package agent

import (
	"context"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

func TestInputIDsDoNotRepeatAcrossBrokers(t *testing.T) {
	_, store, _ := newPermissionTestBroker(t)
	ids := map[string]bool{}
	for range 2 {
		broker := NewInputBroker(store)
		before, _ := store.Events("s1", 0)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); broker.Ask(ctx, "s1", "Which store?", nil) }()
		id := waitForInputQuestion(t, store, "s1", len(before))
		cancel()
		<-done
		if ids[id] {
			t.Errorf("different brokers reused input id %q", id)
		}
		ids[id] = true
	}
}

func waitForInputQuestion(t *testing.T, store *session.Store, sid string, baseline int) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs, err := store.Events(sid, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs[baseline:] {
			if ev.Type == events.TypeInputRequest {
				return dataString(ev.Data, "id")
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no input.request appeared")
	return ""
}

// Rehydration only reads. A successor daemon rehydrates every session
// before it knows which ones the daemon it replaced is still writing, so
// a write from here lands in a log another process owns.
func TestRehydrationWritesNothing(t *testing.T) {
	loop, parent := scriptedLoop(t, &scriptedProvider{}, tools.NewRegistry(nil))
	loop.Input = NewInputBroker(loop.Store)
	for _, kind := range []events.Type{events.TypePermissionRequest, events.TypeInputRequest} {
		if _, err := loop.Store.Append(parent, kind, map[string]any{"id": string(kind)}); err != nil {
			t.Fatal(err)
		}
	}
	loop.RehydrateAll()
	loop.RehydrateSession(parent)
	if evs, _ := loop.Store.Events(parent, 0); len(evs) != 2 {
		t.Errorf("rehydration wrote to the log: %v", evs)
	}
}
