package daemon

import (
	"context"
	"sync"
	"testing"
)

// A message typed while a turn is running used to be held by the client
// until the turn finished. It now goes to the daemon immediately and is
// handed to the running turn, which asks for it at every tool call — so a
// correction reaches the model mid-job instead of after it has finished
// the wrong thing.

func TestInjectOnlyWhileATurnIsRunning(t *testing.T) {
	tr := newTurnTracker()

	if tr.inject("s1", "too early") {
		t.Error("accepted a message for a session with no turn running; the caller must start one instead")
	}

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !tr.begin("s1", cancel) {
		t.Fatal("begin")
	}
	if !tr.inject("s1", "hello") {
		t.Error("refused a message while a turn was running")
	}

	got, ok := tr.takeOne("s1")
	if !ok || got != "hello" {
		t.Errorf("takeOne = %q, %v; want the injected message", got, ok)
	}
	if _, ok := tr.takeOne("s1"); ok {
		t.Error("takeOne returned a second message; only one was sent")
	}
}

func TestInjectedMessagesKeepTheirOrder(t *testing.T) {
	tr := newTurnTracker()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr.begin("s1", cancel)

	for _, text := range []string{"first", "second", "third"} {
		if !tr.inject("s1", text) {
			t.Fatalf("inject %q", text)
		}
	}
	for _, want := range []string{"first", "second", "third"} {
		got, ok := tr.takeOne("s1")
		if !ok || got != want {
			t.Fatalf("takeOne = %q, %v; want %q — corrections have to arrive in the order they were typed", got, ok, want)
		}
	}
}

// The window this closes: a message accepted in the instant a turn is
// deciding to finish. If "is anything queued" and "stop being registered
// as running" were two separate decisions, such a message would be
// accepted by a turn that then ended without ever looking at the queue
// again — and no later turn would look either, because the client already
// considers it sent. finishOrTake makes it one decision under one lock.
func TestFinishOrTakeLeavesNoMessageStranded(t *testing.T) {
	const attempts = 500
	for i := 0; i < attempts; i++ {
		tr := newTurnTracker()
		_, cancel := context.WithCancel(context.Background())
		tr.begin("s1", cancel)

		var wg sync.WaitGroup
		wg.Add(2)

		var accepted bool
		go func() {
			defer wg.Done()
			accepted = tr.inject("s1", "late correction")
		}()

		var delivered bool
		go func() {
			defer wg.Done()
			_, more := tr.finishOrTake("s1")
			delivered = more
		}()

		wg.Wait()
		cancel()

		// Accepted but not delivered is the bug: the sender was told yes,
		// and nobody is left holding it.
		if accepted && !delivered {
			t.Fatalf("attempt %d: a message was accepted by a turn that had already finished, and was never answered", i)
		}
		// The reverse is fine only if it is genuinely still queued.
		if !accepted && delivered {
			t.Fatalf("attempt %d: finishOrTake handed out a message nobody had sent", i)
		}
	}
}

func TestFinishOrTakeClearsTheRegistrationWhenNothingIsQueued(t *testing.T) {
	tr := newTurnTracker()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr.begin("s1", cancel)

	if _, more := tr.finishOrTake("s1"); more {
		t.Fatal("finishOrTake reported more work with an empty queue")
	}
	if tr.busy("s1") {
		t.Error("the session is still registered as running after its turn finished")
	}
	if tr.inject("s1", "next") {
		t.Error("a message was still accepted into a turn that has ended")
	}
}

// Someone who presses stop wants the work to end — not for the messages
// they typed while waiting to be picked up and acted on afterwards.
func TestCancellingATurnDropsWhatWasQueuedForIt(t *testing.T) {
	tr := newTurnTracker()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr.begin("s1", cancel)
	tr.inject("s1", "and also update the docs")

	if !tr.cancel("s1") {
		t.Fatal("cancel reported no turn running")
	}
	if text, ok := tr.takeOne("s1"); ok {
		t.Errorf("takeOne = %q after a cancel; a stopped turn must not go on to act on it", text)
	}
}

// The premise the send handler's retry rests on.
//
// Two ordinary outcomes share one code path: a turn is running, so the
// text is injected into it; or no turn is running, so a new one starts.
// Between those two checks the running turn can end, and then neither
// applied — which used to be answered with a 409 saying the session was
// "already processing a message" about a session that had just gone idle.
// The TUI cannot tell that from the ordinary busy 409, so it queued the
// prompt and waited for a turn.done already appended: the message sat
// unsent with the spinner running until the user typed something else.
//
// inject reporting false means the registration is gone, and begin takes
// the same lock, so the next begin must succeed. That is what makes
// retrying the right answer rather than a hopeful one.
func TestBeginSucceedsOnceInjectHasReportedNoTurn(t *testing.T) {
	tr := newTurnTracker()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !tr.begin("s1", cancel) {
		t.Fatal("begin")
	}
	// The turn ends the ordinary way, with nothing queued.
	if _, more := tr.finishOrTake("s1"); more {
		t.Fatal("nothing was queued")
	}

	if tr.inject("s1", "hello") {
		t.Fatal("inject accepted a message with no turn running")
	}
	if !tr.begin("s1", cancel) {
		t.Error("begin refused after inject reported no turn; the retry would 409 with nothing running")
	}
}
