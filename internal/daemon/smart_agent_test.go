package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/trace"
)

// The endpoint behind the settings panel's Smart Agent switch. Same
// contract as the other live settings: the running loop changes now, and
// config.json changes so the choice survives a restart.
func TestDaemonSetSmartAgent(t *testing.T) {
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

	// The default the whole feature rests on. A build that shipped with
	// this on would be spending several model calls per request for
	// everyone who never asked for it.
	if d.Loop.SmartAgentEnabled() {
		t.Fatal("Smart Agent started on, so this test would prove nothing")
	}

	post := func(enabled bool) {
		t.Helper()
		body := strings.NewReader(fmt.Sprintf(`{"enabled":%t}`, enabled))
		resp, err := http.Post(httpSrv.URL+"/api/settings/smart-agent", "application/json", body)
		if err != nil {
			t.Fatalf("POST smart-agent: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST smart-agent status = %d, want 200", resp.StatusCode)
		}
		var got struct {
			SmartAgent bool `json:"smart_agent"`
			Applied    bool `json:"applied"`
			Persisted  bool `json:"persisted"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode smart-agent response: %v", err)
		}
		if got.SmartAgent != enabled || !got.Applied || !got.Persisted {
			t.Fatalf("POST smart-agent body = %+v, want it applied, persisted and reporting %t", got, enabled)
		}
	}

	post(true)
	if !d.Loop.SmartAgentEnabled() {
		t.Error("the running loop still reports Smart Agent off after turning it on")
	}
	saved, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if !saved.SmartAgentEnabled() {
		t.Error("config.json does not have smart_agent on, so the choice would not survive a restart")
	}
	// Written key by key rather than by rewriting the file, so an
	// unrelated setting is still there afterwards.
	if saved.ShowTPS == nil || *saved.ShowTPS {
		t.Error("writing smart_agent clobbered show_tps")
	}

	post(false)
	if d.Loop.SmartAgentEnabled() {
		t.Error("the running loop still reports Smart Agent on after turning it off")
	}
	saved, err = config.LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if saved.SmartAgentEnabled() {
		t.Error("config.json still has smart_agent on")
	}
}

// A client that has just opened needs the switch's position and the names
// of what it turns on, without waiting for anything to change.
func TestGetSettingsReportsSmartAgent(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	get := func() map[string]any {
		t.Helper()
		resp, err := http.Get(httpSrv.URL + "/api/settings")
		if err != nil {
			t.Fatalf("GET settings: %v", err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode settings: %v", err)
		}
		return out
	}

	s := get()
	if s["smart_agent"] != false {
		t.Errorf("smart_agent = %v, want false", s["smart_agent"])
	}
	roster, _ := s["smart_agent_roster"].([]any)
	if len(roster) == 0 {
		t.Error("the roster is empty, so a client cannot say what the switch adds")
	}

	d.Loop.SetSmartAgentEnabled(true)
	if s := get(); s["smart_agent"] != true {
		t.Errorf("smart_agent = %v after turning it on, want true", s["smart_agent"])
	}
}

// The turn log, over HTTP. The file is the record that lasts; this is for
// the question asked while a session is still open.
func TestTraceEndpointReportsWhetherItIsRecording(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	get := func() map[string]any {
		t.Helper()
		resp, err := http.Get(httpSrv.URL + "/api/trace")
		if err != nil {
			t.Fatalf("GET trace: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	// Nothing is recorded with the feature off, which is a setting rather
	// than a failure: 200 with no records, not an error.
	if s := get(); s["enabled"] != false {
		t.Errorf("enabled = %v with Smart Agent off, want false", s["enabled"])
	}

	w, err := trace.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open trace: %v", err)
	}
	defer w.Close()
	d.Loop.Trace = w
	d.Loop.SetSmartAgentEnabled(true)
	w.Write(trace.Record{TraceID: "t", Span: trace.SpanTurnStart, SessionID: "s1"})

	got := get()
	if got["enabled"] != true {
		t.Errorf("enabled = %v with Smart Agent on and a writer, want true", got["enabled"])
	}
	records, _ := got["records"].([]any)
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if first := records[0].(map[string]any); first["span"] != trace.SpanTurnStart {
		t.Errorf("record = %v", first)
	}
}

// SA6. Persisting and applying are two different outcomes, and answering
// with an HTTP error for a failed persist told every client the change had
// not happened when it had. This switch decides which model answers and
// which tools an agent may call, so a client showing the wrong one is the
// worst thing it can do; showing an unsaved one is merely inconvenient.
func TestDaemonSetSmartAgentReportsAppliedSeparatelyFromPersisted(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	// A directory where the config file should be: writing it fails, and
	// nothing in the process is left in a strange state.
	dir := t.TempDir()
	unwritable := filepath.Join(dir, "config.json")
	if err := os.Mkdir(unwritable, 0o755); err != nil {
		t.Fatal(err)
	}
	d.Broker.ConfigPath = unwritable

	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	resp, err := http.Post(httpSrv.URL+"/api/settings/smart-agent", "application/json",
		strings.NewReader(`{"enabled":true}`))
	if err != nil {
		t.Fatalf("POST smart-agent: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the change was applied", resp.StatusCode)
	}
	var got struct {
		SmartAgent bool   `json:"smart_agent"`
		Applied    bool   `json:"applied"`
		Persisted  bool   `json:"persisted"`
		Error      string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Applied || !got.SmartAgent {
		t.Errorf("body = %+v, want it to report the change as applied", got)
	}
	if got.Persisted {
		t.Error("body claims the change was persisted, but config.json could not be written")
	}
	if got.Error == "" {
		t.Error("nothing in the body says why it was not saved")
	}
	if !d.Loop.SmartAgentEnabled() {
		t.Error("the daemon is not running the state it just reported as applied")
	}
}
