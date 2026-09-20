package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The guards for what a review of v0.138.0 found, each named for the
// thing that went wrong rather than for the function that was changed.

func writeAt(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func homeAndRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	home, repo := filepath.Join(dir, "home"), filepath.Join(dir, "repo")
	for _, d := range []string{filepath.Join(home, ".config", "opencode"), filepath.Join(home, ".localcode"), repo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home, repo
}

const workingLocalcode = `{"providers":{"a":{"type":"anthropic","api_key":"k"}},` +
	`"profiles":{"main":{"provider":"a","model":"claude-x"}},"default_profile":"main"}`

// A machine with opencode installed, whose opencode config carries keys
// localcode cannot honour, and a working localcode config beside it.
//
// This stopped localcode starting. The localcode file was fine; the
// refusal came from a file written for another program, which localcode
// had gone looking for on its own.
func TestSomebodyElsesConfigDoesNotStopLocalcodeStarting(t *testing.T) {
	home, repo := homeAndRepo(t)
	writeAt(t, filepath.Join(home, ".config", "opencode"), "opencode.json",
		`{"lsp":{"go":{"command":["gopls"]}},"formatter":{"gofmt":{}},"share":"manual"}`)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", workingLocalcode)

	cfg, notes, err := loadMergedFrom(configSources(home, repo, ""))
	if err != nil {
		t.Fatalf("localcode would not start because another program's config has keys it cannot honour: %v", err)
	}
	if cfg.DefaultProfile != "main" {
		t.Errorf("default_profile = %q, want the localcode file's", cfg.DefaultProfile)
	}
	if joined := strings.Join(notes, " "); !strings.Contains(joined, "lsp") && !strings.Contains(joined, "formatter") {
		t.Errorf("the file was set aside and nothing was said about it: %v", notes)
	}
}

// The same key in localcode's own file still stops everything: somebody
// who wrote it for localcode is told at startup.
func TestARefusalInLocalcodesOwnFileStillStops(t *testing.T) {
	home, repo := homeAndRepo(t)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json",
		strings.TrimSuffix(workingLocalcode, "}")+`,"lsp":{"go":{}}}`)
	if _, _, err := loadMergedFrom(configSources(home, repo, "")); err == nil {
		t.Fatal("a key localcode cannot honour, written into localcode's own config, was accepted")
	}
}

