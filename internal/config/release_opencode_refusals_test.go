package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadText writes body to a temp config and loads it, so these tests
// exercise the real path a file takes rather than a struct built by hand.
func loadText(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(p)
	return cfg, err
}

const workingProviders = `"providers":{"anthropic":{"type":"anthropic","api_key":"k"}},` +
	`"profiles":{"main":{"provider":"anthropic","model":"claude-sonnet-4-5"}},"default_profile":"main"`

// A {file:...} placeholder becomes the value if nothing refuses it, and
// the value it becomes is an API key that is the literal text.
func TestAFilePlaceholderIsRefusedRatherThanSent(t *testing.T) {
	_, err := loadText(t, `{"providers":{"anthropic":{"type":"anthropic","api_key":"{file:~/.keys/anthropic}"}},`+
		`"profiles":{"main":{"provider":"anthropic","model":"m"}},"default_profile":"main"}`)
	if err == nil {
		t.Fatal("a {file:...} placeholder loaded, so it would be sent as that literal text")
	}
	if !strings.Contains(err.Error(), "{file:~/.keys/anthropic}") {
		t.Errorf("the refusal does not quote the placeholder: %v", err)
	}
	if !strings.Contains(err.Error(), "{env:") {
		t.Errorf("the refusal does not say what to do instead: %v", err)
	}
}

// The same placeholder in an MCP header, which is where it would be sent
// to somebody else's server rather than merely failing.
func TestAFilePlaceholderIsRefusedInAnMCPHeaderToo(t *testing.T) {
	_, err := loadText(t, `{`+workingProviders+`,`+
		`"mcp_servers":{"r":{"type":"http","url":"https://example.com","headers":{"Authorization":"{file:/etc/token}"}}}}`)
	if err == nil {
		t.Fatal("a {file:...} placeholder in a header loaded, so it would be sent to the server verbatim")
	}
}

// {env:NAME} is opencode's spelling too and must keep working.
func TestTheEnvPlaceholderStillWorks(t *testing.T) {
	t.Setenv("LOCALCODE_TEST_KEY", "secret")
	cfg, err := loadText(t, `{"providers":{"anthropic":{"type":"anthropic","api_key":"{env:LOCALCODE_TEST_KEY}"}},`+
		`"profiles":{"main":{"provider":"anthropic","model":"m"}},"default_profile":"main"}`)
	if err != nil {
		t.Fatalf("a working {env:} config was refused: %v", err)
	}
	if got := cfg.Providers["anthropic"].APIKey; got != "secret" {
		t.Errorf("api_key = %q, want the expanded value", got)
	}
}

// An opencode model id pasted into a profile names the provider twice.
// Left alone it loads, validates, and is sent verbatim.
func TestAModelIdThatNamesTheProviderTwiceIsRefused(t *testing.T) {
	_, err := loadText(t, `{"providers":{"anthropic":{"type":"anthropic","api_key":"k"}},`+
		`"profiles":{"main":{"provider":"anthropic","model":"anthropic/claude-sonnet-4-5"}},"default_profile":"main"}`)
	if err == nil {
		t.Fatal(`"anthropic/claude-sonnet-4-5" loaded, and it would be sent to the provider with the prefix on it`)
	}
	if !strings.Contains(err.Error(), "claude-sonnet-4-5") {
		t.Errorf("the refusal does not show what the model should be: %v", err)
	}
}

// And a slash that is part of a real model id is left alone. Local
// OpenAI-compatible servers name models this way, which is the case this
// check must not break.
func TestASlashInAnOrdinaryModelIdIsFine(t *testing.T) {
	for _, model := range []string{"google/gemma-3n-e4b", "account/muse-glimmer-30b"} {
		if _, err := loadText(t, `{"providers":{"local":{"type":"openai-compat","base_url":"http://127.0.0.1:8000/v1"}},`+
			`"profiles":{"main":{"provider":"local","model":"`+model+`"}},"default_profile":"main"}`); err != nil {
			t.Errorf("model %q was refused, and it is how that server names its own model: %v", model, err)
		}
	}
}

// opencode's spelling of auto_update, whose whole purpose is to say "do
// not replace this binary" — and which left the field nil, meaning on.
func TestOpencodeAutoupdateIsHeard(t *testing.T) {
	for _, tc := range []struct {
		json string
		want bool
	}{
		{`"autoupdate":false`, false},
		{`"autoupdate":true`, true},
		{`"autoupdate":"notify"`, false},
	} {
		cfg, err := loadText(t, `{`+workingProviders+`,`+tc.json+`}`)
		if err != nil {
			t.Fatalf("%s was refused: %v", tc.json, err)
		}
		if cfg.AutoUpdate == nil {
			t.Errorf("%s left auto_update unsaid, and unsaid means on", tc.json)
			continue
		}
		if *cfg.AutoUpdate != tc.want {
			t.Errorf("%s gave auto_update=%v, want %v", tc.json, *cfg.AutoUpdate, tc.want)
		}
	}
}

// Both spellings at once, disagreeing, is a file whose author is not sure.
func TestTwoSpellingsOfAutoUpdateThatDisagreeAreRefused(t *testing.T) {
	if _, err := loadText(t, `{`+workingProviders+`,"auto_update":true,"autoupdate":false}`); err == nil {
		t.Fatal("a config saying both true and false loaded, and one of them was silently dropped")
	}
	// Agreeing is not an error: it is redundant, not contradictory.
	if _, err := loadText(t, `{`+workingProviders+`,"auto_update":false,"autoupdate":false}`); err != nil {
		t.Errorf("two spellings that agree were refused: %v", err)
	}
}

// localcode's own spelling keeps meaning exactly what it means today.
func TestLocalcodeAutoUpdateIsUnchanged(t *testing.T) {
	cfg, err := loadText(t, `{`+workingProviders+`,"auto_update":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoUpdate == nil || *cfg.AutoUpdate {
		t.Error("auto_update:false did not survive")
	}
	cfg, err = loadText(t, `{`+workingProviders+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoUpdate != nil {
		t.Error("a config that said nothing about updating came back having said something")
	}
}
