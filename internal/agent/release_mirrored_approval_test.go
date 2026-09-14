package agent

import (
	"context"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/tools"
)

// An approval given for a background task does not become the parent
// conversation's approval across a restart.
//
// The user sees the task's question in their own conversation and answers
// "allow for this session". That answer is for the task's work. After a
// restart — an update, a crash, a handoff to a newer daemon — the parent
// conversation must ask again for the same command instead of acting on
// the task's approval, while the task itself keeps what it was given.
func TestABackgroundTaskApprovalDoesNotBecomeTheParents(t *testing.T) {
	first, store, _ := newPermissionTestBroker(t)
	const parent, task = "s1", "t1"
	if _, err := store.CreateSession(task, parent, "general-purpose", false); err != nil {
		t.Fatalf("create task session: %v", err)
	}

	ask := tools.Ask{Tool: "bash", Subject: "npm test", Description: "run the tests"}
	released := make(chan bool, 1)
	go func() {
		ok, err := first.Func()(WithSessionID(context.Background(), task), ask)
		released <- ok && err == nil
	}()

	// The question reaches the conversation the user is looking at, and
	// says which background task it came from.
	id := waitForPermissionID(t, store, parent, 0)
	evs, err := store.Events(parent, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var shown bool
	for _, ev := range evs {
		if ev.Type == events.TypePermissionRequest {
			shown = true
		}
	}
	if !shown {
		t.Fatal("the task's question never appeared in the parent conversation")
	}

	// The user answers "allow for this session" from the parent side.
	first.Resolve(id, true, ScopeSession)
	select {
	case ok := <-released:
		if !ok {
			t.Fatal("the task's call was not allowed after the answer")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the task is still blocked after its question was answered")
	}

	// The process restarts. The log is all the new process knows.
	again := rebuiltBroker(t, first)
	if remembered(t, again, parent, ask) {
		t.Error("the parent acted on the task's approval without asking: restart granted what only the task was given")
	}
	if !remembered(t, again, task, ask) {
		t.Error("the task lost the approval it was actually given")
	}
}
