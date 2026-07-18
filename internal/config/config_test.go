package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
