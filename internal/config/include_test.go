package config

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// What localcode reads, and what it reads only when asked.
//
// localcode used to go looking for opencode's config on every start, in
// ~/.config/opencode, in OPENCODE_CONFIG and in the project, with no
// setting that turned it off. It now reads two files of its own, and any
// other file only when one of them lists it under "include". These tests
// are that rule: what is read by default, what a listing does, and what
// stays under what.

// withInclude adds an include list to the body of a config.json. The paths
// are real ones from this machine and go through json.Marshal, because a
// Windows path holds backslashes that a hand-written string would turn
// into escapes.
func withInclude(t *testing.T, body string, entries ...string) string {
	t.Helper()
	list, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	body = strings.TrimSpace(body)
	if body == "{}" {
		return `{"include":` + string(list) + `}`
	}
	return strings.TrimSuffix(body, "}") + `,"include":` + string(list) + `}`
}

// loadAt is the whole default load for one home and one project.
func loadAt(home, repo string) (*Config, []string, error) {
	return loadMergedFrom(configSources(home, repo), home)
}

// slashedPaths rewrites a list of host paths with forward slashes, so an
// expectation about their ORDER is not also an assertion about which
// platform the test is running on.
func slashedPaths(srcs []source) []string {
	out := make([]string, len(srcs))
	for i, s := range srcs {
		out[i] = filepath.ToSlash(s.path)
	}
	return out
}

// anOpencodeProvider is an opencode file's provider block, named so a test
// can tell which file a provider came from.
func anOpencodeProvider(name, key string) string {
	return `"provider": {"` + name + `": {"npm": "@ai-sdk/anthropic", "options": {"apiKey": "` + key + `"}}}`
}

