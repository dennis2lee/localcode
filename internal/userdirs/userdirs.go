// Package userdirs decides which directories a person's skills and custom
// commands are read from.
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
// Two rules, because the two kinds are not alike.
//
// Skills are merged. Every skills directory that exists is read, in the
// order .claude, .opencode, .localcode, and a skill name seen first wins:
// see SkillDirs. A skill is one self-contained directory with a name, so
// two roots holding different skills have nothing to disagree about, and a
// person with Claude Code skills in ~/.claude and opencode skills in
// ~/.opencode wants both. This used to be the other rule, and a person who
// ran another agent once in a repository then found their own skills
// unread, with nothing on screen tying the two together.
//
// Commands and the user-level AGENTS.md and CLAUDE.md are not. The first
// root that exists answers for them outright: .claude, then .opencode,
// then .localcode, and nothing is merged across roots. Two half-loaded
// sets of commands with the same names would be worse than one set that
// is clearly from one place, and global rules are text that is appended to
// the prompt, which has no name to deduplicate by.
//
// Both rules run twice, under two directories: the project being worked
// in and the home directory. A project's own skills and commands still
// win over the global ones, exactly as before. The two places are
// resolved independently, so a repo carrying .claude and a home carrying
// only .localcode is an ordinary arrangement rather than a conflict.
//
// The consequence to know about for the first-root rule is that an empty
// winner still wins. A ~/.claude with no commands directory in it means no
// global commands, rather than a fall through to ~/.localcode. That is why
// At reports the root it chose and which roots lost: startup names them,
// so "my commands disappeared" is one line of output away from its answer
// rather than a mystery.
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

// Order is the search order, as directory names under a home or project
// directory. The last is localcode's own, which is the answer when no
// other root is there.
var Order = []string{".claude", ".opencode", ".localcode"}

// Root is the one root that answers for commands and global rules.
type Root struct {
	// Path is the root that won, e.g. /home/x/.claude. Never empty.
	Path string
	// Commands is the directory of custom commands inside it. It is named
	// whether or not it exists: the loader treats a missing directory as
	// no commands, and naming it means a directory created after startup
	// is found by "/reset-skills" without a code change.
	Commands string
	// Chosen is the bare name of the root (".claude"), for saying where
	// things came from.
	Chosen string
	// Shadowed names the roots further down Order that exist and hold a
	// commands directory, and lost anyway.
	//
	// Empty almost always, and the exception is a repository that carries
	// both .opencode and .localcode: .opencode wins, and a person who ran
	// opencode once in a repository of their own then finds their
	// .localcode/commands silently unread, with nothing on screen tying
	// the two together. First-wins is still the rule for commands — two
	// half-loaded sets of same-named commands would be worse — but losing
	// is worth saying out loud when there was something there to lose.
	// Skills are not here: they are all read.
	Shadowed []string
}

// At resolves the root for custom commands and global rules under dir,
// which is either a home directory or a project directory.
//
// The first directory in Order that exists wins. When none exists — a
// first run, or a project that carries no agent directory at all — the
// answer is .localcode, so the paths point where a person following the
// documentation would put their first command.
func At(dir string) Root {
	for i, name := range Order {
		root := filepath.Join(dir, name)
		if isDir(root) {
			return rootAt(root, name, shadowedBy(dir, Order[i+1:]))
		}
	}
	last := Order[len(Order)-1]
	return rootAt(filepath.Join(dir, last), last, nil)
}

// SkillDirs is every directory a skill is read from, in the order they are
// tried, and the first skill of a given name wins: the project's before the
// home's, so a project skill beats a global one of the same name, and within
// each of those .claude before .opencode before .localcode.
//
// The order inside a place is the order the first-root rule used, so a name
// that resolved to a skill before still resolves to that skill. What is new
// is that the directories that lost then are read now, and their skills are
// added.
//
// Every candidate is named, whether or not it exists: the loader skips a
// missing directory, and a directory created after startup is found by
// "/reset-skills" because this is asked again then.
//
// Once, when the project is the home directory, which it is whenever
// localcode is run in one, and however its path is spelled: see
// SameDirectory. The loader reads a directory once however many times it
// is named, but the log line and the reload report list what they are
// given, and the same three paths twice read as a bug in the line.
func SkillDirs(projectDir, home string) []string {
	places := []string{projectDir}
	if !SameDirectory(projectDir, home) {
		places = append(places, home)
	}
	var out []string
	for _, dir := range places {
		for _, name := range Order {
			out = append(out, filepath.Join(dir, name, "skills"))
		}
	}
	return out
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

// shadowedBy is the roots among rest that exist and have commands in them.
//
// Existing is not enough to be worth a word: a bare .localcode holding
// only a config.json loses nothing by losing, and saying so on every
// start would teach people to ignore the line. Having a commands
// directory in it is the case where something went unread.
func shadowedBy(dir string, rest []string) []string {
	var out []string
	for _, name := range rest {
		root := filepath.Join(dir, name)
		if isDir(root) && isDir(commandsDir(root)) {
			out = append(out, name)
		}
	}
	return out
}

func rootAt(path, name string, shadowed []string) Root {
	return Root{
		Path:     path,
		Commands: commandsDir(path),
		Chosen:   name,
		Shadowed: shadowed,
	}
}

// commandsDir is <root>/commands, except where only <root>/command
// exists: opencode names that directory in the singular, and a root
// chosen for its opencode commands that then looked for a directory
// opencode does not create would find nothing.
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
