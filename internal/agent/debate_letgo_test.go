package agent

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/events"
)

// Letting go while a panel reviews gives the turn back at once, and no
// reviewer that was let go is recorded as having reviewed. A panel's
// reviewers are the turn's synchronous children all at the same time, so
// letting go of one of them, as it did, left the turn waiting on the rest
// and recorded the one let go with the let-go note as its review.
func TestLettingGoOfADebatePanelGivesTheTurnBack(t *testing.T) {
	script := &debateScript{authorWorks: true}
	inner := script.server(t)
	defer inner.Close()

	release := make(chan struct{})
	var arrived atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if !bytes.Contains(body, []byte(`"model":"`+authorModel+`"`)) {
			arrived.Add(1)
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
		}
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)
	var once atomic.Bool
	free := func() {
		if once.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer func() {
		free()
		deadline := time.Now().Add(5 * time.Second)
		for len(loop.Tasks.Running()) > 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- loop.SendMessage(context.Background(), sid, "boy", "/debate girl,tom 2 write a sum function")
	}()
	deadline := time.Now().Add(5 * time.Second)
	for arrived.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if arrived.Load() < 2 {
		t.Fatalf("only %d reviewers started", arrived.Load())
	}

	ids, ok := loop.Tasks.DetachChildOf(sid)
	if !ok {
		t.Fatal("nothing was let go while two reviewers were running")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Errorf("let go of %v, and the turn is still waiting on the rest of the panel", ids)
		free()
		<-done
	}

	for _, ev := range debateEvents(t, loop, sid, events.TypeDebateReview) {
		if text := dataString(ev.Data, "text"); strings.Contains(text, "still working") {
			t.Errorf("%s was let go and is recorded as reviewing: %q", dataString(ev.Data, "reviewer"), text)
		}
	}
	ended := debateEvents(t, loop, sid, events.TypeDebateEnded)
	if len(ended) != 1 {
		t.Fatalf("got %d debate.ended events, want 1", len(ended))
	}
	if reason := dataString(ended[0].Data, "reason"); reason != "let go" {
		t.Errorf("ended reason = %q, want \"let go\"", reason)
	}
	for _, id := range ids {
		if note := dataString(ended[0].Data, "note"); !strings.Contains(note, id) {
			t.Errorf("the closing note does not say where %s's review will be: %q", id, note)
		}
	}
}
