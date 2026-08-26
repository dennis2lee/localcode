package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// env is a lookup a test controls, instead of setting variables on the
// process: these tests run in parallel with each other and with every
// other test in this package, and os.Setenv is process-wide.
func env(pairs ...string) func(string) (string, bool) {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

func expand(t *testing.T, doc string, lookup func(string) (string, bool)) string {
	t.Helper()
	out, err := expandEnv([]byte(doc), lookup)
	if err != nil {
		t.Fatalf("expandEnv: %v", err)
	}
	return string(out)
}

func TestAPlaceholderTakesItsValueFromTheEnvironment(t *testing.T) {
	t.Parallel()
	got := expand(t, `{"providers":{"anthropic":{"type":"anthropic","api_key":"{env:ANTHROPIC_API_KEY}"}}}`,
		env("ANTHROPIC_API_KEY", "sk-ant-secret"))
	if !strings.Contains(got, "sk-ant-secret") {
		t.Errorf("the key was not substituted: %s", got)
	}
	if strings.Contains(got, "{env:") {
		t.Errorf("the placeholder is still there: %s", got)
	}
}

// Anywhere in the document, not just in the fields this version happens
// to think of as secrets — a base_url, a model id, an MCP server's own
// environment, an MCP server's own credentials.
func TestEveryStringInTheFileCanUseOne(t *testing.T) {
	t.Parallel()
	doc := `{
	  "profiles": {"main": {"provider": "p", "model": "{env:LC_MODEL}", "max_tokens": 8000}},
	  "mcp_servers": {"gh": {"command": "npx", "args": ["-y", "{env:LC_PKG}"], "env": {"TOKEN": "{env:LC_TOKEN}"}}},
	  "providers": {"p": {"base_url": "http://{env:LC_HOST}:9090/v1"}}
	}`
	got := expand(t, doc, env("LC_MODEL", "claude-opus-5", "LC_PKG", "@modelcontextprotocol/server-github", "LC_TOKEN", "ghp_x", "LC_HOST", "speech.local"))
	for _, want := range []string{"claude-opus-5", "@modelcontextprotocol/server-github", "ghp_x", "http://speech.local:9090/v1"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing from %s", want, got)
		}
	}
}

// Reading the document and writing it back has to leave everything that
// is not a placeholder exactly as it was. Numbers are where that is easy
// to get wrong: decoded as float64, an integer larger than 2^53 comes
// back a different number, silently.
func TestNumbersAndStructureSurviveUnchanged(t *testing.T) {
	t.Parallel()
	doc := `{"max_concurrent_tasks":4,"profiles":{"main":{"provider":"p","model":"m","max_tokens":8000000,"temperature":0.2}}}`
	got := expand(t, doc, env())

	var cfg Config
	if err := json.Unmarshal([]byte(got), &cfg); err != nil {
		t.Fatalf("the expanded document no longer loads: %v\n%s", err, got)
	}
	if cfg.Profiles["main"].MaxTokens != 8000000 {
		t.Errorf("max_tokens came back as %d", cfg.Profiles["main"].MaxTokens)
	}
	if cfg.Profiles["main"].Temperature != 0.2 {
		t.Errorf("temperature came back as %v", cfg.Profiles["main"].Temperature)
	}
	if cfg.MaxConcurrentTasks != 4 {
		t.Errorf("max_concurrent_tasks came back as %d", cfg.MaxConcurrentTasks)
	}

	// Past float64's exact range: 9007199254740993 is 2^53 + 1, which as a
	// float64 is 9007199254740992. No config field holds a number that
	// large today; the guarantee is that this rewrites what it was given,
	// not that it rewrites the fields it currently knows about.
	big := expand(t, `{"providers":{"p":{"api_key":"{env:LC_K}"}},"n":9007199254740993}`, env("LC_K", "k"))
	if !strings.Contains(big, "9007199254740993") {
		t.Errorf("a number was changed on the way through: %s", big)
	}
}

// An unset variable is an error at load, not an empty string. An empty
// api_key fails later as a 401 that says nothing about the config file.
func TestAMissingVariableIsAnErrorThatNamesIt(t *testing.T) {
	t.Parallel()
	_, err := expandEnv([]byte(`{"providers":{"anthropic":{"api_key":"{env:ANTHROPIC_API_KEY}"}}}`), env())
	if err == nil {
		t.Fatal("a config referring to a variable that is not set loaded anyway")
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("the error does not name the variable: %v", err)
	}
	if !strings.Contains(err.Error(), "providers.anthropic.api_key") {
		t.Errorf("the error does not say which field asked for it: %v", err)
	}
	if !strings.Contains(err.Error(), ":-") {
		t.Errorf("the error does not mention the optional form: %v", err)
	}
}

// Set but empty counts as missing, for the same reason: it is the case
// where a variable was exported by a script that had nothing to put in it.
func TestAnEmptyVariableIsTreatedAsMissing(t *testing.T) {
	t.Parallel()
	if _, err := expandEnv([]byte(`{"providers":{"p":{"api_key":"{env:LC_EMPTY}"}}}`), env("LC_EMPTY", "")); err == nil {
		t.Error("an empty variable was accepted as a value")
	}
	got := expand(t, `{"providers":{"p":{"base_url":"{env:LC_EMPTY:-http://localhost:1234/v1}"}}}`, env("LC_EMPTY", ""))
	if !strings.Contains(got, "http://localhost:1234/v1") {
		t.Errorf("the fallback was not used for an empty variable: %s", got)
	}
}

