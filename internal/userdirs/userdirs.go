// Package userdirs decides which home directory a person's skills and
// custom commands are read from.
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
// So one root answers for both kinds, and the first that exists wins
// outright: .claude, then .opencode, then .localcode. Nothing is merged
// across roots. A person with ~/.claude gets their Claude Code skills and
// commands and nothing from the other two, even if the other two also
// have some — which is the point: two sets of half-loaded commands with
// the same names would be worse than one set that is clearly from one
// place.
//
// The same chain runs twice, under two directories: the project being
// worked in and the home directory. A project's own skills and commands
// still win over the global ones, exactly as before; what the chain
// decides is which directory inside each of those two places is read.
// The two are resolved independently, so a repo carrying .claude and a
// home carrying only .localcode is an ordinary arrangement rather than a
// conflict.
//
// The consequence to know about is that an empty winner still wins. A
// ~/.claude with no skills directory in it means no global skills, rather
// than a fall through to ~/.localcode, and a repo whose .claude holds
// only settings shadows its own .localcode/skills the same way. That is
// why At reports the root it chose: startup names both, so "my skills
// disappeared" is one line of output away from its answer rather than a
// mystery.
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

// Root is one resolved home for user-authored assets.
type Root struct {
	// Path is the root that won, e.g. /home/x/.claude. Never empty.
	Path string
	// Skills and Commands are the directories inside it. They are named
	// whether or not they exist: the loaders treat a missing directory as
	// no skills and no commands, and naming them means a directory
	// created after startup is found by "/reset-skills" without a code
	// change.
	Skills   string
	Commands string
	// Chosen is the bare name of the root (".claude"), for saying where
	// things came from.
	Chosen string
}

// At resolves the root for skills and custom commands under dir, which is
// either a home directory or a project directory.
//
// The first directory in Order that exists wins for both kinds. When none
// exists — a first run, or a project that carries no agent directory at
// all — the answer is .localcode, so the paths point where a person
// following the documentation would put their first skill.
func At(dir string) Root {
	for _, name := range Order {
		root := filepath.Join(dir, name)
		if isDir(root) {
			return rootAt(root, name)
		}
	}
	last := Order[len(Order)-1]
	return rootAt(filepath.Join(dir, last), last)
}

func rootAt(path, name string) Root {
	return Root{
		Path:     path,
		Skills:   filepath.Join(path, "skills"),
		Commands: commandsDir(path),
		Chosen:   name,
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
