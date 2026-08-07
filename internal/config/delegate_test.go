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
		"FIND the config loader",         // case-insensitive
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

// TestSkipPermissionsDefaultsOff: adding the feature must not change any
// existing setup — a config that never mentions it still prompts.
func TestSkipPermissionsDefaultsOff(t *testing.T) {
	cfg := &Config{}
	if cfg.PermissionsSkipped() {
		t.Error("skip_permissions must default to off")
	}
	if got := cfg.ResolvePermission("write_file", "/tmp/x", true); got != DecisionAsk {
		t.Errorf("write_file = %q with no config, want ask", got)
	}
}

// TestSkipPermissionsTurnsAskIntoAllow is the feature itself.
func TestSkipPermissionsTurnsAskIntoAllow(t *testing.T) {
	on := true
	cfg := &Config{SkipPermissions: &on}
	for _, tc := range []struct{ tool, subject string }{
		{"write_file", "/etc/hosts"},
		{"bash", "npm install"},
		{"mcp__github__create_pr", ""},
	} {
		if got := cfg.ResolvePermission(tc.tool, tc.subject, true); got != DecisionAllow {
			t.Errorf("%s(%q) = %q with skip_permissions on, want allow", tc.tool, tc.subject, got)
		}
	}
}

// TestSkipPermissionsStillHonorsDeny is the safety line: skipping
// confirmations must not override a rule written to forbid something.
func TestSkipPermissionsStillHonorsDeny(t *testing.T) {
	on := true
	cfg := &Config{
		SkipPermissions: &on,
		Permissions: map[string]ToolPermission{
			"bash": {Rules: []PermissionRule{{Match: "rm *", Decision: DecisionDeny}}},
		},
	}
	if got := cfg.ResolvePermission("bash", "rm -rf ~", true); got != DecisionDeny {
		t.Errorf("denied command = %q with skip_permissions on, want deny to still win", got)
	}
	if got := cfg.ResolvePermission("bash", "echo hi && rm -rf ~", true); got != DecisionDeny {
		t.Errorf("chained denied command = %q, want deny", got)
	}
}

// Regression: turning skip_permissions on stopped almost nothing.
//
// resolveShellCommand resolves each segment of a command line — and those
// do honour the skip — but then forced the whole line back to "ask"
// whenever it contained a redirect or a substitution, without consulting
// the setting. A `>` matches anywhere in the string, and substitutions
// and redirects are in a large share of what an agent writes, so the
// prompts kept coming for someone who had explicitly asked for none.
func TestSkipPermissionsCoversRedirectsAndSubstitutions(t *testing.T) {
	on := true
	cfg := &Config{SkipPermissions: &on}

	for _, command := range []string{
		"ls > out.txt",
		"echo hi >> log",
		"cat $(which go)",
		"diff <(sort a) <(sort b)",
		"grep -c . file 2>/dev/null",
	} {
		if got := cfg.ResolvePermission(BashToolName, command, true); got != DecisionAllow {
			t.Errorf("ResolvePermission(%q) = %v, want allow with skip_permissions on", command, got)
		}
	}
}

// The safety line is unchanged: an explicit deny still denies, even for a
// command the skip would otherwise wave through.
func TestSkipPermissionsStillDeniesARedirectingCommandThatIsDenied(t *testing.T) {
	on := true
	cfg := &Config{
		SkipPermissions: &on,
		Permissions: map[string]ToolPermission{
			BashToolName: {Rules: []PermissionRule{{Match: "rm *", Decision: DecisionDeny}}},
		},
	}
	if got := cfg.ResolvePermission(BashToolName, "rm -rf tmp > log", true); got != DecisionDeny {
		t.Errorf("a denied command with a redirect resolved to %v, want deny", got)
	}
}

// With the setting off, the construct still forces a prompt — that is the
// behaviour this is protecting, not removing.
func TestUnsafeConstructsStillAskWhenSkipIsOff(t *testing.T) {
	cfg := &Config{
		Permissions: map[string]ToolPermission{
			BashToolName: {Rules: []PermissionRule{{Match: "ls *", Decision: DecisionAllow}}},
		},
	}
	if got := cfg.ResolvePermission(BashToolName, "ls > out.txt", true); got != DecisionAsk {
		t.Errorf("ResolvePermission with skip off = %v, want ask", got)
	}
}