// The two places localcode reads without being asked, in the order one is
// laid over the other. Compared through ToSlash and typed out rather than
// built with filepath.Join: an expectation built with Join would agree with
// the code for the wrong reason and stop testing the order at all.
func TestOnlyLocalcodesOwnFilesAreReadByDefault(t *testing.T) {
	got := slashedPaths(configSources("/home/u", "/repo"))
	want := []string{"/home/u/.localcode/config.json", "/repo/.localcode/config.json"}
	if len(got) != len(want) {
		t.Fatalf("sources = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("source %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The three places the old loader looked, each holding a file that would
// stop localcode if it were opened. It starts, says nothing about them,
// and its own config is what it is.
func TestAnOpencodeFileInTheOldPlacesIsNotRead(t *testing.T) {
	home, repo := homeAndRepo(t)
	broken := `{ this is not json`
	writeAt(t, filepath.Join(home, ".config", "opencode"), "opencode.json", broken)
	writeAt(t, filepath.Join(home, ".config", "opencode"), "opencode.jsonc", `{"lsp":{"go":{}}}`)
	writeAt(t, repo, "opencode.json", broken)
	override := writeAt(t, t.TempDir(), "override.json", broken)
	t.Setenv("OPENCODE_CONFIG", override)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", workingLocalcode)

	cfg, notes, err := loadAt(home, repo)
	if err != nil {
		t.Fatalf("a file localcode was not asked to read stopped it: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("something was said about files nobody listed: %v", notes)
	}
	if cfg.DefaultProfile != "main" || len(cfg.Providers) != 1 {
		t.Errorf("the config is not just the localcode file: default %q, providers %v", cfg.DefaultProfile, cfg.Providers)
	}
}

// An opencode file alone is no longer a configuration, and the refusal says
// where to look, since this is the one moment somebody who used to rely on
// it is looking.
func TestAnOpencodeFileAloneIsNotAConfig(t *testing.T) {
	home, repo := homeAndRepo(t)
	writeAt(t, filepath.Join(home, ".config", "opencode"), "opencode.jsonc",
		`{`+anOpencodeProvider("a", "k")+`, "model": "a/claude-sonnet-4-5"}`)

	_, _, err := loadAt(home, repo)
	if err == nil {
		t.Fatal("an opencode file nobody listed was enough to start with")
	}
	for _, want := range []string{"no config found", filepath.Join(home, ".localcode", "config.json"), `"include"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "opencode.json") {
		t.Errorf("the refusal lists an opencode path as somewhere that was looked: %v", err)
	}
}

// A profile name left over from the days the opencode file was read on every
// start. It looks like a typo, so the refusal says where the profile went.
func TestAReferenceToAnOpencodeProfileSaysWhereTheProfileWent(t *testing.T) {
	const provider = `"providers":{"a":{"type":"anthropic","api_key":"k"}},"profiles":{"main":{"provider":"a","model":"claude-x"}}`
	cases := []struct {
		name, body, want string
	}{
		{"default_profile", `{` + provider + `,"default_profile":"opencode:default"}`, `default_profile "opencode:default"`},
		{"agent", `{` + provider + `,"default_profile":"main","agents":{"x":{"profile":"opencode:agent:x"}}}`, `agent "x"`},
		{"fallback", `{"providers":{"a":{"type":"anthropic","api_key":"k"}},"profiles":{"main":{"provider":"a","model":"m","fallback":["opencode:fast"]}},"default_profile":"main"}`, `fallback "opencode:fast"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, repo := homeAndRepo(t)
			writeAt(t, filepath.Join(home, ".localcode"), "config.json", tc.body)

			_, _, err := loadAt(home, repo)
			if err == nil {
				t.Fatal("a reference to a profile nothing defines was accepted")
			}
			for _, want := range []string{tc.want, `"include"`, "opencode file"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q: %v", want, err)
				}
			}
		})
	}

	// A name that was never an opencode profile is a typo and is said as one.
	home, repo := homeAndRepo(t)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", `{`+provider+`,"default_profile":"mian"}`)
	_, _, err := loadAt(home, repo)
	if err == nil {
		t.Fatal("a default_profile naming nothing was accepted")
	}
	if strings.Contains(err.Error(), "include") {
		t.Errorf("a plain typo is blamed on the include list: %v", err)
	}
}

// Listed, the same file is a working configuration, with its comments.
func TestAListedOpencodeFileAloneIsEnough(t *testing.T) {
	home, repo := homeAndRepo(t)
	oc := writeAt(t, filepath.Join(home, ".config", "opencode"), "opencode.jsonc", `{
	  // the provider, with a comment in it
	  `+anOpencodeProvider("a", "k")+`,
	  "model": "a/claude-sonnet-4-5",
	}`)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, "{}", oc))

	cfg, _, err := loadAt(home, repo)
	if err != nil {
		t.Fatalf("a listed opencode file with nothing beside it was refused: %v", err)
	}
	if cfg.DefaultProfile != "opencode:default" {
		t.Errorf("default_profile = %q", cfg.DefaultProfile)
	}
	if len(cfg.Include) != 0 {
		t.Errorf("a merged config still carries its include list: %v", cfg.Include)
	}
}

// The guarantee that survives the change: a listed file sits under the file
// that lists it, so nothing read this way can change what config.json said.
func TestTheLocalcodeFileWinsWhereTheySayDifferentThings(t *testing.T) {
	home, repo := homeAndRepo(t)
	writeAt(t, repo, "opencode.json", `{
	  `+anOpencodeProvider("a", "from-opencode")+`,
	  "model": "a/claude-from-opencode",
	  "username": "ignored-here"
	}`)
	writeAt(t, filepath.Join(repo, ".localcode"), "config.json", withInclude(t, `{
	  "providers": {"b": {"type": "anthropic", "api_key": "from-localcode"}},
	  "profiles": {"mine": {"provider": "b", "model": "claude-from-localcode"}},
	  "default_profile": "mine"
	}`, "../opencode.json"))

	cfg, notes, err := loadAt(home, repo)
	if err != nil {
		t.Fatalf("the two files together were refused: %v", err)
	}
	if cfg.DefaultProfile != "mine" {
		t.Errorf("default_profile = %q, and the localcode file said \"mine\"", cfg.DefaultProfile)
	}
	if got := cfg.Providers["a"].APIKey; got != "from-opencode" {
		t.Errorf("the listed file's provider did not arrive: %q", got)
	}
	if got := cfg.Providers["b"].APIKey; got != "from-localcode" {
		t.Errorf("the localcode provider's key is %q", got)
	}
	if len(notes) == 0 || notes[0] != "username" {
		t.Errorf("notes = %v, want the opencode key that was accepted and not acted on", notes)
	}
}

