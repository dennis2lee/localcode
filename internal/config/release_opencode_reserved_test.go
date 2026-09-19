package config

import (
	"strings"
	"testing"
)

const anthropicProvider = `"providers":{"a":{"type":"anthropic","api_key":"k"}}`

// The reserved prefix is decided from what the file declares, not from
// something the loader remembered.
//
// It was the other way round for one revision: the normaliser recorded
// which profiles it had synthesised, the Config carried that set, merge
// carried it further, and Validate consulted it. That makes the same file
// answer differently depending on how it was loaded — the trap this
// package has already been through once, with a project directory on the
// struct. A profiles block in a file is hand-written by definition, and
// that is the whole of what the rule needs.
func TestAHandWrittenReservedProfileIsRefused(t *testing.T) {
	_, err := loadText(t, `{`+anthropicProvider+`,`+
		`"profiles":{"opencode:default":{"provider":"a","model":"m"}},"default_profile":"opencode:default"}`)
	if err == nil {
		t.Fatal(`a hand-written profile named "opencode:default" was accepted, and the synthesis would land on it`)
	}
	if !strings.Contains(err.Error(), "opencode:default") {
		t.Errorf("the refusal does not name the profile: %v", err)
	}

	// And the synthesis itself still works, under the same prefix.
	cfg, err := loadText(t, `{"provider":{"a":{"npm":"@ai-sdk/anthropic","options":{"apiKey":"k"}}},`+
		`"model":"a/claude-sonnet-4-5"}`)
	if err != nil {
		t.Fatalf("a file whose profile localcode synthesised was refused: %v", err)
	}
	if cfg.DefaultProfile != "opencode:default" {
		t.Errorf("default_profile = %q, want the synthesised one", cfg.DefaultProfile)
	}
}

// opencode names the variable that holds a key rather than the key, so the
// normaliser writes the placeholder localcode understands and the loader
// expands once more. That second pass must cover what the normaliser
// wrote and nothing else: a value that came back from the environment
// carrying the same text is a value, not a placeholder.
func TestTheSecondEnvPassCoversOnlyWhatWasSynthesised(t *testing.T) {
	t.Setenv("LC_KEY_ONE", "sk-from-env")
	cfg, err := loadText(t, `{"provider":{"a":{"npm":"@ai-sdk/anthropic","env":["LC_KEY_ONE"],`+
		`"options":{"baseURL":"https://api.anthropic.com/v1"}}},"model":"a/m"}`)
	if err != nil {
		t.Fatalf("a provider naming its key's variable was refused: %v", err)
	}
	if got := cfg.Providers["a"].APIKey; got != "sk-from-env" {
		t.Errorf("api_key = %q, want the variable's value", got)
	}

	// A key whose own text looks like a placeholder is left alone.
	t.Setenv("LC_KEY_TWO", "{env:LC_KEY_ONE}")
	cfg, err = loadText(t, `{"providers":{"a":{"type":"anthropic","api_key":"{env:LC_KEY_TWO}"}},`+
		`"profiles":{"m":{"provider":"a","model":"x"}},"default_profile":"m"}`)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if got := cfg.Providers["a"].APIKey; got != "{env:LC_KEY_ONE}" {
		t.Errorf("api_key = %q, want the value as it came back, expanded once and not twice", got)
	}
}

// And the /v1 trap, which is the difference between a working anthropic
// provider and a 404 the person reads as a dead key.
func TestTheAnthropicBaseURLLosesItsVersionSegment(t *testing.T) {
	cfg, err := loadText(t, `{"provider":{"a":{"npm":"@ai-sdk/anthropic","options":`+
		`{"baseURL":"https://api.anthropic.com/v1","apiKey":"k"}}},"model":"a/m"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers["a"].BaseURL; got != "https://api.anthropic.com" {
		t.Errorf("anthropic base_url = %q; the client appends /v1/messages, so this would be /v1/v1/messages", got)
	}

	// openai-compat is the other way round: its client appends
	// /chat/completions, so the version segment has to stay.
	cfg, err = loadText(t, `{"provider":{"b":{"npm":"@ai-sdk/openai-compatible","options":`+
		`{"baseURL":"http://127.0.0.1:8000/v1","apiKey":"k"}}},"model":"b/m"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers["b"].BaseURL; got != "http://127.0.0.1:8000/v1" {
		t.Errorf("openai-compat base_url = %q, want it kept whole", got)
	}
}
