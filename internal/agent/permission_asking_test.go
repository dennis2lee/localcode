package agent

import (
	"context"
	"sort"
	"testing"
	"time"

	"localcode/internal/session"
	"localcode/internal/tools"
)

// Which conversations are stopped waiting for a person.
//
// A count, not a flag. One conversation collects the questions of every
// background task under it, so a flag cleared by the first answer would
// unmark a session still blocked on the other two — and the light that
// reads this is the only steady amber one in the product, meaning "this
// one is yours".

func askingBroker(t *testing.T) (*PermissionBroker, *session.Store) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	return NewPermissionBroker(store), store
}

// ask raises one question and returns a function that answers it.
func ask(t *testing.T, b *PermissionBroker, sessionID string) (done chan bool, answer func(bool)) {
	t.Helper()
	fn := b.Func()
	done = make(chan bool, 1)
	go func() {
		ok, _ := fn(WithSessionID(context.Background(), sessionID), tools.Ask{
			Tool: "bash", Subject: "ls", Description: "run: ls",
		})
		done <- ok
	}()
	// The question is on the log before the wait begins, so its arrival is
	// what says the broker has reached the point this test is about.
	waitFor(t, func() bool { return len(b.Asking()) > 0 })
	return done, func(allow bool) {
		for _, id := range b.pendingIDs() {
			b.Resolve(id, allow, ScopeOnce)
		}
	}
}

// pendingIDs is the ids of the questions currently unanswered.
//
// In the test file rather than beside the broker, because nothing in the
// product needs it: a client is told an id when the question is raised
// and answers with that. An accessor that exists only for tests, sitting
// in production code, is a surface somebody later reads as an API.
func (b *PermissionBroker) pendingIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.pending))
	for id := range b.pending {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting")
}

func TestAConversationHoldingAQuestionIsReportedAsWaiting(t *testing.T) {
	b, _ := askingBroker(t)
	if len(b.Asking()) != 0 {
		t.Fatal("a fresh broker reports somebody waiting")
	}

	done, answer := ask(t, b, "s1")
	if !b.Asking()["s1"] {
		t.Error("the conversation holding the question is not reported")
	}

	answer(true)
	<-done
	waitFor(t, func() bool { return !b.Asking()["s1"] })
}

// The reason it is a count. Two questions in one conversation, answered
// one at a time: after the first answer the conversation is still
// stopped, and a flag would say it was not.
func TestAnsweringOneOfTwoQuestionsLeavesTheConversationWaiting(t *testing.T) {
	b, _ := askingBroker(t)

	fn := b.Func()
	done := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		go func() {
			ok, _ := fn(WithSessionID(context.Background(), "s1"), tools.Ask{
				Tool: "bash", Subject: "ls", Description: "run: ls",
			})
			done <- ok
		}()
	}
	waitFor(t, func() bool { return len(b.pendingIDs()) == 2 })
	if !b.Asking()["s1"] {
		t.Fatal("not reported as waiting")
	}

	ids := b.pendingIDs()
	b.Resolve(ids[0], true, ScopeOnce)
	<-done
	if !b.Asking()["s1"] {
		t.Error("one answer unmarked a conversation still holding another question")
	}

	b.Resolve(ids[1], true, ScopeOnce)
	<-done
	waitFor(t, func() bool { return !b.Asking()["s1"] })
}

// A cancelled turn takes its question with it. A conversation left marked
// would show a light for a question nobody can see.
func TestACancelledTurnStopsBeingReportedAsWaiting(t *testing.T) {
	b, _ := askingBroker(t)
	ctx, cancel := context.WithCancel(context.Background())
	fn := b.Func()
	done := make(chan struct{})
	go func() {
		fn(WithSessionID(ctx, "s1"), tools.Ask{Tool: "bash", Subject: "ls", Description: "run: ls"})
		close(done)
	}()
	waitFor(t, func() bool { return b.Asking()["s1"] })

	cancel()
	<-done
	waitFor(t, func() bool { return !b.Asking()["s1"] })
}

// A question raised by a background task is answered from the conversation
// that spawned it, so that is the conversation the light belongs on: the
// task's own session is in no list and cannot be looked at.
func TestATasksQuestionMarksTheConversationItCanBeAnsweredFrom(t *testing.T) {
	b, store := askingBroker(t)
	if _, err := store.CreateSession("task-1", "s1", "explore", false); err != nil {
		t.Fatal(err)
	}

	fn := b.Func()
	done := make(chan struct{})
	go func() {
		fn(WithSessionID(context.Background(), "task-1"), tools.Ask{
			Tool: "bash", Subject: "ls", Description: "run: ls",
		})
		close(done)
	}()
	waitFor(t, func() bool { return len(b.Asking()) > 0 })

	if !b.Asking()["s1"] {
		t.Error("the parent conversation, where the question is shown, is not marked")
	}

	for _, id := range b.pendingIDs() {
		b.Resolve(id, true, ScopeOnce)
	}
	<-done
}
