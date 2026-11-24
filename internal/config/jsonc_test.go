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

// A key the file does not have yet goes at the end of the object, after
// everything that is there, and nothing that is there moves.
//
// Adding used to be refused, because any position chooses a side of
// somebody's comment. The end of the object chooses none, and the refusal
// had a cost that was measured in the desktop window: with one note in
// config.json, a switch turned on in the settings window was off again
// after a restart, and the key was never written.
func TestAKeyAddedToACommentedFileGoesAtTheEnd(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{
			name: "a comment above the last member",
			src:  "{\n  // only this\n  \"profiles\": {}\n}\n",
			want: "{\n  // only this\n  \"profiles\": {},\n  \"smart_agent\": true\n}\n",
		},
		{
			name: "a comment beside the last member stays beside it",
			src:  "{\n  \"profiles\": {} // which models\n}\n",
			want: "{\n  \"profiles\": {}, // which models\n  \"smart_agent\": true\n}\n",
		},
		{
			name: "a trailing comma is kept as the file's own style",
			src:  "{\n  \"profiles\": {}, // which models\n}\n",
			want: "{\n  \"profiles\": {}, // which models\n  \"smart_agent\": true,\n}\n",
		},
		{
			name: "a closing note stays above the new key",
			src:  "{\n  \"profiles\": {}\n  /* the end\n     of my notes */\n}\n",
			want: "{\n  \"profiles\": {},\n  /* the end\n     of my notes */\n  \"smart_agent\": true\n}\n",
		},
		{
			name: "an object with nothing but a note in it",
			src:  "{\n  // nothing yet\n}\n",
			want: "{\n  // nothing yet\n  \"smart_agent\": true\n}\n",
		},
		{
			name: "one line",
			src:  "{ \"profiles\": {} /* x */ }\n",
			want: "{ \"profiles\": {}, /* x */\n\"smart_agent\": true }\n",
		},
		{
			name: "a brace straight after the last member",
			src:  "// mine\n{\"profiles\": {}}\n",
			want: "// mine\n{\"profiles\": {},\n\"smart_agent\": true\n}\n",
		},
		{
			name: "Windows line ends",
			src:  "{\r\n  \"profiles\": {} // which models\r\n}\r\n",
			want: "{\r\n  \"profiles\": {}, // which models\r\n  \"smart_agent\": true\r\n}\r\n",
		},
		{
			name: "tabs",
			src:  "{\n\t// only this\n\t\"profiles\": {}\n}\n",
			want: "{\n\t// only this\n\t\"profiles\": {},\n\t\"smart_agent\": true\n}\n",
		},
		{
			name: "a slash inside the last value is not a comment",
			src:  "{\n  // where\n  \"default_profile\": \"a//b\"\n}\n",
			want: "{\n  // where\n  \"default_profile\": \"a//b\",\n  \"smart_agent\": true\n}\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(c.src), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := SetSmartAgentInFile(path, true); err != nil {
				t.Fatalf("SetSmartAgentInFile: %v", err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(raw) != c.want {
				t.Errorf("file =\n%q\nwant\n%q", raw, c.want)
			}
			var cfg Config
			if err := json.Unmarshal(stripComments(append([]byte(nil), raw...)), &cfg); err != nil {
				t.Fatalf("the rewritten file does not parse: %v\n%s", err, raw)
			}
			if cfg.SmartAgent == nil || !*cfg.SmartAgent {
				t.Errorf("the key did not land:\n%s", raw)
			}

			// And from here on it is a key the file has: the next change
			// replaces the value where it sits and moves nothing.
			if err := SetSmartAgentInFile(path, false); err != nil {
				t.Fatalf("second SetSmartAgentInFile: %v", err)
			}
			again, _ := os.ReadFile(path)
			if want := strings.Replace(c.want, `"smart_agent": true`, `"smart_agent": false`, 1); string(again) != want {
				t.Errorf("after a second change, file =\n%q\nwant\n%q", again, want)
			}
		})
	}
}

