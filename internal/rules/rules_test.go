package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCombinesProjectAndGlobal(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()

	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("Use go test ./... to run tests."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".localcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".localcode", "AGENTS.md"), []byte("Always answer in Korean."), 0o644); err != nil {
		t.Fatal(err)
	}

	section := Load(project, home)
	if !strings.Contains(section, "go test ./...") {
		t.Errorf("expected project rules in section, got %q", section)
	}
	if !strings.Contains(section, "Always answer in Korean") {
		t.Errorf("expected global rules in section, got %q", section)
	}
}

func TestLoadEmptyWhenNeitherExists(t *testing.T) {
	if got := Load(t.TempDir(), t.TempDir()); got != "" {
		t.Errorf("expected empty section, got %q", got)
	}
}

func TestFindProjectRulesClimbsToParentUpToGitRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, _ := findProjectRules(sub)
	if got != "root rules" {
		t.Errorf("expected to find root AGENTS.md by climbing, got %q", got)
	}
}

func TestFindProjectRulesPrefersAGENTSOverCLAUDE(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := findProjectRules(dir); got != "agents" {
		t.Errorf("expected AGENTS.md to win over CLAUDE.md, got %q", got)
	}
}

func TestFindProjectRulesFallsBackToClaude(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude fallback"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := findProjectRules(dir); got != "claude fallback" {
		t.Errorf("expected CLAUDE.md fallback, got %q", got)
	}
}

func TestFindGlobalRulesFallsBackToClaudeDir(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "CLAUDE.md"), []byte("global claude"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := findGlobalRules(home); got != "global claude" {
		t.Errorf("expected ~/.claude/CLAUDE.md fallback, got %q", got)
	}
}

func TestExpandImportsSplicesReferencedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docs.md"), []byte("imported content here"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := expandImports("See @docs.md for details.", dir, dir, 1)
	if !strings.Contains(got, "imported content here") {
		t.Errorf("expandImports() = %q, want it to contain the imported file's content", got)
	}
}

func TestExpandImportsRecursive(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("a-content @b.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("b-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := expandImports("@a.md", dir, dir, 1)
	if !strings.Contains(got, "a-content") || !strings.Contains(got, "b-content") {
		t.Errorf("expandImports() = %q, want both levels of import expanded", got)
	}
}

func TestExpandImportsStopsAtMaxDepth(t *testing.T) {
	dir := t.TempDir()
	// A five-file chain (self-contained, each importing the next) exceeds
	// maxImportDepth (4), so the deepest file's content should not appear.
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("f%d.md", i)
		next := fmt.Sprintf("f%d.md", i+1)
		content := fmt.Sprintf("level%d @%s", i, next)
		if i == 4 {
			content = "level4-leaf"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := expandImports("@f0.md", dir, dir, 1)
	if strings.Contains(got, "level4-leaf") {
		t.Errorf("expandImports() = %q, expected the chain to be cut off before the leaf beyond maxImportDepth", got)
	}
}

func TestExpandImportsSkipsCodeBlocksAndSpans(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.md"), []byte("REAL IMPORT"), 0o644); err != nil {
		t.Fatal(err)
	}
	content := "Inline `@real.md` should stay literal.\n\n```\n@real.md\n```\n\nBut this one imports: @real.md"
	got := expandImports(content, dir, dir, 1)

	if strings.Count(got, "REAL IMPORT") != 1 {
		t.Errorf("expandImports() = %q, want exactly one expansion (the one outside code)", got)
	}
	if !strings.Contains(got, "`@real.md`") {
		t.Error("expected the inline code span reference to remain literal")
	}
	if !strings.Contains(got, "```\n@real.md\n```") {
		t.Error("expected the fenced code block reference to remain literal")
	}
}

func TestExpandImportsHomeRelative(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "shared.md"), []byte("shared preferences"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := expandImports("@~/shared.md", t.TempDir(), home, 1)
	if !strings.Contains(got, "shared preferences") {
		t.Errorf("expandImports() = %q, want the ~/ import resolved against home", got)
	}
}

func TestExpandImportsMissingFileLeftLiteral(t *testing.T) {
	dir := t.TempDir()
	got := expandImports("@missing.md", dir, dir, 1)
	if got != "@missing.md" {
		t.Errorf("expandImports() = %q, want the unreadable reference left as literal text", got)
	}
}

func TestLoadExpandsImportsInProjectRules(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()

	if err := os.WriteFile(filepath.Join(project, "docs.md"), []byte("shared build instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("See @docs.md"), 0o644); err != nil {
		t.Fatal(err)
	}

	section := Load(project, home)
	if !strings.Contains(section, "shared build instructions") {
		t.Errorf("Load() = %q, want the AGENTS.md's @docs.md import expanded", section)
	}
}
