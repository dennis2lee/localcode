package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Archiving, at the daemon.
//
// The two endpoints are easy; what these are about is the refusals, and in
// particular that they are 403 and never 409. Both clients key on the
// status code alone: a 409 means "a turn is running" to them, and both
// answer it by queueing the prompt and waiting for a turn.done that an
// archived conversation will never produce. That defect is already in this
// changelog once.

func post(t *testing.T, srv *httptest.Server, path string, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func archiveDaemon(t *testing.T) (*Daemon, *httptest.Server) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(model.Close)
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	return d, srv
}

func TestArchiveAndRetrieveMoveASessionBetweenTheLists(t *testing.T) {
	d, srv := archiveDaemon(t)
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	list := func(archived bool) []map[string]any {
		t.Helper()
		url := srv.URL + "/api/sessions"
		if archived {
			url += "?archived=1"
		}
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out []map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	if len(list(false)) != 1 || len(list(true)) != 0 {
		t.Fatalf("before: active %d archived %d", len(list(false)), len(list(true)))
	}

	if resp := post(t, srv, "/api/sessions/s1/archive", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("archive status = %d", resp.StatusCode)
	}
	if len(list(false)) != 0 || len(list(true)) != 1 {
		t.Errorf("after archive: active %d archived %d", len(list(false)), len(list(true)))
	}

	if resp := post(t, srv, "/api/sessions/s1/retrieve", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("retrieve status = %d", resp.StatusCode)
	}
	if len(list(false)) != 1 || len(list(true)) != 0 {
		t.Errorf("after retrieve: active %d archived %d", len(list(false)), len(list(true)))
	}
}

// The status code is the contract. 403, never 409.
func TestAnArchivedSessionRefusesWorkWith403(t *testing.T) {
	d, srv := archiveDaemon(t)
	d.Loop.Store.CreateSession("s1", "", "general-purpose", true)
	if _, err := d.Loop.Store.Archive("s1"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ path, body string }{
		{"/api/sessions/s1/messages", `{"text":"hello"}`},
		{"/api/sessions/s1/tasks", `{"agent":"general-purpose","prompt":"go"}`},
		{"/api/sessions/s1/agent", `{"agent":"general-purpose"}`},
	} {
		// Booking a schedule is refused too, at Scheduler.Add rather than
		// here: this daemon has no scheduler, so the endpoint answers 404
		// before it reaches the check. See internal/agent.
		resp := post(t, srv, tc.path, tc.body)
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusConflict {
			t.Errorf("%s answered 409, which both clients read as busy and queue behind forever", tc.path)
			continue
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403", tc.path, resp.StatusCode)
		}
	}
}

// Reading is the whole point of keeping it.
func TestAnArchivedSessionIsStillReadable(t *testing.T) {
	d, srv := archiveDaemon(t)
	d.Loop.Store.CreateSession("s1", "", "general-purpose", true)
	d.Loop.Store.Append("s1", "message.user", map[string]any{"text": "hello"})
	d.Loop.Store.Archive("s1")

	resp, err := http.Get(srv.URL + "/api/sessions/s1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET session status = %d, want 200: reading the transcript is why it was kept", resp.StatusCode)
	}
}

// Claimed, not checked. The turn slot is taken in one step, so nothing can
// start a turn between deciding and archiving.
func TestArchivingIsRefusedWhileATurnIsRunning(t *testing.T) {
	d, srv := archiveDaemon(t)
	d.Loop.Store.CreateSession("s1", "", "general-purpose", true)

	if !d.turns.begin("s1", func() {}) {
		t.Fatal("could not fake a running turn")
	}
	defer d.turns.end("s1")

	resp := post(t, srv, "/api/sessions/s1/archive", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409: a turn was running", resp.StatusCode)
	}
	if sess, _ := d.Loop.Store.Get("s1"); sess.ArchivedAt != nil {
		t.Error("the session was archived out from under a running turn")
	}
}

// The slot an archive holds refuses injection, so a message arriving
// mid-archive is answered rather than queued for a turn that will never
// exist. Without this it becomes a 409, which is the trap above.
func TestAMessageArrivingDuringAnArchiveIsNotQueued(t *testing.T) {
	d, srv := archiveDaemon(t)
	d.Loop.Store.CreateSession("s1", "", "general-purpose", true)

	if !d.turns.beginExclusive("s1", func() {}) {
		t.Fatal("could not take the exclusive slot")
	}
	// The archive has the slot and has written the flag; this is the
	// window a client that has not refreshed sends into.
	d.Loop.Store.Archive("s1")
	defer d.turns.end("s1")

	resp := post(t, srv, "/api/sessions/s1/messages", `{"text":"hello"}`)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		t.Error("the message was accepted into a conversation being archived")
	}
	if resp.StatusCode == http.StatusConflict {
		t.Error("answered 409, so the client queues it and waits for a turn that cannot happen")
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestArchivingAnUnknownSessionIs404(t *testing.T) {
	_, srv := archiveDaemon(t)
	resp := post(t, srv, "/api/sessions/nope/archive", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
