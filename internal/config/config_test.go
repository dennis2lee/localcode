package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/hooks"
)

func writeConfig(t *testing.T, path string, cfg Config) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func validConfig() Config {
	return Config{
		Providers: map[string]ProviderConfig{
			"local": {Type: ProviderOpenAICompat, BaseURL: "http://localhost:1234/v1"},
		},
		Profiles: map[string]Profile{
			"balanced": {Provider: "local", Model: "test-model"},
		},
		Agents: map[string]AgentConfig{
			"general-purpose": {Profile: "balanced"},
		},
		DefaultProfile: "balanced",
	}
}

func TestValidateOK(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected a valid config to pass, got: %v", err)
	}
}

func TestValidateUnknownDefaultProfile(t *testing.T) {
	cfg := validConfig()
	cfg.DefaultProfile = "does-not-exist"
	if err := cfg.Validate(); err == nil {
		t.Error("expected an error for an unknown default_profile")
	}
}

func TestValidateProfileReferencesUnknownProvider(t *testing.T) {
	cfg := validConfig()
	cfg.Profiles["balanced"] = Profile{Provider: "ghost", Model: "x"}
	if err := cfg.Validate(); err == nil {
		t.Error("expected an error when a profile references an unknown provider")
	}
}

func TestValidateAgentReferencesUnknownProfile(t *testing.T) {
	cfg := validConfig()
	cfg.Agents["general-purpose"] = AgentConfig{Profile: "ghost"}
	if err := cfg.Validate(); err == nil {
		t.Error("expected an error when an agent references an unknown profile")
	}
}

func TestResolveProfileByAgentName(t *testing.T) {
	cfg := validConfig()
	cfg.Profiles["cheap"] = Profile{Provider: "local", Model: "cheap-model"}
	cfg.Agents["explore"] = AgentConfig{Profile: "cheap"}

	p, err := cfg.ResolveProfile("explore")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if p.Model != "cheap-model" {
		t.Errorf("model = %q, want %q", p.Model, "cheap-model")
	}
}

func TestResolveProfileFallsBackToDefault(t *testing.T) {
	cfg := validConfig()
	p, err := cfg.ResolveProfile("some-unmapped-agent")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if p.Model != "test-model" {
		t.Errorf("model = %q, want default profile's %q", p.Model, "test-model")
	}
}

func TestResolveProfileNoDefaultAndUnmappedAgent(t *testing.T) {
	cfg := validConfig()
	cfg.DefaultProfile = ""
	if _, err := cfg.ResolveProfile("some-unmapped-agent"); err == nil {
		t.Error("expected an error with no default_profile and an unmapped agent")
	}
}

func TestResolveProvider(t *testing.T) {
	cfg := validConfig()
	p, _ := cfg.ResolveProfile("general-purpose")
	pc, err := cfg.ResolveProvider(p)
	if err != nil {
		t.Fatalf("ResolveProvider: %v", err)
	}
	if pc.BaseURL != "http://localhost:1234/v1" {
		t.Errorf("base_url = %q, want %q", pc.BaseURL, "http://localhost:1234/v1")
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := validConfig()
	cfg.DefaultProfile = "ghost"
	writeConfig(t, path, cfg)

	if _, err := Load(path); err == nil {
		t.Error("expected Load to reject an invalid config")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("/nonexistent/config.json"); err == nil {
		t.Error("expected an error loading a nonexistent config file")
	}
}

// TestLoadMergedProjectOverridesGlobal confirms a project-local
// .localcode/config.json takes priority over the global one for entries
// that appear in both, while entries unique to the global config survive
// the merge.
func TestLoadMergedProjectOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	global := Config{
		Providers: map[string]ProviderConfig{
			"local": {Type: ProviderOpenAICompat, BaseURL: "http://global:1234/v1"},
		},
		Profiles: map[string]Profile{
			"balanced": {Provider: "local", Model: "global-model"},
			"cheap":    {Provider: "local", Model: "global-cheap-model"},
		},
		Agents: map[string]AgentConfig{
			"general-purpose": {Profile: "balanced"},
		},
		DefaultProfile: "balanced",
	}
	writeConfig(t, filepath.Join(home, ".localcode", "config.json"), global)

	projectCfg := Config{
		Profiles: map[string]Profile{
			"balanced": {Provider: "local", Model: "project-model"}, // overrides global's "balanced"
		},
	}
	writeConfig(t, filepath.Join(project, ".localcode", "config.json"), projectCfg)

	merged, err := LoadMerged(project)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}

	if merged.Profiles["balanced"].Model != "project-model" {
		t.Errorf("balanced.model = %q, want project override %q", merged.Profiles["balanced"].Model, "project-model")
	}
	if merged.Profiles["cheap"].Model != "global-cheap-model" {
		t.Errorf("cheap.model = %q, want surviving global value %q", merged.Profiles["cheap"].Model, "global-cheap-model")
	}
	if merged.DefaultProfile != "balanced" {
		t.Errorf("default_profile = %q, want %q (from global, unset in project)", merged.DefaultProfile, "balanced")
	}
}

