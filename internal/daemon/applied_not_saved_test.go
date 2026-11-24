package daemon

import (
	"context"
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

// The four routes that were the last to answer a failed save with a 500:
// the delegation setting, the daemon's skip-permissions default, and a
// permission rule added or removed. Each applies the change to the running
// daemon first and writes config.json second, so the 500 said a change
// that was in force had not been made. They answer in two parts now, as
// the switches in the settings window do (see keep_going_test.go for the
// helpers).

// settingsDaemon is a daemon whose config.json is the file at the path
// returned, holding what was given.
func settingsDaemon(t *testing.T, contents string) (*Daemon, *httptest.Server, string) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(model.Close)
	d := newTestDaemon(t, model.URL)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	d.Broker.ConfigPath = cfgPath
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	return d, srv, cfgPath
}

// unwritableDaemon is a daemon with a directory where its config.json
// should be: every write fails, and nothing else about it is unusual.
func unwritableDaemon(t *testing.T) (*Daemon, *httptest.Server) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(model.Close)
	d := newTestDaemon(t, model.URL)
	d.Broker.ConfigPath = t.TempDir()
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	return d, srv
}

// wantNotSaved checks the half of the answer that is the same for all
// four: a 200, applied, not persisted, and a reason.
func wantNotSaved(t *testing.T, status int, answered map[string]any) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("answered %d %v, want 200: the change was applied", status, answered)
	}
	if answered["applied"] != true {
		t.Errorf("answered %v, want the change reported as applied", answered)
	}
	if answered["persisted"] != false {
		t.Errorf("answered %v, which claims a save that could not have happened", answered)
	}
	if msg, _ := answered["error"].(string); !strings.Contains(msg, "failed to persist") {
		t.Errorf("error = %q, which does not say the save failed", msg)
	}
}

// Both halves of the delegation setting in one request, with a config
// that cannot be written. The target is saved first, and a target that
// could not be saved used to end the request there: the switch in the
// same body was never applied, and no window was told the target moved.
func TestAutoDelegateSaysAppliedWhenTheSaveFails(t *testing.T) {
	d, srv := unwritableDaemon(t)

	c := client.New(srv.URL)
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

	status, answered := postSetting(t, srv.URL+"/api/settings/auto-delegate",
		`{"enabled":true,"agent":"plan","match":["what is *"]}`)
	wantNotSaved(t, status, answered)
	// Each save that failed is named, since a caller may have sent one
	// half and may have sent both.
	msg, _ := answered["error"].(string)
	for _, half := range []string{"agent and patterns", "enabled"} {
		if !strings.Contains(msg, half) {
			t.Errorf("error = %q, which does not name %q", msg, half)
		}
	}
	if match, _ := answered["auto_delegate_match"].([]any); answered["auto_delegate"] != true ||
		answered["auto_delegate_agent"] != "plan" || len(match) != 1 || match[0] != "what is *" {
		t.Errorf("answered %v, want the whole setting as it was applied", answered)
	}

	if !d.Loop.AutoDelegateEnabled() {
		t.Error("the switch was not applied: a target that could not be saved stopped the request")
	}
	if block := d.Loop.Config.AutoDelegateSnapshot(); block == nil || block.Agent != "plan" || len(block.Match) != 1 {
		t.Errorf("live block = %+v, want the target that was reported as applied", block)
	}
	if data := waitForSettings(t, evCh); data["auto_delegate"] != true {
		t.Errorf("announced auto_delegate = %v, want every window told it is on", data["auto_delegate"])
	}
	got := readSettings(t, srv.URL)
	if got["auto_delegate"] != true || got["auto_delegate_agent"] != "plan" {
		t.Errorf("GET /api/settings = %v %v, want what was applied", got["auto_delegate"], got["auto_delegate_agent"])
	}

	// A refusal is still a refusal, and is told apart from the answer
	// above by its status. Nothing is applied and the save is never tried.
	status, answered = postSetting(t, srv.URL+"/api/settings/auto-delegate", `{"enabled":false,"agent":"does-not-exist"}`)
	if status != http.StatusBadRequest {
		t.Errorf("an unknown agent answered %d %v, want 400", status, answered)
	}
	if !d.Loop.AutoDelegateEnabled() {
		t.Error("a refused request turned the switch off anyway")
	}
	if block := d.Loop.Config.AutoDelegateSnapshot(); block.Agent != "plan" {
		t.Errorf("agent = %q after a refused change, want it untouched", block.Agent)
	}
}

