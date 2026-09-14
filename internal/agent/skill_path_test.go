package agent

import (
	"path/filepath"
	"testing"
)

// The path-or-name rule beside /skill: a registered name wins first, and
// what is left reads as a path by the same shape the unknown-command
// route uses — a slash or a dot is what a path looks like.
func TestSkillPathShapedArgumentsReadAsPaths(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		path bool
	}{
		{"./scratch.md", true},
		{"notes/scratch.md", true},
		{"/tmp/scratch.md", true},
		{"~/skills/scratch.md", true},
		{"~", true},
		{`..\scratch.md`, true},
		{"scratch.md", true},
		{"pdf-tools", false},
		{"pdf_tools-2", false},
		{"", false},
	} {
		if got := looksLikeSkillPath(tc.arg); got != tc.path {
			t.Errorf("looksLikeSkillPath(%q) = %v, want %v", tc.arg, got, tc.path)
		}
	}
}

// A relative path is a claim about the session's workspace, the way a
// tool's relative path is; ~ expands to home.
func TestSkillPathsResolveAgainstTheSessionWorkspace(t *testing.T) {
	ws := filepath.Join(string(filepath.Separator), "work", "project")
	if got := resolveSkillPathArg("notes/scratch.md", ws); got != filepath.Join(ws, "notes/scratch.md") {
		t.Errorf("relative resolved to %q", got)
	}
	abs := filepath.Join(ws, "scratch.md")
	if got := resolveSkillPathArg(abs, filepath.Join(string(filepath.Separator), "elsewhere")); got != abs {
		t.Errorf("absolute resolved to %q, want it untouched", got)
	}
}
