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
