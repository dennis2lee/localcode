// Package rules implements opencode-style AGENTS.md project/user rules
// files: a plain Markdown file with build/test/architecture/convention
// notes that gets folded into the system prompt automatically, with
// CLAUDE.md accepted as a compatibility fallback name. It also supports
// Claude Code's "@path/to/import" syntax for splicing other files in.
package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const sectionHeader = "Project/user rules:"

// maxImportDepth caps recursive "@path" imports (an imported file can
// itself import others), matching Claude Code's own limit.
const maxImportDepth = 4

var projectNames = []string{"AGENTS.md", "CLAUDE.md"}

// Load finds the nearest project-level rules file (searching dir and its
// parent directories up to and including the git repo root, or the
// filesystem root if there's no repo) and the global rules files
// (~/.localcode/AGENTS.md and ~/.claude/CLAUDE.md), expands any "@path"
// imports in each, and returns a system-prompt section combining whichever
// were found. Returns "" if none exist.
//
// dir is the directory the session is working in, not the process's — two
// sessions in two projects get their own project's rules, and moving a
// session's workspace moves which AGENTS.md/CLAUDE.md it obeys. It is read
// per turn rather than cached at startup, so editing the file takes effect
// on the next message instead of on the next restart.
func Load(dir, home string) string {
	var found []string
	if project, projectDir := findProjectRules(dir); project != "" {
		found = append(found, expandImports(project, projectDir, home, 1))
	}
	for _, g := range findGlobalRules(home) {
		found = append(found, expandImports(g.content, filepath.Dir(g.path), home, 1))
	}
	if len(found) == 0 {
		return ""
	}
	return sectionHeader + "\n\n" + strings.Join(found, "\n\n") + "\n"
}

func findProjectRules(cwd string) (content, dir string) {
	d := cwd
	for {
		for _, name := range projectNames {
			data, err := os.ReadFile(filepath.Join(d, name))
			if err == nil {
				return string(data), d
			}
		}
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return "", "" // reached the repo root without finding one, stop climbing
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", "" // reached the filesystem root
		}
		d = parent
	}
}

// globalFile is one user-level rules file that exists.
type globalFile struct {
	path    string
	content string
}

// findGlobalRules reads every global rules file there is, rather than the
// first of them.
//
// ~/.claude/CLAUDE.md used to be a fallback, reached only when
// ~/.localcode/AGENTS.md did not exist — so anyone who had both (which is
// anyone who set up localcode after using Claude Code) silently lost the
// instructions they had already written. They are two files by two names
// for the same thing, and having one is not a reason to ignore the other.
func findGlobalRules(home string) []globalFile {
	var out []globalFile
	for _, p := range []string{
		filepath.Join(home, ".localcode", "AGENTS.md"),
		filepath.Join(home, ".claude", "CLAUDE.md"),
	} {
		if data, err := os.ReadFile(p); err == nil {
			out = append(out, globalFile{path: p, content: string(data)})
		}
	}
	return out
}

var (
	fencedBlockPattern = regexp.MustCompile("(?s)```.*?```")
	inlineCodePattern  = regexp.MustCompile("`[^`\n]*`")
	importPattern      = regexp.MustCompile(`@(\S+)`)
)

// expandImports replaces "@path/to/file" references in content with that
// file's contents (recursively expanded up to maxImportDepth), resolving
// relative paths against baseDir (the directory of the file containing the
// reference, not the process cwd) and "~/" against home. References inside
// fenced code blocks or inline code spans are left untouched, so
// mentioning a path in backticks doesn't trigger an import. An
// unreadable/missing import is left as literal text rather than erroring —
// a rules file shouldn't break the whole system prompt over a typo.
func expandImports(content, baseDir, home string, depth int) string {
	if depth > maxImportDepth {
		return content
	}
	return withCodeProtected(content, func(text string) string {
		return importPattern.ReplaceAllStringFunc(text, func(match string) string {
			ref := importPattern.FindStringSubmatch(match)[1]
			path := resolveImportPath(ref, baseDir, home)
			data, err := os.ReadFile(path)
			if err != nil {
				return match
			}
			return expandImports(string(data), filepath.Dir(path), home, depth+1)
		})
	})
}

func resolveImportPath(ref, baseDir, home string) string {
	switch {
	case strings.HasPrefix(ref, "~/"):
		return filepath.Join(home, ref[len("~/"):])
	case filepath.IsAbs(ref):
		return ref
	default:
		return filepath.Join(baseDir, ref)
	}
}

// withCodeProtected runs fn over content with fenced code blocks and
// inline code spans temporarily swapped out for placeholders, then
// restores them verbatim in fn's output — so fn never sees "@" references
// that were only ever meant as literal text inside code.
func withCodeProtected(content string, fn func(string) string) string {
	var fenced, inline []string

	withoutFences := fencedBlockPattern.ReplaceAllStringFunc(content, func(m string) string {
		fenced = append(fenced, m)
		return fmt.Sprintf("\x00FENCE%d\x00", len(fenced)-1)
	})
	withoutInline := inlineCodePattern.ReplaceAllStringFunc(withoutFences, func(m string) string {
		inline = append(inline, m)
		return fmt.Sprintf("\x00CODE%d\x00", len(inline)-1)
	})

	out := fn(withoutInline)

	for i, s := range inline {
		out = strings.ReplaceAll(out, fmt.Sprintf("\x00CODE%d\x00", i), s)
	}
	for i, s := range fenced {
		out = strings.ReplaceAll(out, fmt.Sprintf("\x00FENCE%d\x00", i), s)
	}
	return out
}
