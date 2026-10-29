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
	"localcode/internal/hooks"
)

// Let go during a debate also lets go of a reviewer still on its way to
// starting, here inside a delegate hook that takes a moment. It starts
// let go once the hook lets it through, so the turn waits for the hook
// and not for the reviewer's whole review, which never comes here.
func TestLetGoReachesAReviewerStillStarting(t *testing.T) {
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
	// A delegate hook that takes a moment for one reviewer: a log line
	// shipped over the network, a policy check.
	loop.Config.Hooks = hooks.Config{hooks.EventDelegate: {{
		Command: `grep -q '"agent":"tom"' && sleep 1; exit 0`,
	}}}
	sid := startDebateSession(t, loop)
	var once atomic.Bool
	free := func() {
		if once.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer func() {
		free()
		deadline := time.Now().Add(15 * time.Second)
		for len(loop.Tasks.Running()) > 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- loop.SendMessage(context.Background(), sid, "boy", "/debate girl,tom 2 write a sum function")
	}()
	deadline := time.Now().Add(5 * time.Second)
	for arrived.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if arrived.Load() < 1 {
		t.Fatal("no reviewer started")
	}

	ids, ok := loop.Tasks.DetachChildOf(sid)
	if !ok {
		t.Fatal("nothing was let go while a reviewer was running")
	}
	// The reviews are never released before the turn is back: a turn
	// that comes back at all has stopped waiting on both.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Errorf("let go of %v, and the turn is still waiting on the reviewer that was still starting", ids)
		free()
		<-done
		return
	}
	ended := debateEvents(t, loop, sid, events.TypeDebateEnded)
	if len(ended) != 1 || !strings.Contains(dataString(ended[0].Data, "note"), "girl and tom keep reviewing") {
		t.Errorf("the debate did not end naming both reviewers as let go: %v", ended)
	}
}
