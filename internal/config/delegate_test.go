package config

import "testing"

func delegateConfig(match ...string) *Config {
	return &Config{
		Agents: map[string]AgentConfig{
			"general-purpose": {Profile: "big"},
			"explore":         {Profile: "small"},
		},
		Profiles: map[string]Profile{
			"big":   {Provider: "p", Model: "big-model"},
			"small": {Provider: "p", Model: "small-model"},
		},
		Providers:    map[string]ProviderConfig{"p": {Type: ProviderOpenAICompat, BaseURL: "http://x/v1"}},
		AutoDelegate: &AutoDelegateConfig{Enabled: true, Agent: "explore", Match: match},
	}
}

func TestDelegateDisabledByDefault(t *testing.T) {
	if (&Config{}).DelegateEnabled() {
		t.Error("a config with no auto_delegate block should have delegation off — adding the feature must not change existing setups")
	}
	cfg := delegateConfig("find *")
	cfg.AutoDelegate.Enabled = false
	if cfg.DelegateEnabled() {
		t.Error("enabled:false should stay off")
	}
}

func TestMatchesPromptIsCaseInsensitiveGlob(t *testing.T) {
	cfg := delegateConfig("find *", "where is *", "list *")
	for _, prompt := range []string{
		"find the config loader",
		"FIND the config loader", // case-insensitive
		"  where is globMatch defined  ", // surrounding space trimmed
		"list every TODO",
	} {
		if !cfg.AutoDelegate.MatchesPrompt(prompt) {
			t.Errorf("MatchesPrompt(%q) = false, want it delegated", prompt)
		}
	}
	for _, prompt := range []string{
		"refactor the config loader",
		"findings so far?", // "find *" needs a space, so this must not match
		"",
	} {
		if cfg.AutoDelegate.MatchesPrompt(prompt) {
			t.Errorf("MatchesPrompt(%q) = true, want it handled by the main model", prompt)
		}
	}
}

// TestEmptyMatchListDelegatesNothing: a half-written config must be inert
// rather than silently routing every prompt to the cheap model.
func TestEmptyMatchListDelegatesNothing(t *testing.T) {
	cfg := delegateConfig()
	for _, prompt := range []string{"find x", "anything at all", ""} {
		if cfg.AutoDelegate.MatchesPrompt(prompt) {
			t.Errorf("MatchesPrompt(%q) = true with no patterns configured, want false", prompt)
		}
	}
}

func TestNilAutoDelegateMatchesNothing(t *testing.T) {
	var cfg *AutoDelegateConfig
	if cfg.MatchesPrompt("find x") {
		t.Error("a nil AutoDelegateConfig should match nothing, not panic or match")
	}
}

func TestValidateRejectsUnknownDelegateAgent(t *testing.T) {
	cfg := delegateConfig("find *")
	cfg.AutoDelegate.Agent = "nonexistent"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an error for auto_delegate pointing at an unknown agent")
	}

	cfg = delegateConfig("find *")
	cfg.AutoDelegate.Agent = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an error for auto_delegate with no agent")
	}

	if err := delegateConfig("find *").Validate(); err != nil {
		t.Fatalf("a well-formed auto_delegate config should validate, got %v", err)
	}
}
