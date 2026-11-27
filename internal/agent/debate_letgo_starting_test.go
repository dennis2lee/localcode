package agent

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
			case <-time.After(2 * time.Minute):
			}
		}
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	// A delegate hook that holds one reviewer until the test lets it
	// through: a log line shipped over the network, a policy check. It
	// waits on a file rather than sleeping, so tom is still starting when
	// he is let go however slowly the shell starts (Git Bash on a loaded
	// Windows runner took most of a 5 second window). Bounded at 60
	// seconds so a broken test cannot hang the suite.
	gate := filepath.ToSlash(filepath.Join(t.TempDir(), "gate"))
	loop.Config.Hooks = hooks.Config{hooks.EventDelegate: {{
		Command: `grep -q '"agent":"tom"' || exit 0; n=0; while [ ! -f '` + gate + `' ] && [ $n -lt 1200 ]; do sleep 0.05; n=$((n+1)); done; exit 0`,
	}}}
	letThrough := func() {
		if err := os.WriteFile(filepath.FromSlash(gate), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sid := startDebateSession(t, loop)
	var once atomic.Bool
	free := func() {
		if once.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer func() {
		_ = os.WriteFile(filepath.FromSlash(gate), nil, 0o600)
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
	// Waited on, not timed: under load the first hook's shell alone can
	// take seconds to start.
	deadline := time.Now().Add(60 * time.Second)
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
	if n := arrived.Load(); n != 1 {
		t.Fatalf("%d reviewers had started when they were let go; tom's hook should still hold him", n)
	}
	// The turn waits for the hook to let tom through.
	select {
	case err := <-done:
		t.Fatalf("the turn came back (%v) while tom's hook still held him", err)
	case <-time.After(200 * time.Millisecond):
	}
	letThrough()
	// The reviews are never released before the turn is back: a turn
	// that comes back at all has stopped waiting on both.
	select {
	case <-done:
	case <-time.After(30 * time.Second):
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
