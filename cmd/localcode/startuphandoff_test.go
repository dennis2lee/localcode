package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// stageFakeLocalcode plants an executable where update.StagedBinary looks
// and answers "localcode version" with ver, writing a line to logPath
// every time it is run.
//
// A script rather than a built binary: what is being tested is the
// decision, and the decision's only question of the file is what it says
// its version is. It also makes "was it run at all" answerable, which is
// half of what there is to check — running it is the expensive step.
func stageFakeLocalcode(t *testing.T, cacheHome, logPath, ver string) string {
	t.Helper()
	dir := filepath.Join(cacheHome, "localcode", "bin")
	if runtime.GOOS == "darwin" {
		dir = filepath.Join(cacheHome, "Library", "Caches", "localcode", "bin")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "localcode"
	if runtime.GOOS == "windows" {
		name = "localcode.exe"
	}
	staged := filepath.Join(dir, name)
	script := "#!/bin/sh\necho ran >> \"" + logPath + "\"\necho " + ver + "\n"
	if err := os.WriteFile(staged, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return staged
}

// The positive case, which is the one the refusals above cannot stand in
// for: a version that says no to everything passes every refusal test
// there is.
func TestStagedHandoffTakesANewerCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the staged copy here is a shell script")
	}
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("LOCALAPPDATA", cache)
	ranLog := filepath.Join(t.TempDir(), "ran.txt")
	staged := stageFakeLocalcode(t, cache, ranLog, "9.9.9")

	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := stagedHandoffBinary("0.1.0", cfg, false)
	if !ok {
		t.Fatal("a staged 9.9.9 beside a running 0.1.0 was not handed off to")
	}
	if got != staged {
		t.Errorf("handed off to %q, want the staged copy %q", got, staged)
	}

	// And an older staged copy is not taken, which is the same machinery
	// answering the other way rather than a second refusal.
	if _, ok := stagedHandoffBinary("99.0.0", cfg, false); ok {
		t.Error("handed off to a staged copy older than the running one")
	}
}

// A build that is not a version cannot be superseded, so the staged copy
// is not run to ask what it is. Running it is a process, and up to
// thirty seconds of one if it hangs.
func TestAnUnstampedBuildDoesNotRunTheStagedCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the staged copy here is a shell script")
	}
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("LOCALAPPDATA", cache)
	ranLog := filepath.Join(t.TempDir(), "ran.txt")
	stageFakeLocalcode(t, cache, ranLog, "9.9.9")

	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, ok := stagedHandoffBinary("dev", cfg, false); ok {
		t.Error("a dev build handed off, although no release is newer than a build that is not a version")
	}
	if b, err := os.ReadFile(ranLog); err == nil && strings.Contains(string(b), "ran") {
		t.Error("the staged copy was run to answer a question parsing had already settled")
	}

	// And the same refusal for the other handoff, which is where it
	// matters most: taking the staged copy's version as its own baseline
	// is how a build that is not a version would come to install over
	// itself at startup. Both paths ask through stagedNewerThan, so this
	// is the one place it can be asked.
	if p, v := stagedNewerThan("dev"); p != "" || v != "" {
		t.Errorf("stagedNewerThan(dev) = %q, %q; an unstamped build has no newer release", p, v)
	}
	if p, v := stagedNewerThan("0.1.0"); p == "" || v != "9.9.9" {
		t.Errorf("stagedNewerThan(0.1.0) = %q, %q; want the staged copy and its version", p, v)
	}
}
