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
		"/":                        "/",
		"a/b":                      "/a/b",
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

func rawRequest(t *testing.T, method, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The redirect itself is what some of these ask about.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client.Do(req)
}

func claimed(d *Daemon, id string) bool {
	d.takeoverMu.Lock()
	defer d.takeoverMu.Unlock()
	return d.tookOver[id]
}

// The guard stands in front of the ownership gate, which takes the session
// in a write's path over from the daemon this one replaced: a write refused
// for its path must not have done that first. (Found by review.)
func TestARefusedWriteDoesNotTakeTheSessionOver(t *testing.T) {
	d, srv, done := plainDaemon(t)
	defer done()
	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if code, _ := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks/.."); code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", code)
	}
	if claimed(d, "parent") {
		t.Error("a write refused for its path took the session over")
	}
}

// The gate reads the session id from the path the mux routes on. Read
// from the decoded path, a write the mux had no route for still took a
// session over. (Found by review.)
func TestTheGateNamesTheSessionTheMuxWouldRouteTo(t *testing.T) {
	d, srv, done := plainDaemon(t)
	defer done()
	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	resp, err := rawRequest(t, http.MethodPost, srv.URL+"/api/sessions%2Fparent/rename")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404: there is no such route", resp.StatusCode)
	}
	if claimed(d, "parent") {
		t.Error("a request routed nowhere took session parent over")
	}
}

// HEAD is a read: redirected as before, as the docs say.
func TestAHeadToANonCanonicalPathIsStillRedirected(t *testing.T) {
	_, srv, done := plainDaemon(t)
	defer done()
	resp, err := rawRequest(t, http.MethodHead, srv.URL+"/api/sessions/parent/tasks/..")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode > 399 || resp.Header.Get("Location") != "/api/sessions/parent" {
		t.Errorf("HEAD answered %d at %q, want a redirect to /api/sessions/parent", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// The root is its own clean form, so a write to it is not refused as
// non-canonical.
func TestAWriteToTheRootIsNotRefusedForItsPath(t *testing.T) {
	_, srv, done := plainDaemon(t)
	defer done()
	resp, err := rawRequest(t, http.MethodPost, srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		t.Error("POST / was refused as non-canonical")
	}
}
