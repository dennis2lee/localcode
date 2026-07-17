package rules

import (
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

	got := findProjectRules(sub)
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
	if got := findProjectRules(dir); got != "agents" {
		t.Errorf("expected AGENTS.md to win over CLAUDE.md, got %q", got)
	}
}

func TestFindProjectRulesFallsBackToClaude(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude fallback"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findProjectRules(dir); got != "claude fallback" {
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
	if got := findGlobalRules(home); got != "global claude" {
		t.Errorf("expected ~/.claude/CLAUDE.md fallback, got %q", got)
	}
}
