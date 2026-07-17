// Package memory implements Claude Code-style "auto memory": a
// per-project directory the model reads/writes on its own (via the
// existing read_file/write_file/edit tools — no dedicated Memory tool is
// needed) to accumulate durable notes across sessions, separate from the
// user-authored rules in internal/rules. MEMORY.md is the index, loaded
// into the system prompt every session (capped, like Claude Code's own
// limit, so it stays cheap); topic files it links to are only loaded when
// the model reads them on demand.
package memory

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

const indexFileName = "MEMORY.md"

// Index load limits, matching Claude Code's own auto-memory behavior: the
// first 200 lines or 25KB, whichever comes first.
const (
	maxIndexLines = 200
	maxIndexBytes = 25 * 1024
)

// Dir returns the auto-memory directory for a project: derived from the
// git repo root when projectDir is inside one (so every worktree and
// subdirectory of the same repo shares one memory directory), falling
// back to projectDir itself otherwise, kept under
// ~/.localcode/projects/<slug>/memory/.
func Dir(projectDir, home string) string {
	root := gitRoot(projectDir)
	if root == "" {
		root = projectDir
	}
	return filepath.Join(home, ".localcode", "projects", slugify(root), "memory")
}

func gitRoot(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func slugify(path string) string {
	var b strings.Builder
	lastDash := true // suppresses a leading dash
	for _, r := range path {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// IndexPath returns the path to dir's MEMORY.md index file.
func IndexPath(dir string) string {
	return filepath.Join(dir, indexFileName)
}

// LoadIndex reads dir's MEMORY.md, capped at maxIndexLines lines or
// maxIndexBytes bytes (whichever comes first). Returns "" if the file
// doesn't exist yet (nothing saved so far).
func LoadIndex(dir string) string {
	data, err := os.ReadFile(IndexPath(dir))
	if err != nil {
		return ""
	}
	if len(data) > maxIndexBytes {
		data = data[:maxIndexBytes]
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > maxIndexLines {
		lines = lines[:maxIndexLines]
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// SystemPromptSection describes the auto-memory convention and this
// project's memory directory to the model, plus the current index content
// (if any), so it knows where — and whether — to save/recall notes using
// its ordinary file tools.
func SystemPromptSection(dir, index string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Auto memory: you may keep durable notes across sessions (build commands, debugging insights, conventions, preferences the user states) by reading/writing files under %s with your file tools. %s is the index, loaded into every session — keep it under %d lines / %dKB (one line per entry; move detail into separate topic files under the same directory and link them from the index). Only save something worth recalling in a future session; don't write to it every turn.\n", dir, IndexPath(dir), maxIndexLines, maxIndexBytes/1024)
	if index != "" {
		b.WriteString("\nCurrent memory index (MEMORY.md):\n---\n")
		b.WriteString(index)
		b.WriteString("\n---\n")
	} else {
		b.WriteString("\nNo memory index exists yet.\n")
	}
	return b.String()
}
