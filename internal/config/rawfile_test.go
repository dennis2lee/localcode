package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestUpdateRawConfigWritesMode0600 pins that every writer built on
// updateRawConfig creates the file at 0o600, not the world/group-readable
// 0o644 the ad hoc writers used before — this file can hold provider API
// keys and MCP auth headers.
func TestUpdateRawConfigWritesMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode bits don't apply on Windows")
	}
	path := filepath.Join(t.TempDir(), "config.json")

	if err := updateRawConfig(path, func(raw map[string]json.RawMessage) error {
		raw["default_profile"] = json.RawMessage(`"main"`)
		return nil
	}); err != nil {
		t.Fatalf("updateRawConfig: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
}

// TestUpdateRawConfigMutateErrorLeavesFileUntouched pins that a mutate
// error aborts before anything is written — the whole point of returning an
// error from the callback (e.g. "mcp server not found") is that it must not
// also rewrite (reformat, or worse, corrupt) the file as a side effect.
func TestUpdateRawConfigMutateErrorLeavesFileUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{"default_profile": "main"}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	wantErr := errors.New("boom")
	err := updateRawConfig(path, func(raw map[string]json.RawMessage) error {
		raw["default_profile"] = json.RawMessage(`"changed"`)
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("updateRawConfig error = %v, want %v", err, wantErr)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != original {
		t.Errorf("file = %s, want the original left untouched after a mutate error", data)
	}

	// No stray temp file left behind in the directory either.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("dir entries = %v, want only config.json (no leftover temp file)", entries)
	}
}

// TestUpdateRawConfigRoundTripsThroughRename confirms the happy path still
// produces valid, readable JSON after the switch to temp-file+rename.
func TestUpdateRawConfigRoundTripsThroughRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")

	if err := updateRawConfig(path, func(raw map[string]json.RawMessage) error {
		raw["default_profile"] = json.RawMessage(`"main"`)
		return nil
	}); err != nil {
		t.Fatalf("updateRawConfig: %v", err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.DefaultProfile != "main" {
		t.Errorf("DefaultProfile = %q, want main", cfg.DefaultProfile)
	}
}