// The same holds for one provider that both name: the file that lists wins,
// whatever the listed file says.
func TestAListedFileCannotOverrideTheFileThatListsIt(t *testing.T) {
	home, repo := homeAndRepo(t)
	oc := writeAt(t, t.TempDir(), "shared.json", `{"providers":{"p":{"type":"anthropic","api_key":"theirs"}}}`)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t,
		`{"providers":{"p":{"type":"anthropic","api_key":"mine"}}}`, oc))

	cfg, _, err := loadAt(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers["p"].APIKey; got != "mine" {
		t.Errorf("api_key = %q, so a listed file changed what config.json said", got)
	}
}

// Several files on one list are laid in the order written, so a later entry
// is the more specific one.
func TestListedFilesAreLaidInTheOrderListed(t *testing.T) {
	home, repo := homeAndRepo(t)
	dir := t.TempDir()
	one := writeAt(t, dir, "one.json", `{"providers":{"p":{"type":"anthropic","api_key":"one"},"only-one":{"type":"anthropic","api_key":"k"}}}`)
	two := writeAt(t, dir, "two.json", `{"providers":{"p":{"type":"anthropic","api_key":"two"},"only-two":{"type":"anthropic","api_key":"k"}}}`)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, "{}", one, two))

	cfg, _, err := loadAt(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers["p"].APIKey; got != "two" {
		t.Errorf("api_key = %q, want the later entry's", got)
	}
	for _, name := range []string{"only-one", "only-two"} {
		if _, ok := cfg.Providers[name]; !ok {
			t.Errorf("provider %q from a listed file did not arrive", name)
		}
	}
}

// A project's listing is laid over the home one, as a project's own file is
// over the global one, and under the project's config.json.
func TestAProjectListingBeatsTheHomeOneAndLosesToTheProjectFile(t *testing.T) {
	home, repo := homeAndRepo(t)
	dir := t.TempDir()
	global := writeAt(t, dir, "global.json", `{"providers":{"p":{"type":"anthropic","api_key":"global-listed"},"q":{"type":"anthropic","api_key":"global-listed"}}}`)
	project := writeAt(t, dir, "project.json", `{"providers":{"p":{"type":"anthropic","api_key":"project-listed"},"q":{"type":"anthropic","api_key":"project-listed"}}}`)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, "{}", global))
	writeAt(t, filepath.Join(repo, ".localcode"), "config.json", withInclude(t,
		`{"providers":{"q":{"type":"anthropic","api_key":"project-own"}}}`, project))

	cfg, _, err := loadAt(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers["p"].APIKey; got != "project-listed" {
		t.Errorf("p = %q, want the project's listing over the home one", got)
	}
	if got := cfg.Providers["q"].APIKey; got != "project-own" {
		t.Errorf("q = %q, want the project's own file over everything it lists", got)
	}
}

// How an entry is spelled: under the home, beside the file that lists it,
// absolute, or named by the environment — and an unset variable naming
// nothing is not an error.
func TestHowAnIncludeEntryIsSpelled(t *testing.T) {
	home, repo := homeAndRepo(t)
	provider := func(name string) string {
		return `{"providers":{"` + name + `":{"type":"anthropic","api_key":"k"}}}`
	}
	writeAt(t, home, "under-home.json", provider("from-home"))
	writeAt(t, filepath.Join(home, "shared"), "beside.json", provider("from-beside"))
	abs := writeAt(t, t.TempDir(), "absolute.json", provider("from-absolute"))
	viaEnv := writeAt(t, t.TempDir(), "via-env.json", provider("from-env"))
	t.Setenv("ZZ_INCLUDED", viaEnv)
	t.Setenv("ZZ_NOT_SET_ANYWHERE", "")
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, "{}",
		"~/under-home.json",
		"../shared/beside.json",
		abs,
		"{env:ZZ_INCLUDED:-}",
		"{env:ZZ_NOT_SET_ANYWHERE:-}",
		"   ",
	))

	cfg, notes, err := loadAt(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"from-home", "from-beside", "from-absolute", "from-env"} {
		if _, ok := cfg.Providers[name]; !ok {
			t.Errorf("provider %q did not arrive, so that spelling of an entry was not read", name)
		}
	}
	if len(notes) != 0 {
		t.Errorf("an entry that was empty after {env:} was substituted was reported: %v", notes)
	}
}

