package config

import (
	"fmt"
	"path/filepath"
)

// configSources is every file a merged configuration is built from, in the
// order each is laid over the one before it — so the last that exists wins
// a key the others also set.
//
// Four inputs and no lookups, because the order is a decision and the
// places are facts about two programs rather than about this machine. A
// caller hands over the home directory, the project directory and what
// OPENCODE_CONFIG says; whether any of them is there is the loader's
// question, asked afterwards.
//
// The order is the whole of the compatibility promise. opencode's own
// precedence is global, then OPENCODE_CONFIG, then the project file, and
// localcode's is global then project. Interleaving them so that an
// opencode file always sits UNDER the localcode file of the same scope
// keeps both orders intact and makes the important guarantee structural:
// nothing read from an opencode file can change what an existing
// config.json already said. It also means every writer stays where it
// was — "always allow", /smart-agent, `localcode mcp add` all write to
// ~/.localcode/config.json, which is above the file they would otherwise
// have to rewrite, and opencode's files are only ever read.
//
// opencode looks for its project file in the current directory and then
// walks up to the nearest git directory. This does not walk: localcode
// has already decided which directory is the project, and a second
// opinion about that would be a way for the two to disagree about which
// repository is open.
func configSources(home, projectDir, opencodeConfigEnv string) []source {
	var out []source
	theirs := func(parts ...string) { out = append(out, source{path: filepath.Join(parts...), opencode: true}) }
	ours := func(parts ...string) { out = append(out, source{path: filepath.Join(parts...)}) }

	// opencode's global, which it keeps under XDG rather than beside its
	// own dot-directory.
	theirs(home, ".config", "opencode", "opencode.json")
	if opencodeConfigEnv != "" {
		out = append(out, source{path: opencodeConfigEnv, opencode: true})
	}
	ours(home, ".localcode", "config.json")
	// opencode's project file is at the root of the project, not inside
	// .opencode — that directory holds agents, commands and skills.
	theirs(projectDir, "opencode.json")
	ours(projectDir, ".localcode", "config.json")
	return out
}

// source is one file the configuration is built from, and whose it is.
//
// Whose matters for exactly one thing, and it is the difference between
// localcode starting and not. A key localcode cannot honour is refused,
// which is right for a file somebody wrote for localcode: they said
// something localcode will not do, and finding out at startup beats
// finding out from behaviour. It is wrong for a file they wrote for
// another program. opencode's own configs routinely carry lsp, formatter
// and share; refusing those stopped localcode starting on any machine
// that also had opencode installed, with a working localcode config two
// directories away and nothing wrong with it.
//
// So a refusal from their file is said out loud and the file is set
// aside; a refusal from ours still stops everything.
type source struct {
	path     string
	opencode bool
}

// jsoncAlternative is the same path with a .jsonc extension, which is
// what opencode calls a config with comments in it. localcode reads
// comments out of either extension, so the alternative exists only to be
// found: a person who wrote opencode.jsonc because they wanted to
// annotate it should not have their file silently unread.
func jsoncAlternative(path string) string {
	if filepath.Ext(path) == ".json" && filepath.Base(path) != "config.json" {
		return path + "c"
	}
	return ""
}

// bothSpellings is the error for a directory holding opencode.json and
// opencode.jsonc at once.
//
// Refused rather than ordered, for the reason two spellings of one key
// are refused elsewhere in this package: picking a winner means the file
// that lost is read by nobody and says nothing about it, and the author
// of two files with the same name has not decided which one they meant.
func bothSpellings(a, b string) error {
	return fmt.Errorf("%s and %s are both there and are two spellings of one file; keep one of them", a, b)
}
