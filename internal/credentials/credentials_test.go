package credentials

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadMissingFileReturnsZeroValue(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.AnthropicAPIKey != "" {
		t.Errorf("expected empty Credentials, got %+v", c)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	if err := SaveAnthropicAPIKey(home, "sk-ant-abc123"); err != nil {
		t.Fatalf("SaveAnthropicAPIKey: %v", err)
	}

	c, err := Load(home)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.AnthropicAPIKey != "sk-ant-abc123" {
		t.Errorf("AnthropicAPIKey = %q, want %q", c.AnthropicAPIKey, "sk-ant-abc123")
	}
}

func TestSaveFilePermissionsAreRestricted(t *testing.T) {
	home := t.TempDir()
	if err := SaveAnthropicAPIKey(home, "sk-ant-abc123"); err != nil {
		t.Fatalf("SaveAnthropicAPIKey: %v", err)
	}
	wantOwnerOnly(t, path(home), "credentials file")
}

// wantOwnerOnly requires the file at path to be readable by its owner and
// nobody else.
//
// Not checked on Windows, where the mode bits are not the permission: Go
// reports 0666 for a file created 0600, because access there is governed
// by an ACL the mode never reached. Asserting 0600 fails on a file that
// is correctly protected, and dropping the check entirely would lose it
// on the platforms where it is the guarantee — credentials.json stores
// a secret API key that must not be readable by other accounts on a shared
// machine.
func wantOwnerOnly(t *testing.T, path, what string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s %s: %v", what, path, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s mode = %o, want 0600", what, perm)
	}
}

func TestSaveOverwritesPreviousKeyButKeepsFile(t *testing.T) {
	home := t.TempDir()
	if err := SaveAnthropicAPIKey(home, "first-key"); err != nil {
		t.Fatal(err)
	}
	if err := SaveAnthropicAPIKey(home, "second-key"); err != nil {
		t.Fatal(err)
	}
	c, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if c.AnthropicAPIKey != "second-key" {
		t.Errorf("AnthropicAPIKey = %q, want the most recently saved key %q", c.AnthropicAPIKey, "second-key")
	}
}

func TestLoadCorruptFileErrors(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".localcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path(home), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil {
		t.Error("expected an error for a corrupt credentials file")
	}
}
