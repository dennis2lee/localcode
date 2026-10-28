// Package userdirs decides which directories a person's skills, custom
// commands and global rules are read from.
//
// localcode's own configuration stays in ~/.localcode and is not part of
// this. Skills and commands are different: they are file formats other
// agents already use, and localcode reads them as they are. A skill is
// <name>/SKILL.md with YAML frontmatter, which is Claude Code's
// convention; a custom command is <name>.md with frontmatter and a prompt
// body, which is opencode's. Someone who has already written these for
// one of those tools should not have to copy them into a third directory
// to use them here.
//
// Two rules.
//
// Skills and commands are merged. Every skills directory and every commands
// directory that exists is read, and of two with the same name the higher
// one wins, in seven places: the project's .localcode, .claude and
// .opencode, then the home's in the same order, then opencode's own global
// directory, ~/.config/opencode. See MergedOrder, SkillDirs, CommandDirs and
// OpencodeConfigDir. A skill or a command is one self-contained file with a name,
// so two roots holding different ones have nothing to disagree about, and a
// person with Claude Code's in ~/.claude and opencode's in ~/.opencode
// wants both. Commands used to follow the other rule, and a repository
// carrying .opencode had its .localcode/commands unread.
//
// The user-level AGENTS.md and CLAUDE.md are not merged. The first root
// that exists answers for them outright, .claude, then .opencode, then
// .localcode: see At. They are text appended to the prompt, which has no
// name to deduplicate by, and two people's worth of standing rules in one
// prompt would be worse than one set from one place. An empty winner still
// wins, which is why startup names the root it chose.
//
// Both rules run twice, under two directories: the project being worked
// in and the home directory. The project comes first. The two places are
// resolved independently, so a repo carrying .claude and a home carrying
// only .localcode is an ordinary arrangement rather than a conflict.
//
// None of this is platform-specific. The roots are plain directory names
// under a home or a project, joined with filepath.Join, and on Windows
// the home is the one os.UserHomeDir returns from USERPROFILE — so
// C:\Users\me\.claude answers exactly as ~/.claude does elsewhere.
//
// Reading these files grants nothing new. They are the person's own files
// in the person's own home directory, at the same trust level as the ones
// under ~/.localcode, and every gate that applies to a command loaded
// from there — model_invocable, the shell-splice refusal — applies
// unchanged to one loaded from here.
package userdirs

import (
	"os"
	"path/filepath"
)

// Order is the search order for global rules, as directory names under a
// home or project directory. The last is localcode's own, which is the
// answer when no other root is there.
var Order = []string{".claude", ".opencode", ".localcode"}

// MergedOrder is the order skills and commands directories are read in
// inside one place, and so which of two same-named ones wins: localcode's
// own first, then Claude Code's, then opencode's.
//
// localcode's own first because a skill or command put there was written
// for this program. Somebody who keeps one in ~/.claude for Claude Code
// and wants localcode to do the same job differently writes a same-named
// one in ~/.localcode, and that is the one they mean here; under Order it
// lost to the file it was written to replace, and the only way to change
// what localcode ran was to edit the other agent's copy.
var MergedOrder = []string{".localcode", ".claude", ".opencode"}

// Root is the one root that answers for global rules.
type Root struct {
	// Path is the root that won, e.g. /home/x/.claude. Never empty.
	Path string
	// Chosen is the bare name of the root (".claude"), for saying where
	// things came from.
	Chosen string
}

// At resolves the root for global rules under dir, which is either a home
// directory or a project directory.
//
// The first directory in Order that exists wins. When none exists — a
// first run, or a project that carries no agent directory at all — the
// answer is .localcode, so the path points where a person following the
// documentation would put their first file.
func At(dir string) Root {
	for _, name := range Order {
		root := filepath.Join(dir, name)
		if isDir(root) {
			return Root{Path: root, Chosen: name}
		}
	}
	last := Order[len(Order)-1]
	return Root{Path: filepath.Join(dir, last), Chosen: last}
}

// SkillDirs is every directory a skill is read from, in the order they are
// tried, and the first skill of a given name wins: the project's before the
// home's, so a project skill beats a global one of the same name, and within
// each of those MergedOrder, .localcode before .claude before .opencode.
//
// Every candidate is named, whether or not it exists: the loader skips a
// missing directory, and a directory created after startup is found by
// "/reset-skills" because this is asked again then.
func SkillDirs(projectDir, home string) []string {
	var out []string
	for _, root := range mergedRoots(projectDir, home) {
		out = append(out, filepath.Join(root, "skills"))
	}
	return out
}

// CommandDirs is every directory a custom command is read from, in the
// order SkillDirs gives for skills, and the first command of a given name
// wins. Each root contributes its commands directory, which is command/
// where only that exists, as older opencode versions name it.
func CommandDirs(projectDir, home string) []string {
	var out []string
	for _, root := range mergedRoots(projectDir, home) {
		out = append(out, commandsDir(root))
	}
	return out
}

// WorkflowDirs is where saved orchestration workflows are read from, highest
// first: the project's .localcode/workflows, then the home's. A project
// file wins over a home file of the same name. Only .localcode: a workflow
// is a localcode plan, and no other tool's directory holds one. The home
// is left out when it is the project.
func WorkflowDirs(projectDir, home string) []string {
	out := []string{filepath.Join(projectDir, ".localcode", "workflows")}
	if home != "" && !SameDirectory(projectDir, home) {
		out = append(out, filepath.Join(home, ".localcode", "workflows"))
	}
	return out
}

// mergedRoots is the seven roots skills and commands are read from, highest
// first: MergedOrder under the project, then under the home, then
// opencode's own global directory.
//
// Four, when the project is the home directory, which it is whenever
// localcode is run in one, and however its path is spelled: see
// SameDirectory. The loaders read a directory once however many times it
// is named, but the log line and the reload report list what they are
// given, and the same paths twice read as a bug in the line.
func mergedRoots(projectDir, home string) []string {
	places := []string{projectDir}
	if !SameDirectory(projectDir, home) {
		places = append(places, home)
	}
	var out []string
	for _, dir := range places {
		for _, name := range MergedOrder {
			out = append(out, filepath.Join(dir, name))
		}
	}
	return append(out, OpencodeConfigDir(home))
}

// OpencodeConfigDir is opencode's own global directory, where it keeps its
// skills and commands: $XDG_CONFIG_HOME/opencode when that is set to an
// absolute path, else ~/.config/opencode. opencode resolves it that way on
// every system, Windows included, so a person's opencode skills are where
// this says whatever opencode was installed on.
//
// Last of the seven roots. ~/.opencode is the place for opencode's files
// that this program reads, and this is opencode's own: a person who keeps
// a same-named skill in both put the ~/.opencode one there for localcode.
func OpencodeConfigDir(home string) string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "opencode")
	}
	return filepath.Join(home, ".config", "opencode")
}

// SameDirectory reports whether two paths name one directory, however each
// is spelled: a trailing slash, a link to it, a drive letter in the other
// case, a volume that does not tell "Users" from "users". Spelled the same
// is the cheap answer and comes first. After that the file system is
// asked, because it is the only one that knows what its own names mean. A
// path that cannot be examined is not the same as anything else.
func SameDirectory(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// commandsDir is <root>/commands, except where only <root>/command
// exists: opencode names that directory in the singular, and looking only
// for the plural would read a root of opencode commands as empty.
func commandsDir(root string) string {
	plural := filepath.Join(root, "commands")
	if !isDir(plural) {
		if singular := filepath.Join(root, "command"); isDir(singular) {
			return singular
		}
	}
	return plural
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