// A request that carries one half is answered about that half: the error
// names the switch, and not a target nobody sent.
func TestAutoDelegateNamesOnlyTheHalfThatWasNotSaved(t *testing.T) {
	d, srv := unwritableDaemon(t)

	status, answered := postSetting(t, srv.URL+"/api/settings/auto-delegate", `{"enabled":true}`)
	wantNotSaved(t, status, answered)
	msg, _ := answered["error"].(string)
	if !strings.Contains(msg, "enabled") || strings.Contains(msg, "agent and patterns") {
		t.Errorf("error = %q, want it to name the switch and not the target nobody sent", msg)
	}
	if !d.Loop.AutoDelegateEnabled() {
		t.Error("the switch was not applied")
	}
	// The target the request left alone is in the answer all the same,
	// as empty: a client draws the whole panel from it.
	if match, ok := answered["auto_delegate_match"].([]any); !ok || len(match) != 0 || answered["auto_delegate_agent"] != "" {
		t.Errorf("answered %v, want an empty agent and an empty list of patterns", answered)
	}
}

// The daemon's default for "skip every prompt", which a conversation that
// has not answered for itself follows.
func TestSkipPermissionsAnswersInTwoParts(t *testing.T) {
	d, srv, cfgPath := settingsDaemon(t, `{"show_tps":false}`)
	if d.Loop.Config.PermissionsSkipped() {
		t.Fatal("skip_permissions started on, so turning it on would prove nothing")
	}

	for _, enabled := range []bool{true, false} {
		body := `{"enabled":false}`
		if enabled {
			body = `{"enabled":true}`
		}
		status, answered := postSetting(t, srv.URL+"/api/permissions/skip", body)
		if status != http.StatusOK {
			t.Fatalf("enabled=%t answered %d, want 200", enabled, status)
		}
		if answered["skip_permissions"] != enabled || answered["applied"] != true || answered["persisted"] != true {
			t.Errorf("enabled=%t answered %v, want it applied, persisted and reporting %t", enabled, answered, enabled)
		}
		if _, said := answered["error"]; said {
			t.Errorf("enabled=%t: a save that worked carries an error: %v", enabled, answered["error"])
		}
		if d.Loop.Config.PermissionsSkipped() != enabled {
			t.Errorf("the running daemon has skip_permissions %t after it was set to %t", d.Loop.Config.PermissionsSkipped(), enabled)
		}
		saved, err := config.LoadFile(cfgPath)
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		if saved.SkipPermissions == nil || *saved.SkipPermissions != enabled {
			t.Errorf("config.json has skip_permissions %v after it was set to %t", saved.SkipPermissions, enabled)
		}
		if saved.ShowTPS == nil || *saved.ShowTPS {
			t.Error("writing skip_permissions clobbered show_tps")
		}
	}
}

func TestSkipPermissionsSaysAppliedWhenTheSaveFails(t *testing.T) {
	d, srv := unwritableDaemon(t)

	status, answered := postSetting(t, srv.URL+"/api/permissions/skip", `{"enabled":true}`)
	wantNotSaved(t, status, answered)
	if answered["skip_permissions"] != true {
		t.Errorf("answered %v, want skip_permissions true", answered)
	}
	if !d.Loop.Config.PermissionsSkipped() {
		t.Error("the daemon is not running the default it just reported as applied")
	}
	if got := readSettings(t, srv.URL); got["skip_permissions"] != true {
		t.Errorf("GET /api/settings skip_permissions = %v after it was turned on", got["skip_permissions"])
	}

	// A body it cannot read is the one answer that means nothing changed.
	status, answered = postSetting(t, srv.URL+"/api/permissions/skip", `not json`)
	if status != http.StatusBadRequest {
		t.Errorf("a junk body answered %d %v, want 400", status, answered)
	}
	if !d.Loop.Config.PermissionsSkipped() {
		t.Error("a refused request turned the default off anyway")
	}
}

// rulesIn is the rules of an answer or of GET /api/settings, as
// "tool match decision" lines in the order they are held.
func rulesIn(t *testing.T, answered map[string]any) []string {
	t.Helper()
	held, ok := answered["permission_rules"].(map[string]any)
	if !ok {
		t.Fatalf("no permission_rules object in %v", answered)
	}
	var out []string
	for tool, list := range held {
		rules, _ := list.([]any)
		for _, r := range rules {
			rule, _ := r.(map[string]any)
			out = append(out, tool+" "+rule["match"].(string)+" "+rule["decision"].(string))
		}
	}
	return out
}

