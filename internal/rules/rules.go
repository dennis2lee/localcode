// Package rules implements opencode-style AGENTS.md project/user rules
// files: a plain Markdown file with build/test/architecture/convention
// notes that gets folded into the system prompt automatically, with
// CLAUDE.md accepted as a compatibility fallback name.
package rules

import (
	"os"
	"path/filepath"
	"strings"
)

const sectionHeader = "Project/user rules:"

var projectNames = []string{"AGENTS.md", "CLAUDE.md"}

// Load finds the nearest project-level rules file (searching cwd and its
// parent directories up to and including the git repo root, or the
// filesystem root if there's no repo) and the global rules file
// (~/.localcode/AGENTS.md, falling back to ~/.claude/CLAUDE.md), and
// returns a system-prompt section combining whichever were found. Returns
// "" if neither exists.
func Load(cwd, home string) string {
	project := findProjectRules(cwd)
	global := findGlobalRules(home)

	if project == "" && global == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString(sectionHeader + "\n\n")
	if project != "" {
		b.WriteString(project)
		b.WriteString("\n\n")
	}
	if global != "" {
		b.WriteString(global)
		b.WriteString("\n")
	}
	return b.String()
}

func findProjectRules(cwd string) string {
	dir := cwd
	for {
		for _, name := range projectNames {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil {
				return string(data)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return "" // reached the repo root without finding one, stop climbing
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached the filesystem root
		}
		dir = parent
	}
}

func findGlobalRules(home string) string {
	for _, p := range []string{
		filepath.Join(home, ".localcode", "AGENTS.md"),
		filepath.Join(home, ".claude", "CLAUDE.md"),
	} {
		if data, err := os.ReadFile(p); err == nil {
			return string(data)
		}
	}
	return ""
}
