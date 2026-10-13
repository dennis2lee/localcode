package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A task deleted from the task panel while its conversation waits to
// collect it. The conversation is still there, and the turn waiting on the
// task used to be told it was not: the only outcome cleanup had said "the
// session that launched this task was deleted".
func TestATaskDeletedOnItsOwnDoesNotSayItsConversationWasDeleted(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatal(err)
	}
	res := launch(t, loop.Tools, WithSessionID(context.Background(), sid), "explore", "say first-slow")
	if res.IsError {
		t.Fatalf("launch: %s", res.Content)
	}
	taskID := taskIDFrom(t, res.Content)

	got := make(chan error, 1)
	go func() {
		_, err, _ := tasks.Wait(context.Background(), taskID)
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	// What deleting the task alone does to what is remembered about it:
	// its own id is forgotten, its conversation's books are not.
	tasks.forgetTask(taskID)

	select {
	case err := <-got:
		if !errors.Is(err, errTaskDetached) {
			t.Fatalf("Wait returned %v, want the deleted outcome", err)
		}
		if strings.Contains(err.Error(), "session") || !strings.Contains(err.Error(), "task was deleted") {
			t.Errorf("the waiting turn is told %q", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deleting the task never woke the turn waiting on it")
	}
}
