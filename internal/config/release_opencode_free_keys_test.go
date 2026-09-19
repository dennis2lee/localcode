package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultAgentAcceptedWhenInAgents(t *testing.T) {
	cfg := validConfig()
	cfg.Agents["reviewer"] = AgentConfig{Profile: "balanced"}
	cfg.DefaultAgent = "reviewer"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid default_agent in agents to be accepted, got: %v", err)
	}
}

func TestDefaultAgentAcceptedForGeneralPurposeWithoutDeclaration(t *testing.T) {
	cfg := validConfig()
	delete(cfg.Agents, "general-purpose")
	cfg.DefaultAgent = "general-purpose"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected default_agent 'general-purpose' to be accepted undeclared, got: %v", err)
	}
}

func TestDefaultAgentRejectedWhenUnknown(t *testing.T) {
	cfg := validConfig()
	cfg.DefaultAgent = "nonexistent-agent"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected unknown default_agent to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "nonexistent-agent") {
		t.Errorf("error %q does not name unknown agent 'nonexistent-agent'", err.Error())
	}
}

func TestResolveProfileFallsBackToDefaultAgent(t *testing.T) {
	cfg := validConfig()
	cfg.Profiles["special"] = Profile{Provider: "anthropic", Model: "claude-special"}
	cfg.Agents["specialist"] = AgentConfig{Profile: "special"}
	cfg.DefaultAgent = "specialist"

	// When agentName is empty, ResolveProfile should fall back to DefaultAgent's profile
	got, err := cfg.ResolveProfile("")
	if err != nil {
		t.Fatalf("ResolveProfile(\"\"): %v", err)
	}
	if got.Model != "claude-special" {
		t.Errorf("ResolveProfile(\"\") = %+v, want model \"claude-special\" from default_agent", got)
	}

	// When agentName is explicitly passed, it resolves that agent
	gotExplicit, err := cfg.ResolveProfile("specialist")
	if err != nil {
		t.Fatalf("ResolveProfile(\"specialist\"): %v", err)
	}
	if gotExplicit.Model != "claude-special" {
		t.Errorf("ResolveProfile(\"specialist\") = %+v, want model \"claude-special\"", gotExplicit)
	}

	// When DefaultAgent is empty, ResolveProfile falls back to DefaultProfile
	cfg.DefaultAgent = ""
	gotDefault, err := cfg.ResolveProfile("")
	if err != nil {
		t.Fatalf("ResolveProfile(\"\") with empty default_agent: %v", err)
	}
	wantDefault := cfg.Profiles[cfg.DefaultProfile]
	if gotDefault.Model != wantDefault.Model {
		t.Errorf("ResolveProfile(\"\") with empty default_agent = %+v, want default_profile %+v", gotDefault, wantDefault)
	}
}

func TestInstructionsValidRelativePathAccepted(t *testing.T) {
	proj := t.TempDir()
	rulesFile := filepath.Join(proj, "extra-rules.md")
	if err := os.WriteFile(rulesFile, []byte("# Extra rules"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := validConfig()
	cfg.Instructions = []string{"extra-rules.md"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid relative instructions path to be accepted, got: %v", err)
	}
}

func TestInstructionsPathEscapeDotDotRejected(t *testing.T) {
	cfg := validConfig()
	cfg.Instructions = []string{"../escape.md"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected '..' escape path to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "../escape.md") {
		t.Errorf("error %q does not name escaping entry '../escape.md'", err.Error())
	}
}

func TestInstructionsAbsoluteOutsideProjectRejected(t *testing.T) {
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.md")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := validConfig()
	cfg.Instructions = []string{outsideFile}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected absolute path outside project to be rejected, got nil error")
	}
	// Quoted with %%q in the message, so a Windows path arrives with its
	// separators escaped and the raw string is not a substring of it.
	// What the refusal has to do is name the entry; this is how it reads
	// the same on both platforms.
	if !strings.Contains(err.Error(), fmt.Sprintf("%q", outsideFile)) {
		t.Errorf("error %q does not name escaping entry %q", err.Error(), outsideFile)
	}
}

func TestInstructionsNetworkURLRejected(t *testing.T) {
	cfg := validConfig()
	cfg.Instructions = []string{"http://example.com/rules.md"}
	errHTTP := cfg.Validate()
	if errHTTP == nil {
		t.Fatal("expected http:// instructions URL to be rejected, got nil error")
	}
	if !strings.Contains(errHTTP.Error(), "http://example.com/rules.md") {
		t.Errorf("error %q does not name URL 'http://example.com/rules.md'", errHTTP.Error())
	}

	cfg.Instructions = []string{"https://example.com/rules.md"}
	errHTTPS := cfg.Validate()
	if errHTTPS == nil {
		t.Fatal("expected https:// instructions URL to be rejected, got nil error")
	}
	if !strings.Contains(errHTTPS.Error(), "https://example.com/rules.md") {
		t.Errorf("error %q does not name URL 'https://example.com/rules.md'", errHTTPS.Error())
	}
}

func TestSubagentDepthValidationAndLimits(t *testing.T) {
	cfg := validConfig()

	// nil subagent_depth evaluates to default limit 3
	if got := cfg.SubagentDepthLimit(); got != 3 {
		t.Errorf("SubagentDepthLimit() with nil = %d, want 3", got)
	}

	// 1 evaluates to limit 1
	one := 1
	cfg.SubagentDepth = &one
	if err := cfg.Validate(); err != nil {
		t.Fatalf("subagent_depth = 1 validation failed: %v", err)
	}
	if got := cfg.SubagentDepthLimit(); got != 1 {
		t.Errorf("SubagentDepthLimit() with 1 = %d, want 1", got)
	}

	// 0 evaluates to limit 0
	zero := 0
	cfg.SubagentDepth = &zero
	if err := cfg.Validate(); err != nil {
		t.Fatalf("subagent_depth = 0 validation failed: %v", err)
	}
	if got := cfg.SubagentDepthLimit(); got != 0 {
		t.Errorf("SubagentDepthLimit() with 0 = %d, want 0", got)
	}

	// Negative is rejected at validation
	neg := -1
	cfg.SubagentDepth = &neg
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected negative subagent_depth to be rejected, got nil error")
	}
}

func TestLoadMergedOverridesFreeKeys(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	globalDepth := 3
	global := validConfig()
	global.Agents["global-agent"] = AgentConfig{Profile: "balanced"}
	global.Agents["proj-agent"] = AgentConfig{Profile: "balanced"}
	global.DefaultAgent = "global-agent"
	global.SubagentDepth = &globalDepth
	global.Instructions = []string{"global-rules.md"}
	writeConfig(t, filepath.Join(home, ".localcode", "config.json"), &global)

	projDepth := 1
	projectCfg := Config{
		DefaultAgent:  "proj-agent",
		SubagentDepth: &projDepth,
		Instructions:  []string{"proj-rules.md"},
	}
	writeConfig(t, filepath.Join(proj, ".localcode", "config.json"), &projectCfg)

	merged, err := LoadMerged(proj)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}

	if merged.DefaultAgent != "proj-agent" {
		t.Errorf("DefaultAgent = %q, want project override %q", merged.DefaultAgent, "proj-agent")
	}
	if merged.SubagentDepth == nil || *merged.SubagentDepth != 1 {
		t.Errorf("SubagentDepth = %v, want project override 1", merged.SubagentDepth)
	}
	if len(merged.Instructions) != 1 || merged.Instructions[0] != "proj-rules.md" {
		t.Errorf("Instructions = %v, want project override [\"proj-rules.md\"]", merged.Instructions)
	}
}
