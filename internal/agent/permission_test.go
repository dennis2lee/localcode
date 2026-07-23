package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/session"
)

func newPermissionTestBroker(t *testing.T) (*PermissionBroker, *session.Store, string) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	broker := NewPermissionBroker(store)
	configPath := filepath.Join(t.TempDir(), "config.json")
	broker.ConfigPath = configPath
	return broker, store, configPath
}

// callAndResolve runs Func() in a goroutine (it blocks until Resolve is
// called), waits for the permission.request event so the id is known, then
// resolves it with the given scope and returns Func's result.
func callAndResolve(t *testing.T, broker *PermissionBroker, sessionID, toolName, subject string, allow bool, scope string) bool {
	t.Helper()
	ctx := WithSessionID(context.Background(), sessionID)

	// A baseline of what's already in the log, so waitForPermissionID
	// can't mistake an earlier (already-resolved) permission.request for
	// the one this call is about to make — a session may see the same
	// tool asked about more than once across a test.
	before, err := broker.store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	baseline := len(before)

	resultCh := make(chan bool, 1)
	errCh := make(chan error, 1)
	go func() {
		allowed, err := broker.Func()(ctx, toolName, subject, "test call")
		resultCh <- allowed
		errCh <- err
	}()

	id := waitForPermissionID(t, broker.store, sessionID, baseline)
	broker.Resolve(id, allow, scope)

	select {
	case allowed := <-resultCh:
		if err := <-errCh; err != nil {
			t.Fatalf("Func(): %v", err)
		}
		return allowed
	case <-time.After(5 * time.Second):
		t.Fatal("Func() never returned after Resolve")
		return false
	}
}

