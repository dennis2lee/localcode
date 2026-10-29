package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localcode/internal/events"
)

func TestAnAnswerToAStaleInputDoesNotResolveAnotherSessionsQuestion(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	for _, id := range []string{"a", "b"} {
		if _, err := d.Loop.Store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() { answer, _ := d.Loop.Input.Ask(ctx, "b", "Which store?", nil); done <- answer }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	asked := ""
	for asked == "" {
		evs, err := d.Loop.Store.Events("b", 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if ev.Type == events.TypeInputRequest {
				asked, _ = ev.Data["id"].(string)
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("session b was never asked")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := d.Loop.Store.Append("a", events.TypeInputRequest, map[string]any{"id": asked, "question": "Which account?"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/a/input/"+asked, strings.NewReader(`{"answer":"wrong answer"}`)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("stale input answer = %d %s, want 404", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	// The legitimate answer must still be accepted after the stale post.
	rec = httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/b/input/"+asked, strings.NewReader(`{"answer":"right answer"}`)))
	if rec.Code != http.StatusOK {
		t.Errorf("live input answer = %d %s, want 200", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	select {
	case answer := <-done:
		done <- answer
		if answer != "right answer" {
			t.Errorf("session b received %q, want right answer", answer)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session b never received its answer")
	}
}