// A value that came back from the environment was substituted a second
// time whenever any provider used opencode's env list, because the
// second pass covered the whole document rather than what it had written.
func TestAnEnvironmentValueIsExpandedOnceAndOnlyOnce(t *testing.T) {
	t.Setenv("ZZ_INNER", "inner-val")
	t.Setenv("ZZ_OUTER", "{env:ZZ_INNER}")
	t.Setenv("ZZ_PROVKEY", "sk-prov")
	dir := t.TempDir()
	cfg, _, err := Load(writeAt(t, dir, "config.json", `{
	  "provider": {"a": {"npm":"@ai-sdk/anthropic","env":["ZZ_PROVKEY"],"options":{"baseURL":"https://api.anthropic.com/v1"}},
	               "b": {"type":"anthropic","api_key":"{env:ZZ_OUTER}"}},
	  "model": "a/m"
	}`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Providers["a"].APIKey; got != "sk-prov" {
		t.Errorf("the provider naming its variable got %q, want sk-prov", got)
	}
	if got := cfg.Providers["b"].APIKey; got != "{env:ZZ_INNER}" {
		t.Errorf("api_key = %q, want it expanded once and not twice", got)
	}
}

// And a value from the environment carrying "{file:" is not refused as
// if the file had written it.
func TestAnEnvironmentValueCarryingAFileTokenIsNotRefused(t *testing.T) {
	t.Setenv("ZZ_PROVKEY", "sk-prov")
	t.Setenv("ZZ_TEXT", "see {file:~/notes} for details")
	dir := t.TempDir()
	cfg, _, err := Load(writeAt(t, dir, "config.json", `{
	  "provider": {"a": {"npm":"@ai-sdk/anthropic","env":["ZZ_PROVKEY"],"options":{"baseURL":"https://api.anthropic.com/v1"}}},
	  "model": "a/m",
	  "verify_command": "{env:ZZ_TEXT}"
	}`))
	if err != nil {
		t.Fatalf("a value that came back carrying {file: was refused: %v", err)
	}
	if cfg.VerifyCommand != "see {file:~/notes} for details" {
		t.Errorf("verify_command = %q", cfg.VerifyCommand)
	}
}

// The collision check compared literal keys, so tools.write — which
// becomes edit, covering write_file — sat beside a permission for
// write_file without a word. Resolution prefers the exact name over the
// alias, so the file denied edits in one spelling and allowed them in the
// other: the fail-open direction, in the key this translation exists for.
func TestOverlappingToolsAndPermissionAreRefused(t *testing.T) {
	base := `"providers":{"a":{"type":"anthropic","api_key":"k"}},` +
		`"profiles":{"m":{"provider":"a","model":"x"}},"default_profile":"m"`
	for _, tc := range []struct{ name, body string }{
		{"write vs write_file", `{` + base + `,"tools":{"write":false},"permission":{"write_file":"allow"}}`},
		{"write vs edit", `{` + base + `,"tools":{"write":false},"permission":{"edit":"allow"}}`},
		{"read vs read_file", `{` + base + `,"tools":{"read":false},"permission":{"read_file":"allow"}}`},
		{"bash vs bash", `{` + base + `,"tools":{"bash":false},"permission":{"bash":"allow"}}`},
	} {
		if _, _, err := Load(writeAt(t, t.TempDir(), "config.json", tc.body)); err == nil {
			t.Errorf("%s: loaded, so one spelling silently lost to the other", tc.name)
		}
	}

	// Rules about different tools still merge.
	cfg, _, err := Load(writeAt(t, t.TempDir(), "config.json",
		`{`+base+`,"tools":{"bash":false},"permission":{"read_file":"allow"}}`))
	if err != nil {
		t.Fatalf("two rules about different tools were refused: %v", err)
	}
	if got := cfg.ResolvePermissionFor(context.Background(), "bash", "ls", true); got != DecisionDeny {
		t.Errorf("bash = %q, want deny", got)
	}
	if got := cfg.ResolvePermissionFor(context.Background(), "read_file", "x", true); got != DecisionAllow {
		t.Errorf("read_file = %q, want allow", got)
	}
}

// The same bytes gave different refusals across runs, because the tools
// block was walked in map order.
func TestTheSameBytesGiveTheSameRefusal(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		_, err := NormalizeOpencode([]byte(`{"tools":{"write":true,"apply_patch":false}}`))
		if err == nil {
			t.Fatal("two entries mapping to edit with different decisions were accepted")
		}
		seen[err.Error()] = true
	}
	if len(seen) != 1 {
		t.Errorf("the same input produced %d different messages: %v", len(seen), seen)
	}
}

// An agent that carries only a prompt is most of an opencode file, and
// every one of them stopped the config loading with a message about an
// empty profile name nobody had written.
func TestAnAgentThatNamesNoModelTakesTheDefault(t *testing.T) {
	home, repo := homeAndRepo(t)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", workingLocalcode)
	writeAt(t, repo, "opencode.json", `{"agent":{"worker":{"prompt":"help"}}}`)

	cfg, _, err := loadMergedFrom(configSources(home, repo, ""))
	if err != nil {
		t.Fatalf("an ordinary opencode agent stopped the config loading: %v", err)
	}
	got, err := cfg.ResolveProfile("worker")
	if err != nil {
		t.Fatalf("ResolveProfile(worker): %v", err)
	}
	if got.Model != "claude-x" {
		t.Errorf("the agent resolved to %+v, want the default profile's model", got)
	}
}

// One that carried a temperature with no model to attach it to keeps
// working, with the setting reported rather than turned into a profile
// that has no provider in it.
func TestAnAgentSettingWithNothingToAttachToIsReported(t *testing.T) {
	home, repo := homeAndRepo(t)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", workingLocalcode)
	writeAt(t, repo, "opencode.json", `{"agent":{"writer":{"temperature":0.7,"prompt":"x"}}}`)

	cfg, notes, err := loadMergedFrom(configSources(home, repo, ""))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := cfg.ResolveProfile("writer"); err != nil {
		t.Errorf("the agent does not resolve: %v", err)
	}
	if !strings.Contains(strings.Join(notes, " "), "agent.writer.temperature") {
		t.Errorf("nothing said about the temperature that could not be applied: %v", notes)
	}
	for name, p := range cfg.Profiles {
		if p.Provider == "" {
			t.Errorf("profile %q has no provider in it", name)
		}
	}
}

// Several checks returned at once instead of adding to the set, so the
// first thing wrong in a file was the only thing reported — and a copied
// opencode.json usually trips three or four.
func TestRefusalsAreReportedTogether(t *testing.T) {
	_, err := NormalizeOpencode([]byte(`{"tools":{"bash":"yes"},"snapshot":false,"server":{"port":1}}`))
	if err == nil {
		t.Fatal("a file with three things wrong loaded")
	}
	for _, want := range []string{"tools", "snapshot", "server"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %s, so it stopped at the first: %v", want, err)
		}
	}
}

// A message named a value the file did not contain, which sends somebody
// looking through their own file for text that is not in it.
func TestARefusalQuotesWhatTheFileSays(t *testing.T) {
	if _, err := NormalizeOpencode([]byte(`{"snapshot":true}`)); err != nil {
		t.Errorf(`"snapshot": true was refused, and localcode always records them: %v`, err)
	}
	_, err := NormalizeOpencode([]byte(`{"snapshot":false}`))
	if err == nil || !strings.Contains(err.Error(), "snapshot: false") {
		t.Errorf("refusal = %v, want it to quote false", err)
	}
	_, err = NormalizeOpencode([]byte(`{"share":{"mode":"auto"}}`))
	if err == nil {
		t.Fatal("a share block that is not a string was accepted")
	}
	if strings.Contains(err.Error(), `share: ""`) {
		t.Errorf("the refusal names an empty string the file does not contain: %v", err)
	}
}

// The agent block may arrive under opencode's older name, and the
// refusal named a key the file does not have.
func TestTheAgentRefusalNamesTheSpellingTheFileUses(t *testing.T) {
	_, err := NormalizeOpencode([]byte(`{"mode":{"a":{"prompt":"x"}},"agents":{"b":{"profile":"p"}}}`))
	if err == nil {
		t.Fatal("mode beside agents was accepted")
	}
	if !strings.Contains(err.Error(), "mode and agents") {
		t.Errorf("refusal = %v, want it to name mode, which is the key in the file", err)
	}
}
