package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A config file is written by hand and wants to say why. JSON has no
// comments, so this one carried pseudo-comments instead: a "//base_url"
// key beside "base_url", which reads badly and shows up in the parsed
// config as a field nothing reads.
func TestCommentsAreReadAndIgnored(t *testing.T) {
	src := []byte(`{
  // the endpoint everything local goes to
  "providers": {
    /* block comments work too,
       including across lines */
    "local": { "type": "openai-compat", "base_url": "http://localhost:1234/v1" }
  },
  "smart_agent": true,
}`)
	var cfg Config
	if err := json.Unmarshal(stripComments(src), &cfg); err != nil {
		t.Fatalf("a commented config did not parse: %v", err)
	}
	if cfg.Providers["local"].BaseURL != "http://localhost:1234/v1" {
		t.Errorf("base_url = %q", cfg.Providers["local"].BaseURL)
	}
	if cfg.SmartAgent == nil || !*cfg.SmartAgent {
		t.Error("smart_agent did not survive, so the trailing comma before } was not handled")
	}
}

// The "//" in a URL is not a comment, which is the bug every naive
// version of this has, and a base_url is the field most likely to have
// one.
func TestASlashInsideAStringIsNotAComment(t *testing.T) {
	src := []byte(`{"providers":{"p":{"type":"openai-compat","base_url":"https://example.com/v1"}}}`)
	var cfg Config
	if err := json.Unmarshal(stripComments(src), &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cfg.Providers["p"].BaseURL; got != "https://example.com/v1" {
		t.Errorf("base_url = %q, want the whole URL", got)
	}
}

// An escaped quote must not end the string it is in, or everything after
// it is scanned as if it were outside one.
func TestAnEscapedQuoteDoesNotEndTheString(t *testing.T) {
	src := []byte(`{"a":"he said \"//not a comment\"","b":1}`)
	var out map[string]any
	if err := json.Unmarshal(stripComments(src), &out); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out["a"] != `he said "//not a comment"` {
		t.Errorf("a = %v", out["a"])
	}
	if out["b"] == nil {
		t.Error("the key after the escaped quote was lost")
	}
}

// Blanking rather than deleting is what lets a writer find the exact
// span of a value in the original bytes.
func TestStrippingKeepsEveryOffset(t *testing.T) {
	src := []byte("{\n  // note\n  \"a\": 1\n}")
	got := stripComments(append([]byte(nil), src...))
	if len(got) != len(src) {
		t.Fatalf("stripped length %d, original %d: offsets no longer line up", len(got), len(src))
	}
	// And the line structure survives, so a parse error's line number
	// still means what it says.
	if strings.Count(string(got), "\n") != strings.Count(string(src), "\n") {
		t.Error("a comment swallowed a newline")
	}
}

// The half that matters: this program rewrites config.json when a switch
// is toggled, and a rewrite that dropped the comments would eat the very
// thing they are for, silently, the first time somebody typed
// /smart-agent.
func TestAToggleKeepsTheCommentsInTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	src := `{
  // Which model answers. Do not point this at the big one by accident.
  "profiles": {
    "cheap": { "provider": "local", "model": "qwen3" }
  },
  /* Turned on for this project only. */
  "smart_agent": false
}
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := SetSmartAgentInFile(path, true); err != nil {
		t.Fatalf("SetSmartAgentInFile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	out := string(raw)
	if !strings.Contains(out, "Do not point this at the big one by accident") {
		t.Errorf("the line comment was eaten by the write:\n%s", out)
	}
	if !strings.Contains(out, "Turned on for this project only") {
		t.Errorf("the block comment was eaten by the write:\n%s", out)
	}
	// And the change actually landed.
	var cfg Config
	if err := json.Unmarshal(stripComments(append([]byte(nil), raw...)), &cfg); err != nil {
		t.Fatalf("the rewritten file does not parse: %v\n%s", err, out)
	}
	if cfg.SmartAgent == nil || !*cfg.SmartAgent {
		t.Errorf("the toggle did not take effect:\n%s", out)
	}
	// Everything else is byte for byte what it was.
	if !strings.Contains(out, `"cheap": { "provider": "local", "model": "qwen3" }`) {
		t.Errorf("an untouched section was reformatted:\n%s", out)
	}
}

// A file with no comments is rewritten the way it always was, so nothing
// about the common case changes.
func TestAPlainFileIsStillRewrittenWholesale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"smart_agent":false}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := SetSmartAgentInFile(path, true); err != nil {
		t.Fatalf("SetSmartAgentInFile: %v", err)
	}
	raw, _ := os.ReadFile(path)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.SmartAgent == nil || !*cfg.SmartAgent {
		t.Errorf("the toggle did not take effect: %s", raw)
	}
}

// Adding a key to a commented file has no span to replace, and inventing
// a position would mean deciding which side of somebody's comment it
// belongs on. Refusing and saying so is better than rewriting the file
// and taking the comments with it.
func TestAddingAKeyToACommentedFileIsRefusedRatherThanGuessed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	src := "{\n  // only this\n  \"profiles\": {}\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := SetSmartAgentInFile(path, true)
	if err == nil {
		t.Fatal("adding a key to a commented file should be refused, not guessed at")
	}
	if !strings.Contains(err.Error(), "comments") {
		t.Errorf("error = %q, want it to say why", err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != src {
		t.Errorf("the file was changed despite the refusal:\n%s", raw)
	}
}
