package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The version of a binary is stamped into the file and nowhere else, so
// asking it is the only way to know. A fake that answers "version" is
// enough to pin the asking.
func TestVersionOfAsksTheBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake is a shell script")
	}
	fake := filepath.Join(t.TempDir(), "localcode")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n[ \"$1\" = version ] && echo 9.9.9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	v, err := VersionOf(fake)
	if err != nil {
		t.Fatalf("VersionOf: %v", err)
	}
	if v != "9.9.9" {
		t.Errorf("VersionOf = %q, want 9.9.9", v)
	}

	mute := filepath.Join(t.TempDir(), "localcode")
	if err := os.WriteFile(mute, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := VersionOf(mute); err == nil || !strings.Contains(err.Error(), "no version") {
		t.Errorf("a binary that prints nothing should be an error naming that; got %v", err)
	}
}

// Nothing staged is the ordinary case, and it is "" rather than a path
// that does not exist.
func TestStagedBinaryIsEmptyWhenNothingIsStaged(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if got := StagedBinary(); got != "" {
		t.Errorf("StagedBinary = %q with nothing staged", got)
	}
}