// A rule added and a rule removed, with a config that takes the write:
// in force at once, in the file, and the answer is the daemon's own list.
func TestPermissionRulesAnswerInTwoParts(t *testing.T) {
	d, srv, cfgPath := settingsDaemon(t, `{"show_tps":false}`)

	status, answered := postSetting(t, srv.URL+"/api/permissions/rules", `{"tool":"bash","match":"npm *","decision":"allow"}`)
	if status != http.StatusOK || answered["applied"] != true || answered["persisted"] != true {
		t.Fatalf("adding a rule answered %d %v, want 200 applied and persisted", status, answered)
	}
	if _, said := answered["error"]; said {
		t.Errorf("a save that worked carries an error: %v", answered["error"])
	}
	if got, want := rulesIn(t, answered), []string{"bash npm * allow"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the answer lists %v, want %v", got, want)
	}
	// The same list a client that opens now is given.
	if got, want := rulesIn(t, readSettings(t, srv.URL)), rulesIn(t, answered); !reflect.DeepEqual(got, want) {
		t.Errorf("GET /api/settings lists %v and the answer listed %v", got, want)
	}
	saved, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if rules := saved.Permissions["bash"].Rules; len(rules) != 1 || rules[0].Match != "npm *" || rules[0].Decision != config.DecisionAllow {
		t.Errorf("config.json bash rules = %+v, want the rule that was added", rules)
	}
	if saved.ShowTPS == nil || *saved.ShowTPS {
		t.Error("writing a rule clobbered show_tps")
	}

	status, answered = postSetting(t, srv.URL+"/api/permissions/rules/remove", `{"tool":"bash","match":"npm *","decision":"allow"}`)
	if status != http.StatusOK || answered["applied"] != true || answered["persisted"] != true {
		t.Fatalf("removing a rule answered %d %v, want 200 applied and persisted", status, answered)
	}
	if got := rulesIn(t, answered); len(got) != 0 {
		t.Errorf("the answer still lists %v after the rule was removed", got)
	}
	if _, rules := d.Loop.Config.PermissionsSnapshot(); len(rules["bash"]) != 0 {
		t.Errorf("the running daemon still holds %v", rules["bash"])
	}
	saved, err = config.LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if rules := saved.Permissions["bash"].Rules; len(rules) != 0 {
		t.Errorf("config.json still has %+v after the rule was removed", rules)
	}
}

// A rule is in force from the moment it is applied, so a save that failed
// is told as that and not as a rule that was never made. The permissions
// window listed nothing for a 500, while the daemon went on allowing or
// refusing by the rule.
func TestPermissionRulesSayAppliedWhenTheSaveFails(t *testing.T) {
	d, srv := unwritableDaemon(t)

	status, answered := postSetting(t, srv.URL+"/api/permissions/rules", `{"tool":"bash","match":"rm *","decision":"deny"}`)
	wantNotSaved(t, status, answered)
	if got, want := rulesIn(t, answered), []string{"bash rm * deny"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the answer lists %v, want %v", got, want)
	}
	if _, rules := d.Loop.Config.PermissionsSnapshot(); len(rules["bash"]) != 1 || rules["bash"][0].Match != "rm *" {
		t.Errorf("the running daemon holds %v, not the rule it just reported as applied", rules["bash"])
	}
	if got := rulesIn(t, readSettings(t, srv.URL)); len(got) != 1 {
		t.Errorf("GET /api/settings lists %v after the rule was added", got)
	}

	// A request that is refused applies nothing, and does not reach the
	// save: a missing field, and a decision that is not one.
	for _, body := range []string{
		`{"tool":"bash","match":"","decision":"allow"}`,
		`{"tool":"bash","match":"ls *","decision":"permit"}`,
		`not json`,
	} {
		for _, path := range []string{"/api/permissions/rules", "/api/permissions/rules/remove"} {
			status, answered := postSetting(t, srv.URL+path, body)
			if status != http.StatusBadRequest {
				t.Errorf("%s %s answered %d %v, want 400", path, body, status, answered)
			}
		}
	}
	if _, rules := d.Loop.Config.PermissionsSnapshot(); len(rules["bash"]) != 1 {
		t.Errorf("the refused requests left %v", rules["bash"])
	}

	status, answered = postSetting(t, srv.URL+"/api/permissions/rules/remove", `{"tool":"bash","match":"rm *","decision":"deny"}`)
	wantNotSaved(t, status, answered)
	if got := rulesIn(t, answered); len(got) != 0 {
		t.Errorf("the answer still lists %v after the rule was removed", got)
	}
	if _, rules := d.Loop.Config.PermissionsSnapshot(); len(rules["bash"]) != 0 {
		t.Errorf("the running daemon still holds %v after reporting the removal as applied", rules["bash"])
	}
}