// One write that changes a key the file has and adds one it does not: the
// two edits are made in the same pass, and one must not move the other.
// The key that changes is the last member here, which is where the new
// one is written after.
func TestOneWriteCanChangeAKeyAndAddAnother(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{
			name: "the changed key is last, with a comment beside it",
			src:  "{\n  \"profiles\": {},\n  \"auto_compact_enabled\": true // on since March\n}\n",
			want: "{\n  \"profiles\": {},\n  \"auto_compact_enabled\": false, // on since March\n  \"auto_compact_percent\": 80\n}\n",
		},
		{
			name: "the changed key is last, with nothing after it",
			src:  "{\n  // sizes\n  \"auto_compact_enabled\": true\n}\n",
			want: "{\n  // sizes\n  \"auto_compact_enabled\": false,\n  \"auto_compact_percent\": 80\n}\n",
		},
		{
			name: "the changed key is first",
			src:  "{\n  \"auto_compact_enabled\": true, // on since March\n  \"profiles\": {}\n}\n",
			want: "{\n  \"auto_compact_enabled\": false, // on since March\n  \"profiles\": {},\n  \"auto_compact_percent\": 80\n}\n",
		},
		{
			name: "text after the closing brace, and braces and commas inside comments",
			src:  "{\n  \"auto_compact_enabled\": true /* } , */ // } ,\n}\n// the end }\n",
			want: "{\n  \"auto_compact_enabled\": false, /* } , */ // } ,\n  \"auto_compact_percent\": 80\n}\n// the end }\n",
		},
		{
			name: "the last member is an object over several lines",
			src:  "{\n  \"auto_compact_enabled\": true,\n  \"profiles\": {\n    // the only one\n    \"main\": {}\n  }\n  // end of profiles\n}\n",
			want: "{\n  \"auto_compact_enabled\": false,\n  \"profiles\": {\n    // the only one\n    \"main\": {}\n  },\n  // end of profiles\n  \"auto_compact_percent\": 80\n}\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(c.src), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := SetAutoCompactInFile(path, false, 80); err != nil {
				t.Fatalf("SetAutoCompactInFile: %v", err)
			}
			raw, _ := os.ReadFile(path)
			if string(raw) != c.want {
				t.Errorf("file =\n%q\nwant\n%q", raw, c.want)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(stripComments(append([]byte(nil), raw...)), &top); err != nil {
				t.Fatalf("the rewritten file does not parse: %v\n%s", err, raw)
			}
			if string(top["auto_compact_enabled"]) != "false" || string(top["auto_compact_percent"]) != "80" {
				t.Errorf("the two keys did not both land:\n%s", raw)
			}
		})
	}
}