// A listed file that is not there is said and skipped. A config.json
// copied to another machine should not stop localcode because a file it
// lists lives only on the first.
func TestAMissingListedFileIsSaidAndSkipped(t *testing.T) {
	home, repo := homeAndRepo(t)
	missing := filepath.Join(t.TempDir(), "not-there.json")
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, workingLocalcode, missing))

	cfg, notes, err := loadAt(home, repo)
	if err != nil {
		t.Fatalf("a listed file that does not exist stopped localcode: %v", err)
	}
	if cfg.DefaultProfile != "main" {
		t.Errorf("default_profile = %q", cfg.DefaultProfile)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "set aside and not read") || !strings.Contains(joined, "no such file") || !strings.Contains(joined, missing) {
		t.Errorf("nothing useful was said about the missing file: %v", notes)
	}
}

// A listed file carrying keys localcode cannot honour, or one that is not
// JSON at all, is set aside by name. The file that lists it is fine, and
// localcode starts.
func TestAListedFileThatCannotBeReadIsSetAside(t *testing.T) {
	home, repo := homeAndRepo(t)
	dir := t.TempDir()
	plugin := writeAt(t, dir, "plugin.json", `{"plugin":["oh-my-openagent"],`+anOpencodeProvider("from-the-plugin-file", "k")+`}`)
	garbage := writeAt(t, dir, "garbage.json", `{ this is not json`)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, workingLocalcode, plugin, garbage))

	cfg, notes, err := loadAt(home, repo)
	if err != nil {
		t.Fatalf("a file another program wrote stopped localcode: %v", err)
	}
	if cfg.DefaultProfile != "main" {
		t.Errorf("default_profile = %q", cfg.DefaultProfile)
	}
	if _, ok := cfg.Providers["from-the-plugin-file"]; ok {
		t.Error("a provider from a file that was set aside arrived anyway")
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{plugin, "plugin", garbage} {
		if !strings.Contains(joined, want) {
			t.Errorf("the notes never mention %q: %v", want, notes)
		}
	}
}

// One flat list. A listed file that lists another is said and not followed,
// so what was read is always what the first file wrote down.
func TestAnIncludeInsideAListedFileIsNotFollowed(t *testing.T) {
	home, repo := homeAndRepo(t)
	dir := t.TempDir()
	deeper := writeAt(t, dir, "deeper.json", `{"providers":{"deeper":{"type":"anthropic","api_key":"k"}}}`)
	middle := writeAt(t, dir, "middle.json", withInclude(t, `{"providers":{"middle":{"type":"anthropic","api_key":"k"}}}`, deeper))
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, workingLocalcode, middle))

	cfg, notes, err := loadAt(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Providers["middle"]; !ok {
		t.Error("the file that was listed was not read")
	}
	if _, ok := cfg.Providers["deeper"]; ok {
		t.Error("a file listed by a listed file was read")
	}
	if !strings.Contains(strings.Join(notes, "\n"), "is not followed") {
		t.Errorf("nothing said that the nested list was dropped: %v", notes)
	}
}

// Listed twice, or listing itself, is read once and is not worth a line.
func TestAFileListedTwiceOrListingItselfIsReadOnce(t *testing.T) {
	home, repo := homeAndRepo(t)
	shared := writeAt(t, t.TempDir(), "shared.json", `{"providers":{"s":{"type":"anthropic","api_key":"k"}}}`)
	own := filepath.Join(home, ".localcode", "config.json")
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, workingLocalcode, shared, shared, own))

	cfg, notes, err := loadAt(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Providers["s"]; !ok {
		t.Error("the listed file was not read")
	}
	if len(notes) != 0 {
		t.Errorf("a duplicate entry was reported: %v", notes)
	}
}

// Listed twice, under any spelling of one path, a file is one file. Laying
// the same content again changes nothing a Config shows, so what shows a
// second read is what each read says: a file that is missing is said once
// per read.
func TestAFileListedTwiceIsSaidOnceWhenItIsMissing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "not-there.json")
	own := writeAt(t, dir, "config.json", withInclude(t, workingLocalcode, missing, missing, "./not-there.json", "sub/../not-there.json"))

	_, notes, err := Load(own)
	if err != nil {
		t.Fatalf("a missing listed file stopped localcode: %v", err)
	}
	var said int
	for _, n := range notes {
		if strings.Contains(n, "not-there.json") {
			said++
		}
	}
	if said != 1 {
		t.Errorf("one file listed four ways was said %d times: %v", said, notes)
	}
}