func TestLoadMergedOnlyGlobalExists(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir() // no .localcode/config.json here
	t.Setenv("HOME", home)

	writeConfig(t, filepath.Join(home, ".localcode", "config.json"), validConfig())

	merged, err := LoadMerged(project)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if merged.DefaultProfile != "balanced" {
		t.Errorf("expected the global config to load standalone, got %+v", merged)
	}
}

func TestLoadMergedNeitherExists(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := LoadMerged(project); err == nil {
		t.Error("expected an error when neither global nor project config exists")
	}
}

func TestMemoryEnabledDefaultsTrue(t *testing.T) {
	c := &Config{}
	if !c.MemoryEnabled() {
		t.Error("MemoryEnabled() should default to true when AutoMemoryEnabled is unset")
	}
}

func TestMemoryEnabledExplicitFalse(t *testing.T) {
	disabled := false
	c := &Config{AutoMemoryEnabled: &disabled}
	if c.MemoryEnabled() {
		t.Error("MemoryEnabled() should be false when AutoMemoryEnabled is explicitly false")
	}
}

func TestMergeCarriesAutoMemoryEnabledFromProject(t *testing.T) {
	disabled := false
	base := &Config{}
	base.merge(&Config{AutoMemoryEnabled: &disabled})
	if base.MemoryEnabled() {
		t.Error("expected the project override's AutoMemoryEnabled=false to win after merge")
	}
}

func TestCompactEnabledDefaultsTrue(t *testing.T) {
	c := &Config{}
	if !c.CompactEnabled() {
		t.Error("CompactEnabled() should default to true when AutoCompactEnabled is unset")
	}
}

func TestCompactEnabledExplicitFalse(t *testing.T) {
	disabled := false
	c := &Config{AutoCompactEnabled: &disabled}
	if c.CompactEnabled() {
		t.Error("CompactEnabled() should be false when AutoCompactEnabled is explicitly false")
	}
}

func TestTPSEnabledDefaultsTrue(t *testing.T) {
	c := &Config{}
	if !c.TPSEnabled() {
		t.Error("TPSEnabled() should default to true when ShowTPS is unset")
	}
}

func TestTPSEnabledExplicitFalse(t *testing.T) {
	disabled := false
	c := &Config{ShowTPS: &disabled}
	if c.TPSEnabled() {
		t.Error("TPSEnabled() should be false when ShowTPS is explicitly false")
	}
}

func TestMergeCarriesAutoCompactAndShowTPS(t *testing.T) {
	disabled := false
	base := &Config{}
	base.merge(&Config{AutoCompactEnabled: &disabled, ShowTPS: &disabled})
	if base.CompactEnabled() {
		t.Error("expected the project override's AutoCompactEnabled=false to win after merge")
	}
	if base.TPSEnabled() {
		t.Error("expected the project override's ShowTPS=false to win after merge")
	}
}

func TestValidateRejectsUnknownHookEvent(t *testing.T) {
	c := &Config{Hooks: hooks.Config{"not_a_real_event": {{Command: "true"}}}}
	if err := c.Validate(); err == nil {
		t.Error("expected an error for an unknown hook event name")
	}
}