// A value that takes several lines is written with the file's own line
// ends, replaced or added: a config kept on Windows does not come back
// with two kinds of line end in it.
func TestAValueOverSeveralLinesKeepsTheFilesLineEnds(t *testing.T) {
	const src = "{\r\n  // who may do what\r\n  \"permission\": {\r\n    \"bash\": []\r\n  }\r\n}\r\n"
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Replaces the value of a key the file has.
	if err := AddPermissionRuleToFile(path, "bash", PermissionRule{Match: "git status", Decision: DecisionAllow}); err != nil {
		t.Fatalf("AddPermissionRuleToFile: %v", err)
	}
	// Adds a key it does not have, with a value over several lines.
	if err := SetAutoDelegateTargetInFile(path, "plan", []string{"plan *"}); err != nil {
		t.Fatalf("SetAutoDelegateTargetInFile: %v", err)
	}
	raw, _ := os.ReadFile(path)
	out := string(raw)
	if strings.Count(out, "\n") != strings.Count(out, "\r\n") {
		t.Errorf("the file has a line end that is not the Windows one:\n%q", out)
	}
	if !strings.Contains(out, "// who may do what") {
		t.Errorf("the comment was lost:\n%s", out)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(stripComments(append([]byte(nil), raw...)), &top); err != nil {
		t.Fatalf("the file does not parse: %v\n%s", err, out)
	}
	if !strings.Contains(string(top["permission"]), "git status") || !strings.Contains(string(top["auto_delegate"]), "plan") {
		t.Errorf("the two changes did not both land:\n%s", out)
	}
}

// Two keys added by one write go in a fixed order, so the same change
// always writes the same file.
func TestTwoKeysAddedAtOnceGoInOneOrder(t *testing.T) {
	const src = "{\n  // sizes\n  \"profiles\": {}\n}\n"
	const want = "{\n  // sizes\n  \"profiles\": {},\n  \"auto_compact_enabled\": true,\n  \"auto_compact_percent\": 70\n}\n"
	for i := 0; i < 20; i++ {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := SetAutoCompactInFile(path, true, 70); err != nil {
			t.Fatalf("SetAutoCompactInFile: %v", err)
		}
		if raw, _ := os.ReadFile(path); string(raw) != want {
			t.Fatalf("file =\n%q\nwant\n%q", raw, want)
		}
	}
}

// Every switch the settings window writes, against a file with a note in
// it and none of the keys: each is still there when the file is read the
// way a start reads it.
func TestEverySettingsSwitchLandsInACommentedFile(t *testing.T) {
	const src = "{\n  // my notes about this machine\n  \"providers\": {\"local\": {\"type\": \"openai-compat\", \"base_url\": \"http://127.0.0.1:9/v1\"}},\n" +
		"  \"profiles\": {\"main\": {\"provider\": \"local\", \"model\": \"m\"}}, // the only one\n  \"default_profile\": \"main\"\n}\n"
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	steps := []struct {
		name string
		do   func() error
	}{
		{"smart_agent", func() error { return SetSmartAgentInFile(path, true) }},
		{"ssh", func() error { return SetSSHInFile(path, true) }},
		{"browser", func() error { return SetBrowserInFile(path, true) }},
		{"orchestrate", func() error { return SetOrchestrateInFile(path, true) }},
		{"model_invocable", func() error { return SetModelInvocableInFile(path, true) }},
		{"keep_going", func() error { return SetKeepGoingInFile(path, false) }},
		{"fold_thinking", func() error { return SetFoldThinkingInFile(path, false) }},
		{"repeat_limit", func() error { return SetRepeatLimitInFile(path, 7) }},
		{"skip_permissions", func() error { return SetSkipPermissionsInFile(path, true) }},
		{"permission", func() error {
			return AddPermissionRuleToFile(path, "bash", PermissionRule{Match: "git status", Decision: DecisionAllow})
		}},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, note := range []string{"// my notes about this machine", "// the only one"} {
		if !strings.Contains(out, note) {
			t.Errorf("%q was lost:\n%s", note, out)
		}
	}
	if !strings.HasPrefix(out, src[:strings.Index(src, "\"default_profile\"")]) {
		t.Errorf("what was in the file before the new keys has moved:\n%s", out)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(stripComments(append([]byte(nil), raw...)), &top); err != nil {
		t.Fatalf("the file does not parse: %v\n%s", err, out)
	}
	for _, s := range steps {
		if _, ok := top[s.name]; !ok {
			t.Errorf("%s is not in the file:\n%s", s.name, out)
		}
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v\n%s", err, out)
	}
	if cfg.SmartAgent == nil || !*cfg.SmartAgent {
		t.Errorf("smart_agent did not load as true:\n%s", out)
	}
}

// What is still refused: a file that does not parse, here for a comment
// nobody closed. It is left exactly as it was.
func TestAFileThatDoesNotParseIsLeftAlone(t *testing.T) {
	for _, src := range []string{
		"{\n  // a note\n  \"profiles\": {}\n  /* never closed\n}\n",
		"{\n  // a note\n  \"profiles\": {}\n",
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := SetSmartAgentInFile(path, true); err == nil {
			t.Errorf("a file that does not scan was written to:\n%s", src)
		}
		if raw, _ := os.ReadFile(path); string(raw) != src {
			t.Errorf("the file was changed despite the refusal:\n%s", raw)
		}
	}
}