// An entry that starts "~/" keeps a backslash as it is. On Unix that is a
// letter in a file name, and the entry that starts "~\" is the one written
// the Windows way. Windows reads both as separators, so it is skipped there.
func TestATildeSlashEntryKeepsABackslashInAUnixFileName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(`a backslash is a separator on Windows, in every entry`)
	}
	home := filepath.Join(string(filepath.Separator), "home", "user")
	got, ok, err := resolveInclude(`~/a\b.json`, filepath.Join(string(filepath.Separator), "base"), home)
	if err != nil || !ok {
		t.Fatalf("ok %v, err %v", ok, err)
	}
	if want := filepath.Join(home, `a\b.json`); got != want {
		t.Errorf("~/a\\b.json = %q, want %q", got, want)
	}
	got, _, _ = resolveInclude(`~\a\b.json`, filepath.Join(string(filepath.Separator), "base"), home)
	if want := filepath.Join(home, "a", "b.json"); got != want {
		t.Errorf("~\\a\\b.json = %q, want %q", got, want)
	}
}

// "~" needs a home directory. Without one only that entry is set aside.
func TestATildeEntryNeedsAHomeDirectory(t *testing.T) {
	dir := t.TempDir()
	own := writeAt(t, dir, "config.json", withInclude(t, workingLocalcode, "~/x.json"))

	cfg, notes, err := loadMergedFrom([]source{{path: own}}, "")
	if err != nil {
		t.Fatalf("an entry that could not be resolved stopped localcode: %v", err)
	}
	if cfg.DefaultProfile != "main" || !strings.Contains(strings.Join(notes, "\n"), "home directory") {
		t.Errorf("default %q, notes %v", cfg.DefaultProfile, notes)
	}
}

// --config names one file, and that file's list is followed like any other.
func TestAFileNamedWithTheConfigFlagIsFollowedToo(t *testing.T) {
	dir := t.TempDir()
	listed := writeAt(t, dir, "listed.json", `{"providers":{"from-listed":{"type":"anthropic","api_key":"k"}}}`)
	own := writeAt(t, dir, "config.json", withInclude(t, workingLocalcode, "listed.json"))
	_ = listed

	cfg, _, err := Load(own)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Providers["from-listed"]; !ok {
		t.Error("a file listed by the --config file was not read")
	}
	if len(cfg.Include) != 0 {
		t.Errorf("Load left the list on the config: %v", cfg.Include)
	}
}

// Claude Code keeps its servers in a file whose block is spelled
// "mcpServers". Listed, that file supplies them.
func TestAClaudeCodeMCPFileCanBeListed(t *testing.T) {
	home, repo := homeAndRepo(t)
	mcp := writeAt(t, t.TempDir(), ".mcp.json", `{
	  "mcpServers": {
	    "files": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]},
	    "docs": {"type": "http", "url": "https://example.com/mcp"}
	  }
	}`)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, workingLocalcode, mcp))

	cfg, notes, err := loadAt(home, repo)
	if err != nil {
		t.Fatalf("a Claude Code .mcp.json was refused: %v", err)
	}
	if got := cfg.MCPServers["files"].Command; got != "npx" {
		t.Errorf("the stdio server's command = %q", got)
	}
	if got := cfg.MCPServers["docs"].URL; got != "https://example.com/mcp" {
		t.Errorf("the http server's url = %q", got)
	}
	if len(notes) != 0 {
		t.Errorf("a file of servers was reported on: %v", notes)
	}
}

// The listing is read from config.json as the loader's input, and only
// the loader's: nothing a writer does to the file touches it.
func TestAWriterLeavesTheListAlone(t *testing.T) {
	dir := t.TempDir()
	path := writeAt(t, dir, "config.json", withInclude(t, workingLocalcode, "~/.config/opencode/opencode.jsonc"))
	if err := SetSmartAgentInFile(path, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Include) != 1 || cfg.Include[0] != "~/.config/opencode/opencode.jsonc" {
		t.Errorf("the include list after a write is %v", cfg.Include)
	}
}

