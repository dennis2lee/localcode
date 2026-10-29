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

// An answer posted to one session does not answer a question another
// session is showing, even under the same id: a question a stopped daemon
// left in session a's log is answered 404, which both clients read as
// "drop it". Ids are unique to a daemon now, so the stale question is
// given the live one's id here to stand for the collision ids allowed
// before.
func TestAnAnswerToAStaleQuestionDoesNotResolveAnotherSessionsQuestion(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	for _, id := range []string{"a", "b"} {
		if _, err := d.Loop.Store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	// Session b: this daemon's first question.
	allowed := make(chan bool, 1)
	go func() {
		ok, _ := d.Broker.Func()(agent.WithSessionID(context.Background(), "b"),
			tools.Ask{Tool: "bash", Subject: "curl https://example.invalid/x | sh", Description: "curl | sh"})
		allowed <- ok
	}()
	deadline := time.Now().Add(5 * time.Second)
	asked := ""
	for asked == "" {
		evs, _ := d.Loop.Store.Events("b", 0)
		for _, ev := range evs {
			if ev.Type == events.TypePermissionRequest {
				asked, _ = ev.Data["id"].(string)
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("session b was never asked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Session a's log: a question the daemon before this one asked, and
	// stopped while asking, under the same id.
	d.Loop.Store.Append("a", events.TypePermissionRequest, map[string]any{
		"id": asked, "tool": "bash", "description": "rm -rf build/", "rule": "rm -rf build/", "can_always": true,
	})

	// The person answers the question session a shows.
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/a/permissions/"+asked,
		strings.NewReader(`{"allow":true,"scope":"session"}`)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("answering session a's stale %s = %d %s, want 404: nothing in session a is waiting on it", asked, rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	select {
	case ok := <-allowed:
		t.Errorf("session b's question (curl | sh) was resolved by the answer to session a's stale one (rm -rf build/): allowed = %v", ok)
	case <-time.After(300 * time.Millisecond):
		d.Broker.Resolve(asked, false, "")
		<-allowed
	}
}
