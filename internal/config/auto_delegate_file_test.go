package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readRawConfig reads path back as an untyped map, so a test can assert on
// keys the Config struct doesn't know about — the whole point of the
// surgical writers is that those survive.
func readRawConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return raw
}

// TestSetAutoDelegateEnabledInFilePreservesBlock is the case that matters
// for the GUI toggle: flipping "enabled" must not take the agent and match
// patterns with it, or turning auto-delegation off and on again would
// silently reduce it to an inert block.
func TestSetAutoDelegateEnabledInFilePreservesBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{
	  "auto_delegate": {"enabled": false, "agent": "explore", "match": ["what is *", "where is *"]},
	  "some_future_key": {"unknown": true}
	}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetAutoDelegateEnabledInFile(path, true); err != nil {
		t.Fatalf("SetAutoDelegateEnabledInFile: %v", err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.AutoDelegate == nil {
		t.Fatal("auto_delegate block disappeared")
	}
	if !cfg.AutoDelegate.Enabled {
		t.Error("enabled = false, want true")
	}
	if cfg.AutoDelegate.Agent != "explore" {
		t.Errorf("agent = %q, want %q — flipping enabled must not drop the rest of the block", cfg.AutoDelegate.Agent, "explore")
	}
	if len(cfg.AutoDelegate.Match) != 2 {
		t.Errorf("match = %v, want the 2 original patterns", cfg.AutoDelegate.Match)
	}

	if _, ok := readRawConfig(t, path)["some_future_key"]; !ok {
		t.Error("some_future_key was dropped — the writer must only touch auto_delegate")
	}

	// And back off again, to pin that the round trip is symmetric.
	if err := SetAutoDelegateEnabledInFile(path, false); err != nil {
		t.Fatalf("SetAutoDelegateEnabledInFile(false): %v", err)
	}
	cfg, err = LoadFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.AutoDelegate.Enabled {
		t.Error("enabled = true after turning it off")
	}
	if cfg.AutoDelegate.Agent != "explore" {
		t.Errorf("agent = %q after the second write, want %q", cfg.AutoDelegate.Agent, "explore")
	}
}

// TestSetAutoDelegateEnabledInFileCreatesBlock covers a config that has no
// auto_delegate block at all. Writing a bare {"enabled": true} is
// deliberate — see the function's doc comment — so this pins that it does
// not invent an agent name.
func TestSetAutoDelegateEnabledInFileCreatesBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")

	if err := SetAutoDelegateEnabledInFile(path, true); err != nil {
		t.Fatalf("SetAutoDelegateEnabledInFile on a missing file: %v", err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.AutoDelegate == nil || !cfg.AutoDelegate.Enabled {
		t.Fatalf("auto_delegate = %+v, want an enabled block", cfg.AutoDelegate)
	}
	if cfg.AutoDelegate.Agent != "" {
		t.Errorf("agent = %q, want empty — the writer must not guess an agent", cfg.AutoDelegate.Agent)
	}
	// An agent-less block delegates nothing, which is what makes writing
	// one safe rather than surprising.
	if cfg.AutoDelegate.MatchesPrompt("anything at all") {
		t.Error("a match-less block matched a prompt")
	}
}

// TestSetAutoDelegateTargetInFilePreservesEnabled is the mirror of the test
// above: changing the target must not disturb the switch, or configuring
// auto-delegation from a settings panel would silently turn it off.
func TestSetAutoDelegateTargetInFilePreservesEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{
	  "auto_delegate": {"enabled": true, "agent": "old", "match": ["a *"]},
	  "some_future_key": {"unknown": true}
	}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetAutoDelegateTargetInFile(path, "explore", []string{"what is *", "where is *"}); err != nil {
		t.Fatalf("SetAutoDelegateTargetInFile: %v", err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !cfg.AutoDelegate.Enabled {
		t.Error("enabled = false, want the existing true preserved")
	}
	if cfg.AutoDelegate.Agent != "explore" {
		t.Errorf("agent = %q, want explore", cfg.AutoDelegate.Agent)
	}
	if got := cfg.AutoDelegate.Match; len(got) != 2 || got[0] != "what is *" {
		t.Errorf("match = %v, want the two new patterns", got)
	}
	if _, ok := readRawConfig(t, path)["some_future_key"]; !ok {
		t.Error("some_future_key was dropped")
	}

	// Clearing every pattern is a real choice ("delegate nothing"), so it
	// must round-trip as an empty list rather than vanishing into "unset".
	if err := SetAutoDelegateTargetInFile(path, "", nil); err != nil {
		t.Fatalf("SetAutoDelegateTargetInFile(empty): %v", err)
	}
	cfg, err = LoadFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.AutoDelegate.Agent != "" || len(cfg.AutoDelegate.Match) != 0 {
		t.Errorf("after clearing: agent=%q match=%v, want both empty", cfg.AutoDelegate.Agent, cfg.AutoDelegate.Match)
	}
	if !cfg.AutoDelegate.Enabled {
		t.Error("clearing the target turned the switch off")
	}
}

// TestAutoDelegateSnapshotIsACopy pins that a caller can't mutate the live
// config through the snapshot — the delegate path holds one across a whole
// turn while a client may be rewriting the real block.
func TestAutoDelegateSnapshotIsACopy(t *testing.T) {
	cfg := &Config{AutoDelegate: &AutoDelegateConfig{Agent: "explore", Match: []string{"a *"}}}

	snap := cfg.AutoDelegateSnapshot()
	snap.Agent = "tampered"
	snap.Match[0] = "tampered *"

	if cfg.AutoDelegate.Agent != "explore" {
		t.Errorf("live agent = %q, want it unaffected by a snapshot mutation", cfg.AutoDelegate.Agent)
	}
	if cfg.AutoDelegate.Match[0] != "a *" {
		t.Errorf("live match = %v, want it unaffected by a snapshot mutation", cfg.AutoDelegate.Match)
	}

	// And a snapshot taken before a runtime change does not see it.
	before := cfg.AutoDelegateSnapshot()
	cfg.SetAutoDelegateRuntime("plan", []string{"b *"})
	if before.Agent != "explore" {
		t.Errorf("earlier snapshot changed to %q", before.Agent)
	}
	if after := cfg.AutoDelegateSnapshot(); after.Agent != "plan" || after.Match[0] != "b *" {
		t.Errorf("new snapshot = %+v, want the runtime change", after)
	}

	// A config with no block at all can be configured from scratch.
	empty := &Config{}
	if empty.AutoDelegateSnapshot() != nil {
		t.Error("snapshot of a config with no auto_delegate block should be nil")
	}
	empty.SetAutoDelegateRuntime("explore", []string{"x *"})
	if got := empty.AutoDelegateSnapshot(); got == nil || got.Agent != "explore" {
		t.Errorf("snapshot after configuring from nothing = %+v", got)
	}
}