// The list is consumed whether or not anything on it was read. A config
// that lists one missing file has no layers to lay it under, so the file
// is the result itself, and it must not come out still carrying the list.
func TestAMergedConfigCarriesNoListWhateverWasRead(t *testing.T) {
	home, repo := homeAndRepo(t)
	missing := filepath.Join(t.TempDir(), "not-there.json")
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, workingLocalcode, missing))

	cfg, _, err := loadAt(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Include) != 0 {
		t.Errorf("the merged config carries the list %v", cfg.Include)
	}
}

// An empty list is an answer. A file that says the model may run no
// built-in command must not have a list from a file under it survive that.
func TestAnEmptyModelCommandsListClearsOneFromAListedFile(t *testing.T) {
	home, repo := homeAndRepo(t)
	listed := writeAt(t, t.TempDir(), "listed.json", `{"model_commands":["/review","/test"]}`)

	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, `{"model_commands":[]}`, listed))
	cfg, _, err := loadAt(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ModelCommands) != 0 {
		t.Errorf("model_commands = %v, but the file that lists %s said []", cfg.ModelCommands, listed)
	}

	// And without the key the listed file's list stands: absent is not [].
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, "{}", listed))
	cfg, _, err = loadAt(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ModelCommands) != 2 {
		t.Errorf("model_commands = %v, want the listed file's two", cfg.ModelCommands)
	}
}

// The same rule between the global file and a project's, which is where it
// was wrong first: a project that turns the commands off must stay off.
func TestAProjectsEmptyModelCommandsListClearsTheGlobalOne(t *testing.T) {
	home, repo := homeAndRepo(t)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", `{"model_commands":["/review"]}`)
	writeAt(t, filepath.Join(repo, ".localcode"), "config.json", `{"model_commands":[]}`)

	cfg, _, err := loadAt(home, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ModelCommands) != 0 {
		t.Errorf("model_commands = %v, so the project's [] did not turn them off", cfg.ModelCommands)
	}
}

// A backslash after the tilde is a separator wherever this runs: a
// config.json written on Windows is read the same on a Mac.
func TestATildeEntryWithBackslashesMeansTheSameEverywhere(t *testing.T) {
	home, repo := homeAndRepo(t)
	writeAt(t, filepath.Join(home, "sub"), "target.json", `{"providers":{"from-target":{"type":"anthropic","api_key":"k"}}}`)
	for _, entry := range []string{`~\sub\target.json`, `~/sub/target.json`, `~\sub/target.json`} {
		writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, "{}", entry))
		cfg, notes, err := loadAt(home, repo)
		if err != nil {
			t.Fatalf("%s: %v", entry, err)
		}
		if _, ok := cfg.Providers["from-target"]; !ok {
			t.Errorf("%s did not reach the file; notes %v", entry, notes)
		}
	}
}

// The merged result is checked as a whole, because a listed file may lean on
// a provider another file defines. When it is invalid the error says which
// listed files were read, so the defect is not blamed on the file that is
// fine and only lists the others.
func TestAValidationFailureNamesTheListedFilesThatWereRead(t *testing.T) {
	home, repo := homeAndRepo(t)
	listed := writeAt(t, t.TempDir(), "leans.json", `{"profiles":{"broken":{"provider":"nowhere","model":"m"}}}`)
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", withInclude(t, workingLocalcode, listed))

	_, _, err := loadAt(home, repo)
	if err == nil {
		t.Fatal("a profile naming a provider nothing defines was accepted")
	}
	for _, want := range []string{"broken", "nowhere", listed} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}

	_, _, err = Load(filepath.Join(home, ".localcode", "config.json"))
	if err == nil || !strings.Contains(err.Error(), listed) {
		t.Errorf("--config's error does not name the listed file: %v", err)
	}

	// And when nothing was listed there is nothing to add.
	writeAt(t, filepath.Join(home, ".localcode"), "config.json", `{"profiles":{"broken":{"provider":"nowhere","model":"m"}}}`)
	if _, _, err := loadAt(home, repo); err == nil || strings.Contains(err.Error(), "include") {
		t.Errorf("an error with no listed files mentions include: %v", err)
	}
}