func TestValidateAcceptsKnownHookEvents(t *testing.T) {
	c := &Config{Hooks: hooks.Config{
		hooks.EventPreToolUse:       {{Command: "true"}},
		hooks.EventPostToolUse:      {{Command: "true"}},
		hooks.EventUserPromptSubmit: {{Command: "true"}},
		hooks.EventStop:             {{Command: "true"}},
		hooks.EventSessionStart:     {{Command: "true"}},
	}}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for all-known hook events", err)
	}
}

func TestMergeHooksOverridesPerEvent(t *testing.T) {
	base := &Config{Hooks: hooks.Config{
		hooks.EventPreToolUse: {{Command: "global-pre"}},
		hooks.EventStop:       {{Command: "global-stop"}},
	}}
	base.merge(&Config{Hooks: hooks.Config{
		hooks.EventPreToolUse: {{Command: "project-pre"}},
	}})

	if len(base.Hooks[hooks.EventPreToolUse]) != 1 || base.Hooks[hooks.EventPreToolUse][0].Command != "project-pre" {
		t.Errorf("pre_tool_use hooks = %+v, want the project override to replace the global list", base.Hooks[hooks.EventPreToolUse])
	}
	if len(base.Hooks[hooks.EventStop]) != 1 || base.Hooks[hooks.EventStop][0].Command != "global-stop" {
		t.Errorf("stop hooks = %+v, want the global list untouched since the project didn't override it", base.Hooks[hooks.EventStop])
	}
}

func TestLoadFileReturnsEmptyConfigWhenMissing(t *testing.T) {
	cfg, err := LoadFile(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg == nil || len(cfg.MCPServers) != 0 {
		t.Errorf("cfg = %+v, want a non-nil empty Config", cfg)
	}
}

func TestUpdateMCPServersInFileCreatesNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")

	err := UpdateMCPServersInFile(path, func(servers map[string]MCPServerConfig) {
		servers["github"] = MCPServerConfig{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}, Env: map[string]string{"GITHUB_TOKEN": "abc"}}
	})
	if err != nil {
		t.Fatalf("UpdateMCPServersInFile: %v", err)
	}

	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got.MCPServers["github"].Command != "npx" {
		t.Errorf("MCPServers = %+v, want the github entry", got.MCPServers)
	}
	if got.MCPServers["github"].Env["GITHUB_TOKEN"] != "abc" {
		t.Errorf("env = %+v, want GITHUB_TOKEN=abc", got.MCPServers["github"].Env)
	}
}

func TestUpdateMCPServersInFilePreservesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{
  "default_profile": "main",
  "some_future_field": {"nested": [1, 2, 3]},
  "mcp_servers": {"old": {"command": "echo"}}
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err := UpdateMCPServersInFile(path, func(servers map[string]MCPServerConfig) {
		servers["new"] = MCPServerConfig{Command: "npx"}
	})
	if err != nil {
		t.Fatalf("UpdateMCPServersInFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, want := range []string{`"some_future_field"`, `"nested"`, `"default_profile"`, `"main"`, `"old"`, `"new"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("rewritten config = %s, want it to still contain %s", data, want)
		}
	}
}

func TestUpdateMCPServersInFileRemovingLastServerDropsKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"mcp_servers": {"x": {"command": "echo"}}, "show_tps": false}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err := UpdateMCPServersInFile(path, func(servers map[string]MCPServerConfig) {
		delete(servers, "x")
	})
	if err != nil {
		t.Fatalf("UpdateMCPServersInFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "mcp_servers") {
		t.Errorf("rewritten config = %s, want mcp_servers key dropped when empty", data)
	}
	if !strings.Contains(string(data), `"show_tps"`) {
		t.Errorf("rewritten config = %s, want other keys kept", data)
	}
}

func TestUpdateMCPServersInFileRejectsInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err := UpdateMCPServersInFile(path, func(servers map[string]MCPServerConfig) {})
	if err == nil {
		t.Fatal("expected an error for invalid JSON, got nil")
	}
	data, _ := os.ReadFile(path)
	if string(data) != `{not json` {
		t.Errorf("file = %s, want the broken original left untouched", data)
	}
}
