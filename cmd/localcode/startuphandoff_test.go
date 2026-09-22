package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The three ways the cheap handoff says no, and why each matters.
func TestStagedHandoffRefusals(t *testing.T) {
	t.Run("a process that can exec never reads a staged copy", func(t *testing.T) {
		// It replaces itself through autoUpdateAtStartup instead, so the
		// staged path is Windows and the window, not every platform.
		if _, ok := stagedHandoffBinary("0.1.0", "", true); ok {
			t.Error("handed off although this process can exec")
		}
	})

	t.Run("no staged copy is one stat and no more", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
		t.Setenv("LOCALAPPDATA", t.TempDir())
		if _, ok := stagedHandoffBinary("0.1.0", "", false); ok {
			t.Error("handed off with nothing staged")
		}
	})

	t.Run("a config that cannot be read answers no", func(t *testing.T) {
		// Not because the flag defaults to off — it does not — but
		// because handing off is the unusual action and this process has
		// no answer. buildDaemon reads the same file and reports why.
		if autoUpdateWanted(filepath.Join(t.TempDir(), "nothing-here.json")) {
			t.Error("a config that cannot be read said yes")
		}
	})

	t.Run("auto_update: false is honoured", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, []byte(`{"auto_update": false}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if autoUpdateWanted(path) {
			t.Error("auto_update: false was read as yes")
		}
		if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if !autoUpdateWanted(path) {
			t.Error("an unset auto_update was read as no; the default is on")
		}
	})
}
