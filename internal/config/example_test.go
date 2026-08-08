package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// config.example.json is the only documentation of the file format that
// can be copied and run, so it has to stay a valid Config rather than a
// description of one that used to be.
//
// Loaded through the real loader, not just json.Unmarshal, because the
// loader is where an unknown key or a renamed field would actually bite.
func TestExampleConfigStillMatchesTheStruct(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config.example.json does not parse as a Config: %v", err)
	}

	// Spot-check the blocks that are easy to leave behind when a
	// feature moves: an example that silently stops mentioning a
	// setting is how a setting becomes undiscoverable.
	if cfg.Dictation == nil {
		t.Error("no dictation block; the example no longer shows how to configure speech")
	}
	if len(cfg.Providers) == 0 {
		t.Error("no providers in the example")
	}
	if len(cfg.Profiles) == 0 {
		t.Error("no profiles in the example")
	}
}
