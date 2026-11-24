package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localcode/internal/client"
	"localcode/internal/config"
)

// The repeat guard's ceiling has two homes, a number in the settings
// window and a command, and they have to be one setting: moved in
// either, the other hears about it, and a client that just opened reads
// the same value.
func TestRepeatLimitSyncsBetweenTheCommandAndTheSettingsWindow(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	ctx := context.Background()
	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	evCtx, cancelEvents := context.WithCancel(ctx)
	defer cancelEvents()
	evCh, err := c.SubscribeEvents(evCtx, sess.ID, 0)
	if err != nil {
		t.Fatalf("SubscribeEvents: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// The window's endpoint moves it and every client hears.
	status, answered := postSetting(t, httpSrv.URL+"/api/settings/repeat-limit", `{"limit":0}`)
	if status != http.StatusOK {
		t.Fatalf("POST repeat-limit answered %d", status)
	}
	// No config.json in this daemon, so there was nothing to fail to save.
	if answered["repeat_limit"] != float64(0) || answered["applied"] != true || answered["persisted"] != true {
		t.Errorf("POST repeat-limit answered %v, want limit 0 applied", answered)
	}
	if data := waitForSettings(t, evCh); data["repeat_limit"] != float64(0) {
		t.Errorf("repeat_limit = %v after the window turned it off", data["repeat_limit"])
	}
	if d.Loop.RepeatLimit() != 0 {
		t.Errorf("the daemon still has a limit of %d", d.Loop.RepeatLimit())
	}

	// Out of range is refused, not clamped: a window that sent 500 and
	// was told "ok" would show a number the daemon does not hold.
	resp, err := http.Post(httpSrv.URL+"/api/settings/repeat-limit", "application/json",
		strings.NewReader(`{"limit":500}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("limit 500 answered %s, want 400", resp.Status)
	}

	// And GET /api/settings is where a client that just opened reads it.
	var got map[string]any
	r2, err := http.Get(httpSrv.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(r2.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if got["repeat_limit"] != float64(0) {
		t.Errorf("GET /api/settings repeat_limit = %v, want 0", got["repeat_limit"])
	}
}

// The endpoint behind the number in the settings window. The running
// loop changes now, config.json changes so the number is there after a
// restart, and the answer says both.
func TestDaemonSetRepeatLimit(t *testing.T) {
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

	// On at a number that is not the default for "on", then off, then the
	// ceiling: the three sizes of the request.
	for _, limit := range []int{7, 0, config.MaxRepeatLimit} {
		status, answered := postSetting(t, httpSrv.URL+"/api/settings/repeat-limit", fmt.Sprintf(`{"limit":%d}`, limit))
		if status != http.StatusOK {
			t.Fatalf("limit %d answered %d, want 200", limit, status)
		}
		if answered["repeat_limit"] != float64(limit) || answered["applied"] != true || answered["persisted"] != true {
			t.Errorf("limit %d answered %v, want it applied, persisted and reporting %d", limit, answered, limit)
		}
		if _, said := answered["error"]; said {
			t.Errorf("limit %d: a save that worked carries an error: %v", limit, answered["error"])
		}
		if d.Loop.RepeatLimit() != limit {
			t.Errorf("the running loop has a limit of %d after it was set to %d", d.Loop.RepeatLimit(), limit)
		}
		saved, err := config.LoadFile(cfgPath)
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if saved.RepeatLimitSteps == nil || *saved.RepeatLimitSteps != limit {
			t.Errorf("config.json has repeat_limit %v after it was set to %d", saved.RepeatLimitSteps, limit)
		}
		// Written key by key, so an unrelated setting is still there.
		if saved.ShowTPS == nil || *saved.ShowTPS {
			t.Error("writing repeat_limit clobbered show_tps")
		}
	}
}

// A save that fails is still a change that was made. The route used to
// answer 500 for it, which the settings window read as "not changed"
// while the daemon went on with the new ceiling.
func TestRepeatLimitSaysAppliedWhenTheSaveFails(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	// A directory where the file should be: every write fails.
	d.Broker.ConfigPath = t.TempDir()
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	status, answered := postSetting(t, httpSrv.URL+"/api/settings/repeat-limit", `{"limit":6}`)
	if status != http.StatusOK {
		t.Fatalf("answered %d %v, want 200: the change was applied", status, answered)
	}
	if answered["repeat_limit"] != float64(6) || answered["applied"] != true {
		t.Errorf("answered %v, want the change reported as applied", answered)
	}
	if answered["persisted"] != false {
		t.Errorf("answered %v, which claims a save that could not have happened", answered)
	}
	if msg, _ := answered["error"].(string); !strings.Contains(msg, "failed to persist") {
		t.Errorf("error = %q, which does not say the save failed", msg)
	}
	if d.Loop.RepeatLimit() != 6 {
		t.Errorf("the daemon has a limit of %d, not the 6 it just reported as applied", d.Loop.RepeatLimit())
	}
	// And a client that opens now reads the applied value, not the saved one.
	if got := readSettings(t, httpSrv.URL); got["repeat_limit"] != float64(6) {
		t.Errorf("GET /api/settings repeat_limit = %v after it was set to 6", got["repeat_limit"])
	}

	// The refusals are still refusals, and are told apart from the answer
	// above by their status: nothing is applied, and the save is never
	// reached, so the config that cannot be written changes nothing.
	for _, body := range []string{`{"limit":51}`, `{"limit":-1}`, `not json`} {
		status, answered := postSetting(t, httpSrv.URL+"/api/settings/repeat-limit", body)
		if status != http.StatusBadRequest {
			t.Errorf("%s answered %d %v, want 400", body, status, answered)
		}
		if d.Loop.RepeatLimit() != 6 {
			t.Errorf("%s was refused and moved the limit to %d anyway", body, d.Loop.RepeatLimit())
		}
	}
}
