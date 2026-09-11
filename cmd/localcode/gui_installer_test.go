package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The window's installed copy, and why the update button vanished.
//
// After a startup handoff the window serves a proxy to the successor,
// and the successor is the staged copy — which is current by
// construction. So "is there an update?" reached a process that was
// always up to date and always answered no, while on Windows the copy
// the shortcut starts, under Program Files, stayed on whatever version
// the MSI put there. The reply that created the situation promised the
// opposite, and the button that was supposed to fix it was hidden by the
// same answer that hid the problem.

// A window that kept an installer answers the update question itself.
func TestTheUpdateCheckIsAnsweredByTheWindowNotTheSuccessor(t *testing.T) {
	successor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/update" {
			// The staged copy, current by construction. This is the
			// answer that used to reach the page.
			writeJSON(w, map[string]any{"current": "9.9.9", "available": false})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer successor.Close()

	installer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"current": "1.0.0", "latest": "9.9.9", "available": true, "can_install": true})
	})

	front := httptest.NewServer(successorProxy(strings.TrimPrefix(successor.URL, "http://"), installer))
	defer front.Close()

	resp, err := http.Get(front.URL + "/api/update")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["current"] != "1.0.0" {
		t.Errorf("current = %v, want the window's own version rather than the successor's", body["current"])
	}
	if body["available"] != true {
		t.Error("the update was reported as unavailable, which is what hid it")
	}
}

// And the install button reaches that same process, because it is the
// only one able to replace the installed copy.
func TestTheInstallButtonReachesTheWindow(t *testing.T) {
	successor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the install reached the successor at %s, which cannot replace the installed copy", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer successor.Close()

	reached := false
	installer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		writeJSON(w, map[string]any{"version": "9.9.9", "replaced": true})
	})

	front := httptest.NewServer(successorProxy(strings.TrimPrefix(successor.URL, "http://"), installer))
	defer front.Close()

	resp, err := http.Post(front.URL+"/api/update/install", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if !reached {
		t.Error("the install never reached the window's own daemon")
	}
}

// With nothing kept — the mid-session handoff, where the daemon being
// retired is the one that just installed the release — the successor's
// answer stands rather than none at all.
func TestWithNoInstallerTheSuccessorStillAnswers(t *testing.T) {
	successor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"current": "9.9.9", "available": false})
	}))
	defer successor.Close()

	front := httptest.NewServer(successorProxy(strings.TrimPrefix(successor.URL, "http://"), nil))
	defer front.Close()

	resp, err := http.Get(front.URL + "/api/update")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["current"] != "9.9.9" {
		t.Errorf("current = %v, want the successor's answer when nothing was kept", body["current"])
	}
}
