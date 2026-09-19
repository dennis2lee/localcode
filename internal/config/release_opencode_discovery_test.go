package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The order is the whole of the compatibility promise, so it is asserted
// as an order rather than as behaviour that happens to come out right.
//
// An opencode file always sits under the localcode file of the same
// scope. That is what makes the guarantee structural: nothing read from
// an opencode file can change what an existing config.json already said,
// and every writer in this package keeps writing to a file that is above
// the ones it never writes.
func TestAnOpencodeFileIsAlwaysUnderTheLocalcodeFileOfItsScope(t *testing.T) {
	// Compared through ToSlash, and the expectations typed out rather
	// than built with filepath.Join. These are real paths on this machine
	// — unlike a path inside a config file, which travels and therefore
	// has one shape everywhere — so the code is right to join them the
	// host's way and the test is wrong to insist on one separator. An
	// expectation built with Join instead would agree with the code for
	// the wrong reason and stop testing the order at all.
	got := slashed(configSources("/home/u", "/repo", ""))
	want := []string{
		"/home/u/.config/opencode/opencode.json",
		"/home/u/.localcode/config.json",
		"/repo/opencode.json",
		"/repo/.localcode/config.json",
	}
	if len(got) != len(want) {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("source %d = %q, want %q", i, got[i], want[i])
		}
	}

	// OPENCODE_CONFIG is opencode's own override and sits where opencode
	// puts it: after the global file and before the project one. It is
	// passed through as the person wrote it, so it is not joined and not
	// normalised.
	withEnv := slashed(configSources("/home/u", "/repo", "/custom/oc.json"))
	if withEnv[1] != "/custom/oc.json" {
		t.Errorf("OPENCODE_CONFIG landed at %v, want second — between the global and the project files", withEnv)
	}
	if withEnv[2] != "/home/u/.localcode/config.json" {
		t.Errorf("a localcode file stopped being above the opencode ones: %v", withEnv)
	}
}

// opencode's project file is at the root of the project. Its .opencode
// directory holds agents, commands and skills, and a config file looked
// for in there would never be found.
func TestTheProjectFileIsAtTheProjectRoot(t *testing.T) {
	for _, s := range configSources("/home/u", "/repo", "") {
		if strings.Contains(s, filepath.Join(".opencode", "opencode.json")) {
			t.Errorf("looking for the project config in %q, and opencode keeps it at the project root", s)
		}
	}
}

// slashed rewrites a list of host paths with forward slashes, so an
// expectation about their ORDER is not also an assertion about which
// platform the test is running on.
func slashed(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.ToSlash(p)
	}
	return out
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A real arrangement: an opencode.json in the repository and a localcode
// config.json beside it, each saying something about the same thing.
func TestTheLocalcodeFileWinsWhereTheySayDifferentThings(t *testing.T) {
	dir := t.TempDir()
	home, repo := filepath.Join(dir, "home"), filepath.Join(dir, "repo")

	write(t, filepath.Join(repo, "opencode.json"), `{
	  "provider": {"a": {"npm": "@ai-sdk/anthropic", "options": {"apiKey": "from-opencode"}}},
	  "model": "a/claude-from-opencode",
	  "username": "ignored-here"
	}`)
	write(t, filepath.Join(repo, ".localcode", "config.json"), `{
	  "providers": {"b": {"type": "anthropic", "api_key": "from-localcode"}},
	  "profiles": {"mine": {"provider": "b", "model": "claude-from-localcode"}},
	  "default_profile": "mine"
	}`)

	cfg, notes, err := loadMergedFrom(configSources(home, repo, ""))
	if err != nil {
		t.Fatalf("the two files together were refused: %v", err)
	}
	if cfg.DefaultProfile != "mine" {
		t.Errorf("default_profile = %q, and the localcode file said \"mine\"", cfg.DefaultProfile)
	}
	// Both providers survive: the opencode file adds rather than replaces.
	if _, ok := cfg.Providers["a"]; !ok {
		t.Error("the opencode file's provider did not arrive")
	}
	if got := cfg.Providers["b"].APIKey; got != "from-localcode" {
		t.Errorf("the localcode provider's key is %q", got)
	}
	if len(notes) == 0 || notes[0] != "username" {
		t.Errorf("notes = %v, want the opencode key that was accepted and not acted on", notes)
	}
}

// An opencode.json on its own is a working configuration.
func TestAnOpencodeFileAloneIsEnough(t *testing.T) {
	dir := t.TempDir()
	home, repo := filepath.Join(dir, "home"), filepath.Join(dir, "repo")
	write(t, filepath.Join(repo, "opencode.json"), `{
	  "provider": {"a": {"npm": "@ai-sdk/anthropic", "options": {"apiKey": "k"}}},
	  "model": "a/claude-sonnet-4-5"
	}`)

	cfg, _, err := loadMergedFrom(configSources(home, repo, ""))
	if err != nil {
		t.Fatalf("an opencode.json with nothing beside it was refused: %v", err)
	}
	if cfg.DefaultProfile != "opencode:default" {
		t.Errorf("default_profile = %q", cfg.DefaultProfile)
	}
}

// .jsonc is what opencode calls a config with comments in it, and a
// person who wrote one should not find it unread.
func TestTheJsoncSpellingIsFound(t *testing.T) {
	dir := t.TempDir()
	home, repo := filepath.Join(dir, "home"), filepath.Join(dir, "repo")
	write(t, filepath.Join(repo, "opencode.jsonc"), `{
	  // the provider, with a comment in it
	  "provider": {"a": {"npm": "@ai-sdk/anthropic", "options": {"apiKey": "k"}}},
	  "model": "a/m",
	}`)
	if _, _, err := loadMergedFrom(configSources(home, repo, "")); err != nil {
		t.Fatalf("opencode.jsonc was not read: %v", err)
	}

	// Both at once is two files with one name, and the author has not
	// said which they meant.
	write(t, filepath.Join(repo, "opencode.json"), `{"model":"a/m"}`)
	_, _, err := loadMergedFrom(configSources(home, repo, ""))
	if err == nil {
		t.Fatal("opencode.json and opencode.jsonc together were accepted, so one of them was read by nobody")
	}
	if !strings.Contains(err.Error(), "opencode.jsonc") || !strings.Contains(err.Error(), "opencode.json ") {
		t.Errorf("the refusal does not name both files: %v", err)
	}
}

// And localcode's own config.json keeps its own name: config.jsonc is not
// a spelling localcode has ever had, and inventing one here would make a
// file appear that the writers in this package do not know about.
func TestLocalcodeConfigHasNoJsoncAlternative(t *testing.T) {
	if alt := jsoncAlternative("/home/u/.localcode/config.json"); alt != "" {
		t.Errorf("config.json gained an alternative spelling %q", alt)
	}
	if alt := jsoncAlternative("/repo/opencode.json"); alt != "/repo/opencode.jsonc" {
		t.Errorf("opencode.json alternative = %q", alt)
	}
}
