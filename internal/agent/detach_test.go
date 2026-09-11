package agent

import (
	"context"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Letting go of a synchronous sub-agent.
//
// A synchronous delegation had two endings, finish or be killed, and the
// common third case had neither: the sub-agent is doing something useful
// and slow, and the person wants their own turn back. Stopping it threw
// the work away.

// slowServer answers only once release is closed, so a synchronous child
// can be caught mid-flight without sleeping for a fixed time.
func slowServer(t *testing.T, release <-chan struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		case <-time.After(20 * time.Second):
			t.Error("the slow server was never released")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range textChunks("done at last") {
			w.Write([]byte("data: " + c + "\n\n"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The parent's turn comes back before the child has finished, and the
// child keeps going.
func TestLettingGoReturnsTheTurnAndKeepsTheChild(t *testing.T) {
	release := make(chan struct{})
	srv := slowServer(t, release)
	loop, tasks := newBackgroundLoop(t, srv.URL)
	if _, err := loop.Store.CreateSession("parent", "", "explore", true); err != nil {
		t.Fatal(err)
	}

	type spawn struct {
		taskID, text string
		err          error
	}
	answered := make(chan spawn, 1)
	go func() {
		id, text, err := tasks.SpawnSyncInto(context.Background(), "parent", "", "explore", "take your time")
		answered <- spawn{id, text, err}
	}()

	// Let go of whatever is blocking the parent, without knowing the id —
	// which is how it is actually asked.
	var taskID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if id, ok := tasks.DetachChildOf("parent"); ok {
			taskID = id
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if taskID == "" {
		t.Fatal("nothing was blocking the parent to let go of")
	}

	select {
	case got := <-answered:
		if got.err != nil {
			t.Fatalf("the delegation returned an error rather than letting go: %v", got.err)
		}
		// What the model is told has to say the work is still happening.
		// "Cancelled" would have it start the same job again, which is
		// two sub-agents doing one piece of work with one of them
		// invisible.
		for _, want := range []string{"still working", taskID, "Do not start it again"} {
			if !strings.Contains(got.text, want) {
				t.Errorf("the note does not say %q: %s", want, got.text)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the parent's turn never came back after letting go")
	}

	// And the child is still going: it has not been cancelled, and it
	// finishes once the server answers.
	close(release)
	waitUntil(t, 5*time.Second, func() bool {
		return strings.Contains(lastAssistantText(loop.Store, taskID), "done at last")
	}, "the child never finished after being let go")
}

// Letting go twice is what a person does when the first one looked like
// nothing happened. It must not panic on a closed channel.
func TestLettingGoTwiceIsHarmless(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv := slowServer(t, release)
	loop, tasks := newBackgroundLoop(t, srv.URL)
	if _, err := loop.Store.CreateSession("parent", "", "explore", true); err != nil {
		t.Fatal(err)
	}
	go tasks.SpawnSyncInto(context.Background(), "parent", "", "explore", "take your time")

	var taskID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if id, ok := tasks.DetachChildOf("parent"); ok {
			taskID = id
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if taskID == "" {
		t.Fatal("nothing was blocking the parent")
	}
	if tasks.Detach(taskID) {
		t.Error("letting go of the same child twice reported a second success")
	}
	if _, ok := tasks.DetachChildOf("parent"); ok {
		t.Error("the parent still has something to let go of")
	}
}

// Nothing running means nothing to let go of, said plainly rather than
// as an error.
func TestLettingGoWithNothingRunning(t *testing.T) {
	_, tasks := newBackgroundLoop(t, "http://127.0.0.1:1")
	if id, ok := tasks.DetachChildOf("parent"); ok {
		t.Errorf("it let go of %q in a conversation with nothing running", id)
	}
}

// Until it is let go, the parent's cancellation still reaches the child —
// the behaviour that existed before, which the new goroutine has to
// reproduce rather than replace.
func TestAnAttachedChildStillDiesWithItsParent(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv := slowServer(t, release)
	loop, tasks := newBackgroundLoop(t, srv.URL)
	if _, err := loop.Store.CreateSession("parent", "", "explore", true); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, _, err := tasks.SpawnSyncInto(ctx, "parent", "", "explore", "take your time")
		errs <- err
	}()
	// Wait until the child is registered, then cancel the parent.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tasks.mu.Lock()
		n := len(tasks.syncParent)
		tasks.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-errs:
		if err == nil {
			t.Error("cancelling the parent left the synchronous child running")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the parent never ended the delegation")
	}
}

// waitUntil polls cond until it holds or the deadline passes. Its own
// function rather than the two-second waitFor beside it, because a
// sub-agent finishing a turn is not on the same timescale as a
// permission being answered.
func waitUntil(t *testing.T, within time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
