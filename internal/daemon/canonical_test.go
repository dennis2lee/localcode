package daemon

import (
	"net/http"
	"strings"
	"testing"
)

// A write to a path with a "." or ".." segment was redirected by the mux
// to the cleaned path, and the client followed with the same method:
// DELETE /api/sessions/{id}/tasks/.. deleted the conversation. (Found by
// review.)
func TestAWriteToANonCanonicalPathIsRefusedNotRedirected(t *testing.T) {
	d, srv, done := plainDaemon(t)
	defer done()
	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		"/api/sessions/parent/tasks/..",
		"/api/sessions/parent/tasks/./..",
		"/api/sessions/parent/schedules/..",
		"/api/sessions//parent",
	} {
		if code, body := deleteRequest(t, srv.URL+p); code != http.StatusBadRequest {
			t.Errorf("DELETE %s: status %d (%s), want 400", p, code, body)
		}
	}
	if _, err := d.Loop.Store.Get("parent"); err != nil {
		t.Fatal("a request aimed at a task deleted the conversation")
	}

	// An escaped dot is a segment, not a step up: it reaches the task
	// route, which has no such row.
	if code, _ := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks/%2e%2e"); code != http.StatusNotFound {
		t.Errorf("DELETE .../tasks/%%2e%%2e: status %d, want 404", code)
	}
	// A read is redirected as it always was, because a redirected read is
	// the same read.
	resp, err := http.Get(srv.URL + "/api/sessions/parent/tasks/..")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasSuffix(resp.Request.URL.Path, "/api/sessions/parent") {
		t.Errorf("GET was answered %d at %s, want the redirect followed to the session", resp.StatusCode, resp.Request.URL.Path)
	}
	if _, err := d.Loop.Store.Get("parent"); err != nil {
		t.Fatal("the conversation is gone")
	}
}

func TestCleanPathIsTheMuxRule(t *testing.T) {
	for in, want := range map[string]string{
		"":                         "/",
		"/api/sessions":            "/api/sessions",
		"/api/sessions/":           "/api/sessions/",
		"/api/sessions/x/tasks/..": "/api/sessions/x",
		"/api//sessions":           "/api/sessions",
		"/api/./sessions/":         "/api/sessions/",
		"/api/sessions/%2e%2e":     "/api/sessions/%2e%2e",
	} {
		if got := cleanPath(in); got != want {
			t.Errorf("cleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}
