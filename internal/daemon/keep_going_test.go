package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/config"
)

// postSetting posts a body to a settings route and returns the status
// with the JSON object the daemon answered, or nil when the answer was
// not one: a refusal here is a line of text.
func postSetting(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the answer from %s: %v", url, err)
	}
	var answered map[string]any
	if err := json.Unmarshal(raw, &answered); err != nil {
		return resp.StatusCode, nil
	}
	// The page reads a body only when it is labelled as JSON, and reads
	// an answer with no body as a change that was applied and saved.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("POST %s answered JSON as %q, which the page does not read", url, ct)
	}
	return resp.StatusCode, answered
}

// readSettings is what a client that has just opened reads.
func readSettings(t *testing.T, base string) map[string]any {
	t.Helper()
	resp, err := http.Get(base + "/api/settings")
	if err != nil {
		t.Fatalf("GET settings: %v", err)
	}
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	return got
}

// The endpoint behind the settings window's keep-going checkbox. The
// running loop changes now, config.json changes so the choice is there
// after a restart, and the answer says both.
func TestDaemonSetKeepGoing(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"show_tps":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d.Broker.ConfigPath = cfgPath
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	if !d.Loop.KeepGoingEnabled() {
		t.Fatal("keep-going started off, so turning it off would prove nothing")
	}

	for _, enabled := range []bool{false, true} {
		body := `{"enabled":false}`
		if enabled {
			body = `{"enabled":true}`
		}
		status, answered := postSetting(t, httpSrv.URL+"/api/settings/keep-going", body)
		if status != http.StatusOK {
			t.Fatalf("enabled=%t answered %d, want 200", enabled, status)
		}
		if answered["keep_going"] != enabled || answered["applied"] != true || answered["persisted"] != true {
			t.Errorf("enabled=%t answered %v, want it applied, persisted and reporting %t", enabled, answered, enabled)
		}
		if _, said := answered["error"]; said {
			t.Errorf("enabled=%t: a save that worked carries an error: %v", enabled, answered["error"])
		}
		if d.Loop.KeepGoingEnabled() != enabled {
			t.Errorf("the running loop has keep-going %t after it was set to %t", d.Loop.KeepGoingEnabled(), enabled)
		}
		saved, err := config.LoadFile(cfgPath)
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if saved.KeepGoing() != enabled {
			t.Errorf("config.json has keep_going %t after it was set to %t", saved.KeepGoing(), enabled)
		}
		if saved.KeepGoingEnabled == nil {
			t.Error("config.json has no keep_going key, so the test read a default and not what was written")
		}
		// Written key by key, so an unrelated setting is still there.
		if saved.ShowTPS == nil || *saved.ShowTPS {
			t.Error("writing keep_going clobbered show_tps")
		}
	}
}

// A save that fails is still a change that was made. The route used to
// answer 500 for it, which the settings window read as "not changed": it
// put the box back while the daemon went on with the new value.
func TestKeepGoingSaysAppliedWhenTheSaveFails(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	// A directory where the file should be: every write fails.
	d.Broker.ConfigPath = t.TempDir()
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	status, answered := postSetting(t, httpSrv.URL+"/api/settings/keep-going", `{"enabled":false}`)
	if status != http.StatusOK {
		t.Fatalf("answered %d %v, want 200: the change was applied", status, answered)
	}
	if answered["keep_going"] != false || answered["applied"] != true {
		t.Errorf("answered %v, want the change reported as applied", answered)
	}
	if answered["persisted"] != false {
		t.Errorf("answered %v, which claims a save that could not have happened", answered)
	}
	if msg, _ := answered["error"].(string); !strings.Contains(msg, "failed to persist") {
		t.Errorf("error = %q, which does not say the save failed", msg)
	}
	if d.Loop.KeepGoingEnabled() {
		t.Error("the daemon is not running the state it just reported as applied")
	}
	// And a client that opens now reads the applied value, not the saved one.
	if got := readSettings(t, httpSrv.URL); got["keep_going"] != false {
		t.Errorf("GET /api/settings keep_going = %v after it was turned off", got["keep_going"])
	}
}

// A body the endpoint cannot read is the one answer that means nothing
// changed, and it stays an error status so a client can tell the two
// apart.
func TestSetKeepGoingRefusesAJunkBody(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	// With a config that cannot be written, so a refusal that reached the
	// save would show as a different answer.
	d.Broker.ConfigPath = t.TempDir()
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	status, answered := postSetting(t, httpSrv.URL+"/api/settings/keep-going", `not json`)
	if status != http.StatusBadRequest {
		t.Errorf("answered %d %v, want 400", status, answered)
	}
	if !d.Loop.KeepGoingEnabled() {
		t.Error("a rejected request changed the switch anyway")
	}
}
