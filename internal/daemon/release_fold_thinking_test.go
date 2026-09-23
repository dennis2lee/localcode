package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"localcode/internal/client"
	"localcode/internal/config"
)

// The muse reasoning block's switch has the same two homes keep_going
// has: the settings window's checkbox and "/fold-thinking". The checkbox
// moves it, saves it, and every client hears; GET /api/settings is where
// a window that just opened reads it, with the profiles it would reach.
func TestFoldThinkingSwitchSyncsSavesAndNamesTheMuseProfiles(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	d.Loop.Config.Profiles["glimmer"] = config.Profile{Provider: "local", Model: "meta/muse-glimmer-30b"}
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"keep_going": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d.Broker.ConfigPath = cfgPath
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

	resp, err := http.Post(httpSrv.URL+"/api/settings/fold-thinking", "application/json",
		strings.NewReader(`{"enabled":false}`))
	if err != nil {
		t.Fatalf("POST fold-thinking: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("POST fold-thinking status = %d, want 204", resp.StatusCode)
	}
	if data := waitForSettings(t, evCh); data["fold_thinking"] != false {
		t.Errorf("settings.changed fold_thinking = %v after the checkbox turned it off", data["fold_thinking"])
	}
	if d.Loop.FoldThinkingEnabled() {
		t.Error("the daemon still has the switch on")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"fold_thinking": false`) || !strings.Contains(string(raw), `"keep_going": true`) {
		t.Errorf("config.json after the checkbox:\n%s", raw)
	}

	var got map[string]any
	r2, err := http.Get(httpSrv.URL + "/api/settings")
	if err != nil {
		t.Fatalf("GET settings: %v", err)
	}
	defer r2.Body.Close()
	if err := json.NewDecoder(r2.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["fold_thinking"] != false {
		t.Errorf("GET /api/settings fold_thinking = %v", got["fold_thinking"])
	}
	if !reflect.DeepEqual(got["muse_profiles"], []any{"glimmer"}) {
		t.Errorf("GET /api/settings muse_profiles = %#v, want [glimmer]", got["muse_profiles"])
	}

	// A malformed body changes nothing.
	bad, err := http.Post(httpSrv.URL+"/api/settings/fold-thinking", "application/json", strings.NewReader(`{`))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest || d.Loop.FoldThinkingEnabled() {
		t.Errorf("a malformed body answered %d and left the switch %v", bad.StatusCode, d.Loop.FoldThinkingEnabled())
	}
}

// muse_profiles is a list, never null, so a client can read its length
// without a guard when no profile runs a muse model.
func TestMuseProfilesIsAnEmptyListWhenNoneRunOne(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	r, err := http.Get(httpSrv.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if list, ok := got["muse_profiles"].([]any); !ok || len(list) != 0 {
		t.Errorf("muse_profiles = %#v, want []", got["muse_profiles"])
	}
}

// "/thinking off" typed in one client reaches a window watching any
// session. settings.changed carried every switch but the display ones,
// so an open Web UI went on painting reasoning until it was reloaded.
func TestTheDisplaySwitchesReachEveryClientLive(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	ctx := context.Background()
	typed, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	watching, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	evCtx, cancelEvents := context.WithCancel(ctx)
	defer cancelEvents()
	evCh, err := c.SubscribeEvents(evCtx, watching.ID, 0)
	if err != nil {
		t.Fatalf("SubscribeEvents: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if err := c.SendMessage(ctx, typed.ID, "/thinking off"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	data := waitForSettings(t, evCh)
	if data["show_thinking"] != false {
		t.Errorf("settings.changed show_thinking = %v after /thinking off", data["show_thinking"])
	}
	for _, key := range []string{"show_timestamps", "orchestrate", "fold_thinking"} {
		if _, ok := data[key].(bool); !ok {
			t.Errorf("settings.changed carries no %s: %v", key, data)
		}
	}
}
