package credentials

import (
	"os"
	"path/filepath"
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
	info, err := os.Stat(path(home))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("credentials file mode = %v, want 0600 (contains a secret API key)", info.Mode().Perm())
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
