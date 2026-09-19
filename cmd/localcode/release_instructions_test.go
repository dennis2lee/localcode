package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/config"
)

func TestInstructionsAppendedInOrderAfterBaseRules(t *testing.T) {
	proj := t.TempDir()
	home := t.TempDir()

	agentsPath := filepath.Join(proj, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("Base project rule from AGENTS.md"), 0o644); err != nil {
		t.Fatal(err)
	}

	inst1Path := filepath.Join(proj, "step1.txt")
	if err := os.WriteFile(inst1Path, []byte("First instruction content"), 0o644); err != nil {
		t.Fatal(err)
	}

	inst2Path := filepath.Join(proj, "step2.txt")
	if err := os.WriteFile(inst2Path, []byte("Second instruction content"), 0o644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(proj, "extra")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inst3Path := filepath.Join(subDir, "step3.txt")
	if err := os.WriteFile(inst3Path, []byte("Third instruction content"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Instructions: []string{
			"step1.txt",
			"step2.txt",
			"extra/*.txt",
		},
	}

	got := loadWorkspaceRules(proj, home, cfg)

	for _, want := range []string{
		"Base project rule from AGENTS.md",
		"First instruction content",
		"Second instruction content",
		"Third instruction content",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("loadWorkspaceRules output missing %q; full text:\n%s", want, got)
		}
	}

	idxBase := strings.Index(got, "Base project rule from AGENTS.md")
	idx1 := strings.Index(got, "First instruction content")
	idx2 := strings.Index(got, "Second instruction content")
	idx3 := strings.Index(got, "Third instruction content")

	if !(idxBase < idx1 && idx1 < idx2 && idx2 < idx3) {
		t.Errorf("instructions not appended in order: idxBase=%d, idx1=%d, idx2=%d, idx3=%d",
			idxBase, idx1, idx2, idx3)
	}
}

func TestInstructionsUnmatchedPatternsLogNoticeWithoutFailure(t *testing.T) {
	proj := t.TempDir()
	home := t.TempDir()

	var buf bytes.Buffer
	origWriter := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
	})

	cfg := &config.Config{
		Instructions: []string{
			"nonexistent-*.md",
			"missing.txt",
		},
	}

	// Must not panic or return an error/failure
	got := loadWorkspaceRules(proj, home, cfg)
	if got != "" {
		t.Errorf("expected empty rules when base and instructions are empty, got %q", got)
	}

	logged := buf.String()
	if !strings.Contains(logged, "nonexistent-*.md") || !strings.Contains(logged, "matched no files") {
		t.Errorf("log notice missing for pattern 'nonexistent-*.md'; logged:\n%s", logged)
	}
	if !strings.Contains(logged, "missing.txt") || !strings.Contains(logged, "matched no files") {
		t.Errorf("log notice missing for pattern 'missing.txt'; logged:\n%s", logged)
	}
}

func TestExplicitAgentFlagOverridesDefaultAgent(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	cfgContent := `{
		"providers": {"test": {"type": "anthropic", "api_key": "k"}},
		"profiles": {"p": {"provider": "test", "model": "m"}},
		"default_profile": "p",
		"agents": {
			"coder": {"profile": "p"},
			"reviewer": {"profile": "p"}
		},
		"default_agent": "coder"
	}`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Case 1: Flag not explicitly set -> uses default_agent "coder"
	agentImplicit := determineActiveAgent(cfgPath, "general-purpose", false)
	if agentImplicit != "coder" {
		t.Errorf("determineActiveAgent without explicit flag = %q, want %q from default_agent", agentImplicit, "coder")
	}

	// Case 2: Flag explicitly set to "reviewer" -> overrides default_agent "coder"
	agentExplicit := determineActiveAgent(cfgPath, "reviewer", true)
	if agentExplicit != "reviewer" {
		t.Errorf("determineActiveAgent with explicit flag = %q, want explicit override %q", agentExplicit, "reviewer")
	}

	// Case 3: Empty config without default_agent, flag not explicit -> falls back to "general-purpose"
	emptyCfgPath := filepath.Join(t.TempDir(), "config.json")
	emptyCfgContent := `{
		"providers": {"test": {"type": "anthropic", "api_key": "k"}},
		"profiles": {"p": {"provider": "test", "model": "m"}},
		"default_profile": "p"
	}`
	if err := os.WriteFile(emptyCfgPath, []byte(emptyCfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	agentDefault := determineActiveAgent(emptyCfgPath, "general-purpose", false)
	if agentDefault != "general-purpose" {
		t.Errorf("determineActiveAgent with no default_agent = %q, want %q", agentDefault, "general-purpose")
	}
}