func TestAFallbackCoversAnOptionalValue(t *testing.T) {
	t.Parallel()
	got := expand(t, `{"providers":{"p":{"base_url":"{env:LC_URL:-http://127.0.0.1:1234/v1}"}}}`, env())
	if !strings.Contains(got, "http://127.0.0.1:1234/v1") {
		t.Errorf("the fallback was not used: %s", got)
	}
	got = expand(t, `{"providers":{"p":{"base_url":"{env:LC_URL:-http://127.0.0.1:1234/v1}"}}}`, env("LC_URL", "http://gpu-box:8000/v1"))
	if !strings.Contains(got, "http://gpu-box:8000/v1") {
		t.Errorf("the variable did not win over the fallback: %s", got)
	}
}

// A value spliced into surrounding text, and more than one in a string.
func TestAPlaceholderCanBePartOfAValue(t *testing.T) {
	t.Parallel()
	got := expand(t, `{"mcp_servers":{"s":{"url":"https://{env:LC_HOST}/mcp","headers":{"Authorization":"Bearer {env:LC_TOKEN}"}}}}`,
		env("LC_HOST", "mcp.example.com", "LC_TOKEN", "t0ken"))
	if !strings.Contains(got, "https://mcp.example.com/mcp") || !strings.Contains(got, "Bearer t0ken") {
		t.Errorf("the substitution did not keep the text around it: %s", got)
	}
}

// A key that happens to contain a quote or a backslash. Substituting in
// the file's text rather than in the decoded document would produce a
// config that no longer parses, or one that parses differently.
func TestAValueWithQuotesInItDoesNotBreakTheFile(t *testing.T) {
	t.Parallel()
	nasty := `a"b\c` + "\n" + `{"not":"json"}`
	got := expand(t, `{"providers":{"p":{"type":"openai-compat","base_url":"u","api_key":"{env:LC_KEY}"}}}`, env("LC_KEY", nasty))

	var cfg Config
	if err := json.Unmarshal([]byte(got), &cfg); err != nil {
		t.Fatalf("the expanded document no longer parses: %v\n%s", err, got)
	}
	if cfg.Providers["p"].APIKey != nasty {
		t.Errorf("the value came back as %q", cfg.Providers["p"].APIKey)
	}
}

// Only a real variable name is a placeholder, so an ordinary string that
// happens to contain a brace is left alone.
func TestSomethingThatIsNotAVariableNameIsLeftAlone(t *testing.T) {
	t.Parallel()
	doc := `{"agents":{"a":{"profile":"p","prompt":"write {env: something} and {envelope} and {env:9bad}"}}}`
	got := expand(t, doc, env())
	for _, want := range []string{"{env: something}", "{envelope}", "{env:9bad}"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was mangled: %s", want, got)
		}
	}
}

// The whole point of loading it this way: what is on disk stays a
// placeholder. Every writer in this package rewrites the raw file, so a
// setting saved from the UI ("always allow", `localcode mcp add`) must
// not turn the key into its value.
func TestWritingBackKeepsThePlaceholder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := `{
  "providers": {"anthropic": {"type": "anthropic", "api_key": "{env:ANTHROPIC_API_KEY}"}},
  "profiles": {"main": {"provider": "anthropic", "model": "claude-opus-5"}},
  "default_profile": "main"
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := AddPermissionRuleToFile(path, "bash", PermissionRule{Match: "git status", Decision: DecisionAllow}); err != nil {
		t.Fatalf("saving a permission rule: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "{env:ANTHROPIC_API_KEY}") {
		t.Errorf("the placeholder was replaced by its value on disk:\n%s", after)
	}
	if strings.Contains(string(after), "sk-") {
		t.Errorf("something that looks like a key was written to disk:\n%s", after)
	}
}

// End to end through the loader that every command uses, with the real
// environment rather than a lookup this test controls: the wiring is the
// part that would silently not happen.
func TestLoadReadsAKeyOutOfTheEnvironment(t *testing.T) {
	t.Setenv("LC_TEST_API_KEY", "sk-from-the-environment")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	doc := `{
  "providers": {"local": {"type": "openai-compat", "base_url": "http://127.0.0.1:1234/v1", "api_key": "{env:LC_TEST_API_KEY}"}},
  "profiles": {"main": {"provider": "local", "model": "qwen"}},
  "default_profile": "main"
}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Providers["local"].APIKey; got != "sk-from-the-environment" {
		t.Errorf("api_key loaded as %q", got)
	}
}

func TestLoadSaysWhichVariableIsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	doc := `{
  "providers": {"local": {"type": "openai-compat", "base_url": "u", "api_key": "{env:LC_TEST_ABSENT_KEY}"}},
  "profiles": {"main": {"provider": "local", "model": "qwen"}},
  "default_profile": "main"
}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("a config asking for a variable that is not set loaded anyway")
	}
	for _, want := range []string{"LC_TEST_ABSENT_KEY", "providers.local.api_key", path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}
