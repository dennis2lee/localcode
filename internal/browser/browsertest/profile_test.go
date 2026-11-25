package browsertest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The directory a test is given is new, holds the profile's name and
// nothing else, and is gone when the test is over.
func TestAProfileDirIsNewNamedAndGoneAfterTheTest(t *testing.T) {
	var dir string
	t.Run("a test that starts a browser", func(t *testing.T) {
		dir = ProfileDir(t)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != ProfileNameFile {
			t.Fatalf("a new profile directory holds %v, want only %s", entries, ProfileNameFile)
		}
		if name, _ := os.ReadFile(filepath.Join(dir, ProfileNameFile)); string(name) != "Default\n" {
			t.Errorf("the profile is named %q, want Default", name)
		}
		// What a browser leaves: files, in directories of its own.
		if err := os.MkdirAll(filepath.Join(dir, "Default", "Network"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Default", "Network", "Cookies"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the profile directory is still there after its test: %v", err)
	}
}

// heldDir is a directory with a file in it, and a remove that fails, as
// for a file a process has open, until release is called.
func heldDir(t *testing.T) (dir string, remove func(string) error, release func(), tries *int) {
	t.Helper()
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Default", "DIPS"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	held := true
	tries = new(int)
	remove = func(path string) error {
		*tries++
		if held {
			return &os.PathError{Op: "unlinkat", Path: filepath.Join(path, "Default", "DIPS"), Err: errors.New("used by another process")}
		}
		return os.RemoveAll(path)
	}
	return dir, remove, func() { held = false }, tries
}

// A directory nothing holds is removed at once, and nobody is asked who
// holds it.
func TestADirectoryNothingHoldsGoesAtOnce(t *testing.T) {
	dir, remove, release, tries := heldDir(t)
	release()
	r, err := removeWithin(dir, time.Minute, remove, func([]string) []string {
		t.Error("asked who holds a directory that was removed")
		return nil
	})
	if err != nil || r.waited != 0 || r.first != nil || *tries != 1 {
		t.Errorf("removal = %+v, %v after %d tries; want one try and no wait", r, err, *tries)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the directory is still there: %v", err)
	}
}

// A directory a process still holds is waited for, and removed when the
// process lets go. Who held it at the first try is kept.
func TestAHeldDirectoryIsWaitedFor(t *testing.T) {
	dir, remove, release, tries := heldDir(t)
	asked := 0
	holders := func(paths []string) []string {
		asked++
		if len(paths) != 1 || filepath.Base(paths[0]) != "DIPS" {
			t.Errorf("asked who holds %v, want the one file left", paths)
		}
		return []string{"process 7 (a browser)"}
	}
	late := func(path string) error {
		if *tries == 3 {
			release()
		}
		return remove(path)
	}
	r, err := removeWithin(dir, time.Minute, late, holders)
	if err != nil {
		t.Fatalf("a directory that came free was not removed: %v", err)
	}
	if *tries != 4 || r.waited <= 0 || r.first == nil || len(r.held) != 1 || r.held[0] != "process 7 (a browser)" {
		t.Errorf("removal = %+v after %d tries; want the fourth try to remove it, a wait, and the holder kept", r, *tries)
	}
	if asked != 1 {
		t.Errorf("asked who holds it %d times, want once: at the first try", asked)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the directory is still there: %v", err)
	}
}

// A directory still held when the time is up is a failure, and the
// failure says what would not go, what is left, and who holds it: the
// browser a test left running is not waited out and passed.
func TestADirectoryStillHeldAtTheEndIsAFailureThatNamesTheHolder(t *testing.T) {
	dir, remove, _, tries := heldDir(t)
	asked := 0
	holders := func([]string) []string {
		asked++
		if asked == 1 {
			return []string{"process 7 (a browser)", "process 8 (its network service)"}
		}
		return []string{"process 8 (its network service)"}
	}
	start := time.Now()
	r, err := removeWithin(dir, 60*time.Millisecond, remove, holders)
	if err == nil {
		t.Fatal("a directory that never came free was reported removed")
	}
	if took := time.Since(start); took < 60*time.Millisecond {
		t.Errorf("gave up after %v, before the time it was given", took)
	}
	if *tries < 2 {
		t.Errorf("tried %d times, want more than one", *tries)
	}
	var pe *os.PathError
	if !errors.As(err, &pe) || filepath.Base(pe.Path) != "DIPS" {
		t.Errorf("the failure does not carry what the removal said: %v", err)
	}
	for _, want := range []string{
		"used by another process",
		"left in it: " + filepath.Join("Default", "DIPS"),
		"held by: process 8 (its network service);",
		"held at the first try by: process 7 (a browser); process 8 (its network service)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure lacks %q: %v", want, err)
		}
	}
	if len(r.held) != 2 {
		t.Errorf("the holders at the first try were not kept: %v", r.held)
	}
}

// With nothing to name, the failure says so, not an empty list.
func TestAFailureWithNoHolderToNameSaysSo(t *testing.T) {
	dir, remove, _, _ := heldDir(t)
	_, err := removeWithin(dir, 20*time.Millisecond, remove, func([]string) []string { return nil })
	if err == nil || !strings.Contains(err.Error(), "held by: no process the system names") {
		t.Errorf("failure = %v", err)
	}
}
