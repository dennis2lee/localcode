package daemon

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/dictation"
)

// A daemon with no dictation configured must explain itself rather than
// look broken: the microphone button is hidden on the strength of this
// answer, and "not ready, because X" is what makes that a decision
// instead of a mystery.
func TestDictationStatusWithoutAManager(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/dictation")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var got struct {
		Ready  bool   `json:"ready"`
		Detail string `json:"detail"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Ready {
		t.Error("ready = true with no dictation configured")
	}
	if got.Detail == "" {
		t.Error("no detail explaining why dictation is unavailable")
	}
}

// Starting a dictation with no model must be a 503 with the reason, not
// a 500: the daemon is fine, the model is absent, and the difference is
// what the person reading the message needs.
func TestDictationStartWithoutAModelExplains(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	d.Dictation = dictation.NewManager(dictation.Config{ModelDir: t.TempDir()})
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/dictation", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	// Either the build has no recognizer at all, or it has one and the
	// model directory is empty — both are worth saying out loud.
	if body["error"] == "" {
		t.Error("no error message explaining the failure")
	}
}

// Audio for a session that does not exist is a 404 that says the session
// may have been reaped, rather than a silent nothing — an abandoned tab
// gets its recognizer closed, and a client that comes back needs to know
// to start a new one.
func TestDictationAudioForAnUnknownSession(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	d.Dictation = dictation.NewManager(dictation.Config{ModelDir: t.TempDir()})
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	pcm := make([]byte, 8)
	binary.LittleEndian.PutUint16(pcm, 1)
	resp, err := http.Post(srv.URL+"/api/dictation/nope/audio", "application/octet-stream", strings.NewReader(string(pcm)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// The manager reports readiness the same way whether the build lacks a
// recognizer or the model is missing, and either way it is a reason, not
// a bare false.
func TestManagerReadyExplainsWhyNot(t *testing.T) {
	m := dictation.NewManager(dictation.Config{ModelDir: t.TempDir()})
	defer m.Close()
	ready, detail := m.Ready()
	if ready {
		t.Error("ready with an empty model directory")
	}
	if detail == "" {
		t.Error("no reason given")
	}
	_ = context.Background()
}