// waitForPermissionID polls the session's event log for a permission.request
// appended at or after index baseline — Func() appends it asynchronously
// relative to this test goroutine reading it back, and baseline keeps an
// earlier, already-resolved request (from an earlier call in the same
// test) from being picked up a second time.
func waitForPermissionID(t *testing.T, store *session.Store, sessionID string, baseline int) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs, err := store.Events(sessionID, 0)
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		for i := len(evs) - 1; i >= baseline; i-- {
			if evs[i].Type == events.TypePermissionRequest {
				id, _ := evs[i].Data["id"].(string)
				return id
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no new permission.request event appeared")
	return ""
}

// TestScopeOnceAsksEveryTime confirms the default: an approval with no
// scope (or "once") is not remembered, so the same call asks again.
func TestScopeOnceAsksEveryTime(t *testing.T) {
	broker, _, _ := newPermissionTestBroker(t)

	if !callAndResolve(t, broker, "s1", "bash", "npm test", true, "once") {
		t.Fatal("expected the first call to be allowed")
	}
	if !callAndResolve(t, broker, "s1", "bash", "npm test", true, "once") {
		t.Fatal("expected the second call to be allowed (approved again)")
	}
	// Both calls had to go through Func()'s full request/resolve path —
	// if "once" were remembered, the second callAndResolve would hang
	// waiting for a permission.request event that never comes, and this
	// test would time out rather than reach here.
}

// TestScopeSessionRemembersWithinSession confirms "allow for session"
// covers every later call in the same session with the same rule pattern,
// without needing to answer again.
func TestScopeSessionRemembersWithinSession(t *testing.T) {
	broker, _, _ := newPermissionTestBroker(t)

	if !callAndResolve(t, broker, "s1", "bash", "npm test", true, "session") {
		t.Fatal("expected the first call to be allowed")
	}

	// A second, different npm invocation matches the same generalized
	// rule ("npm *") and should be granted without a new request —
	// call Func() directly and expect it to return immediately rather
	// than blocking on a permission.request this test never resolves.
	allowed, err := broker.Func()(WithSessionID(context.Background(), "s1"), "bash", "npm install", "test call")
	if err != nil {
		t.Fatalf("Func(): %v", err)
	}
	if !allowed {
		t.Error("a second npm command should be auto-allowed after \"allow for session\"")
	}
}

// TestScopeSessionDoesNotLeakToOtherSessions confirms a session-scoped
// grant is exactly that — scoped to the session it was granted in.
func TestScopeSessionDoesNotLeakToOtherSessions(t *testing.T) {
	broker, store, _ := newPermissionTestBroker(t)
	if _, err := store.CreateSession("s2", "", "general-purpose", true); err != nil {
		t.Fatalf("create session s2: %v", err)
	}

	if !callAndResolve(t, broker, "s1", "bash", "npm test", true, "session") {
		t.Fatal("expected the first call to be allowed")
	}

	if !callAndResolve(t, broker, "s2", "bash", "npm test", true, "once") {
		t.Fatal("expected s2's call to be allowed once resolved")
	}
	// The fact that callAndResolve for s2 had to wait for and resolve its
	// own permission.request (rather than returning instantly) proves the
	// s1 grant did not leak across sessions.
}

// TestScopeAlwaysPersistsToConfigFile confirms "always allow" writes a
// rule to config.json, in a form that would actually match a later call.
func TestScopeAlwaysPersistsToConfigFile(t *testing.T) {
	broker, _, configPath := newPermissionTestBroker(t)

	if !callAndResolve(t, broker, "s1", "bash", "npm test", true, "always") {
		t.Fatal("expected the call to be allowed")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.json was not written: %v", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("written config.json does not parse: %v", err)
	}
	// The persisted rule has to actually cover future npm calls, not just
	// exist — resolve confirms the shape (generalized to "npm *"), not
	// just that some bytes landed on disk.
	if got := cfg.ResolvePermission("bash", "npm run build", true); got != config.DecisionAllow {
		t.Errorf("ResolvePermission after persisting = %q, want allow — the written rule should cover other npm invocations too", got)
	}
	if got := cfg.ResolvePermission("bash", "rm -rf /", true); got != config.DecisionAsk {
		t.Errorf("ResolvePermission(rm) after allowing npm = %q, want ask — the persisted rule must not be broader than the npm command it was granted for", got)
	}
}

// TestScopeAlwaysAlsoGrantsCurrentSession confirms "always" behaves like
// "session" for the rest of the current run, not just for a future
// process that reloads config.json.
func TestScopeAlwaysAlsoGrantsCurrentSession(t *testing.T) {
	broker, _, _ := newPermissionTestBroker(t)

	if !callAndResolve(t, broker, "s1", "bash", "npm test", true, "always") {
		t.Fatal("expected the call to be allowed")
	}

	allowed, err := broker.Func()(WithSessionID(context.Background(), "s1"), "bash", "npm install", "test call")
	if err != nil {
		t.Fatalf("Func(): %v", err)
	}
	if !allowed {
		t.Error("a later npm command in the same run should be auto-allowed after \"always allow\", without waiting for a config reload")
	}
}

// TestDenyIsNeverRemembered confirms Resolve(id, false, ...) never grants
// anything, regardless of what scope was requested — denying is always
// exactly this one call, since no client offers a "deny forever" option
// and a scope arriving on a deny would be a bug elsewhere, not a policy
// to honor.
func TestDenyIsNeverRemembered(t *testing.T) {
	broker, _, configPath := newPermissionTestBroker(t)

	if callAndResolve(t, broker, "s1", "bash", "npm test", false, "always") {
		t.Fatal("expected the call to be denied")
	}
	if _, err := os.Stat(configPath); err == nil {
		t.Error("a denial must never write anything to config.json")
	}

	// The next identical call must ask again, not be silently denied from
	// memory — deny-with-scope should behave exactly like deny-once.
	if !callAndResolve(t, broker, "s1", "bash", "npm test", true, "once") {
		t.Error("a later call should still be asked (and can be allowed) independently of the earlier denial")
	}
}

// TestForgetSessionDropsGrants confirms ForgetSession actually releases
// the memory, matching what happens when a session is deleted.
func TestForgetSessionDropsGrants(t *testing.T) {
	broker, _, _ := newPermissionTestBroker(t)

	if !callAndResolve(t, broker, "s1", "bash", "npm test", true, "session") {
		t.Fatal("expected the call to be allowed")
	}
	broker.ForgetSession("s1")

	// After forgetting, the same command must ask again.
	if !callAndResolve(t, broker, "s1", "bash", "npm test", true, "once") {
		t.Error("expected the call to be allowed once resolved")
	}
}

// TestScopeAlwaysWithoutConfigPathStillGrantsSession confirms a broker
// with no ConfigPath (the daemon couldn't resolve a config.json to write
// to) degrades gracefully: "always" still behaves as "session" instead of
// failing the tool call the user just approved.
func TestScopeAlwaysWithoutConfigPathStillGrantsSession(t *testing.T) {
	broker, _, _ := newPermissionTestBroker(t)
	broker.ConfigPath = ""

	if !callAndResolve(t, broker, "s1", "bash", "npm test", true, "always") {
		t.Fatal("expected the call to be allowed even though persisting will fail")
	}
	allowed, err := broker.Func()(WithSessionID(context.Background(), "s1"), "bash", "npm install", "test call")
	if err != nil {
		t.Fatalf("Func(): %v", err)
	}
	if !allowed {
		t.Error("the session-level grant should still apply even when persisting to config.json is unavailable")
	}
}
