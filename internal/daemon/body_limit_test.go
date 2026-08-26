package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every JSON handler decoded the body with no size limit, while uploads
// capped theirs — an inconsistency rather than a decision.
// It matters because --listen can bind something other than loopback: a
// message with a huge "text" was allocated in full before the empty check
// and, on success, written into the session log.
func TestJSONHandlersRefuseAnOversizedBody(t *testing.T) {
	model := mockModelServer(t, t.TempDir()+"/out.txt")
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Comfortably past the cap, and far past any real prompt.
	huge := `{"text":"` + strings.Repeat("a", 4<<20) + `"}`
	for _, path := range []string{
		"/api/sessions",
		"/api/workspace",
	} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(huge))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("POST %s accepted a %d MB body: %d", path, len(huge)>>20, resp.StatusCode)
		}
	}
}

// An ordinary body must still go through untouched — a cap that rejected
// real prompts would be worse than no cap.
func TestJSONHandlersStillAcceptAnOrdinaryBody(t *testing.T) {
	model := mockModelServer(t, t.TempDir()+"/out.txt")
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/sessions", "application/json",
		strings.NewReader(`{"agent":"general-purpose"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("creating a session returned %d", resp.StatusCode)
	}
}

// Answering a permission id nobody is waiting on used to return 200
// {"status":"resolved"}. A client resolving a stale prompt — replayed
// from the log, or answered a moment earlier by a second client — was
// told it worked, and went on believing the turn it was watching had been
// unblocked.
func TestResolvingAnUnknownPermissionIsNotReportedAsSuccess(t *testing.T) {
	model := mockModelServer(t, t.TempDir()+"/out.txt")
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/sessions/s-nope/permissions/p999",
		"application/json", strings.NewReader(`{"allow":true,"scope":"once"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Errorf("resolving an unknown permission returned %d", resp.StatusCode)
	}
}
