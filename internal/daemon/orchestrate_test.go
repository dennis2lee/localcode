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
)

// The endpoint behind the settings panel's Orchestration switch.
//
// Its own switch rather than a line inside Smart Agent, because the two
// are different sizes: Smart Agent lets the model hand one question to a
// specialist, this lets it commit to a shape and spend up to thirty-two
// agent turns on it. So the two have to move independently, and the panel
// has to be able to show one on and the other off.

func TestDaemonSetOrchestrate(t *testing.T) {
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

	// A run is the most expensive single thing localcode can be asked to
	// do. A build that shipped with this on would be spending it for
	// everyone who never asked.
	if d.Loop.OrchestrateEnabled() {
		t.Fatal("orchestration started on, so this test would prove nothing")
	}

	post := func(enabled bool) {
		t.Helper()
		body := strings.NewReader(fmt.Sprintf(`{"enabled":%t}`, enabled))
		resp, err := http.Post(httpSrv.URL+"/api/settings/orchestrate", "application/json", body)
		if err != nil {
			t.Fatalf("POST orchestrate: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST orchestrate status = %d, want 200", resp.StatusCode)
		}
		var got struct {
			Orchestrate bool `json:"orchestrate"`
			Applied     bool `json:"applied"`
			Persisted   bool `json:"persisted"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Orchestrate != enabled || !got.Applied || !got.Persisted {
			t.Fatalf("response = %+v, want orchestrate=%t applied and persisted", got, enabled)
		}
	}

	post(true)
	if !d.Loop.OrchestrateEnabled() {
		t.Error("the running daemon did not change")
	}
	// Saved as a top-level boolean. The shape matters: a nested object
	// written here would be a config.json that no later load can decode.
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(saved, &raw); err != nil {
		t.Fatalf("the config we wrote no longer parses: %v\n%s", err, saved)
	}
	if raw["orchestrate"] != true {
		t.Errorf("config.json has orchestrate=%v, want true:\n%s", raw["orchestrate"], saved)
	}
	if raw["show_tps"] != false {
		t.Error("writing the switch dropped a key that was already in the file")
	}

	post(false)
	if d.Loop.OrchestrateEnabled() {
		t.Error("turning it off did not change the running daemon")
	}

	// And the two switches are genuinely separate, which is the whole
	// argument for there being two.
	d.Loop.SetSmartAgentEnabled(true)
	post(false)
	if !d.Loop.SmartAgentEnabled() {
		t.Error("changing orchestration turned Smart Agent off")
	}
}

// The panel reads both switches from one GET, so a client can show one on
// and the other off without asking twice.
func TestSettingsReportsBothSwitches(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	d.Loop.SetSmartAgentEnabled(true)
	d.Loop.SetOrchestrateEnabled(false)

	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	resp, err := http.Get(httpSrv.URL + "/api/settings")
	if err != nil {
		t.Fatalf("GET settings: %v", err)
	}
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["smart_agent"] != true {
		t.Errorf("smart_agent = %v, want true", got["smart_agent"])
	}
	if _, ok := got["orchestrate"]; !ok {
		t.Fatal("the settings payload has no orchestrate key, so the panel cannot draw the switch")
	}
	if got["orchestrate"] != false {
		t.Errorf("orchestrate = %v, want false", got["orchestrate"])
	}
}

// A body that is not what the endpoint takes is a 400, not a silent
// change: the switch decides what a request may spend.
func TestSetOrchestrateRefusesAJunkBody(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	resp, err := http.Post(httpSrv.URL+"/api/settings/orchestrate", "application/json",
		strings.NewReader(`not json`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if d.Loop.OrchestrateEnabled() {
		t.Error("a rejected request changed the switch anyway")
	}
}
