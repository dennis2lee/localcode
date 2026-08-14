package daemon

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// With a Manager present and no model configured — which is now every
// daemon that has not been pointed at a model — the status has to carry
// the Manager's own reason rather than a fixed string about the build.
func TestDictationStatusAsksTheManagerForTheReason(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	d.Dictation = dictation.NewManager(dictation.Config{})
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
		t.Error("ready = true with no model configured")
	}
	wantReady, want := d.Dictation.Ready()
	if wantReady {
		t.Fatal("test premise broken: the manager considers itself ready")
	}
	if got.Detail != want {
		t.Errorf("detail = %q, want the manager's own reason %q", got.Detail, want)
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

// The settings endpoint changes the live manager and persists to
// config.json, in that order: the setting taking effect is what the user
// asked for, and a daemon started without a config.json can still be
// configured for as long as it runs.
func TestSetDictationAppliesAndPersists(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"dictation":{"threads":3}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{Dictation: dictation.NewManager(dictation.Config{}), ConfigPath: cfgPath}
	rec := httptest.NewRecorder()
	d.handleSetDictation(rec, httptest.NewRequest("POST", "/api/dictation/settings",
		strings.NewReader(`{"language":"ko","whisper_url":"http://box:8080"}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["save_error"] != "" {
		t.Errorf("save_error = %v", got["save_error"])
	}
	if got["remote"] != true {
		t.Errorf("remote = %v, want true for a configured URL", got["remote"])
	}

	// Applied to the live manager.
	if live := d.Dictation.Config(); live.Language != "ko" || live.WhisperURL != "http://box:8080" {
		t.Errorf("live config = %+v", live)
	}

	// Persisted, and without trampling the key this build was not asked
	// to change.
	raw, _ := os.ReadFile(cfgPath)
	var onDisk struct {
		Dictation map[string]any `json:"dictation"`
	}
	json.Unmarshal(raw, &onDisk)
	if onDisk.Dictation["language"] != "ko" {
		t.Errorf("language not written: %s", raw)
	}
	if onDisk.Dictation["whisper_url"] != "http://box:8080" {
		t.Errorf("whisper_url not written: %s", raw)
	}
	if onDisk.Dictation["threads"] != float64(3) {
		t.Errorf("an untouched key was lost: %s", raw)
	}
}

// A nonsense address is refused at the panel, where the person can still
// see what they typed — not at the first attempt to dictate.
func TestSetDictationRejectsAnUnusableAddress(t *testing.T) {
	d := &Daemon{Dictation: dictation.NewManager(dictation.Config{})}
	rec := httptest.NewRecorder()
	d.handleSetDictation(rec, httptest.NewRequest("POST", "/api/dictation/settings",
		strings.NewReader(`{"whisper_url":"http://"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
	}
	if d.Dictation.Config().WhisperURL != "" {
		t.Error("the rejected address was applied anyway")
	}
}

// Clearing the URL goes back to running locally, and the empty value has
// to reach the file — leaving the old one there would make the panel
// appear not to have saved.
func TestClearingTheURLIsWritten(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{"dictation":{"whisper_url":"http://old:8080"}}`), 0o644)

	d := &Daemon{Dictation: dictation.NewManager(dictation.Config{WhisperURL: "http://old:8080"}), ConfigPath: cfgPath}
	rec := httptest.NewRecorder()
	d.handleSetDictation(rec, httptest.NewRequest("POST", "/api/dictation/settings",
		strings.NewReader(`{"language":"","whisper_url":""}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if d.Dictation.Config().WhisperURL != "" {
		t.Error("the URL was not cleared on the live manager")
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), `"whisper_url": ""`) && !strings.Contains(string(raw), `"whisper_url":""`) {
		t.Errorf("the cleared URL was not written: %s", raw)
	}
}

// The dialect a remote server speaks is a setting like the others: it is
// discovered when unset, and naming it is the answer for a server that
// discovery cannot work out.
func TestSetDictationPersistsTheAPIDialect(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{Dictation: dictation.NewManager(dictation.Config{}), ConfigPath: cfgPath}
	rec := httptest.NewRecorder()
	d.handleSetDictation(rec, httptest.NewRequest("POST", "/api/dictation/settings",
		strings.NewReader(`{"whisper_url":"box:9000","whisper_api":"whisperx"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if live := d.Dictation.Config(); live.WhisperAPI != "whisperx" {
		t.Errorf("live whisper_api = %q, want whisperx", live.WhisperAPI)
	}
	raw, _ := os.ReadFile(cfgPath)
	var onDisk struct {
		Dictation map[string]any `json:"dictation"`
	}
	json.Unmarshal(raw, &onDisk)
	if onDisk.Dictation["whisper_api"] != "whisperx" {
		t.Errorf("whisper_api not written: %s", raw)
	}
}

// A dialect nobody speaks is refused where it was typed, rather than
// accepted and then failing at the first utterance with an error about
// something else.
func TestSetDictationRejectsAnUnknownAPIDialect(t *testing.T) {
	d := &Daemon{Dictation: dictation.NewManager(dictation.Config{})}
	rec := httptest.NewRecorder()
	d.handleSetDictation(rec, httptest.NewRequest("POST", "/api/dictation/settings",
		strings.NewReader(`{"whisper_url":"box:9000","whisper_api":"nonsense"}`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
	}
	if d.Dictation.Config().WhisperAPI != "" {
		t.Error("the refused dialect was applied anyway")
	}
}
