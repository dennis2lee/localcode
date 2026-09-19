package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestReleaseOpencodePermission_BareDecisionString tests that opencode's bare
// decision string at the root {"permission": "ask"} unmarshals into a single
// "*" fallback entry and resolves properly for all tools.
func TestReleaseOpencodePermission_BareDecisionString(t *testing.T) {
	data := `{"permission": "ask"}`
	var cfg Config
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("Unmarshal root bare decision string: %v", err)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate root bare decision string: %v", err)
	}

	if len(cfg.Permissions) != 1 {
		t.Fatalf("Permissions len = %d, want 1", len(cfg.Permissions))
	}
	rule, ok := cfg.Permissions["*"]
	if !ok {
		t.Fatalf("expected '*' fallback rule in Permissions, got %+v", cfg.Permissions)
	}
	if rule.Flat != DecisionAsk {
		t.Errorf("Flat = %q, want %q", rule.Flat, DecisionAsk)
	}

	// Should resolve to "ask" for any tool via the "*" fallback.
	if got := cfg.ResolvePermission("write_file", "src/main.go", false); got != DecisionAsk {
		t.Errorf("ResolvePermission(write_file) = %q, want %q", got, DecisionAsk)
	}
	if got := cfg.ResolvePermission("bash", "echo hello", false); got != DecisionAsk {
		t.Errorf("ResolvePermission(bash) = %q, want %q", got, DecisionAsk)
	}

	// Invalid root shapes (e.g. numbers or booleans) must keep erroring.
	for _, bad := range []string{`{"permission": 42}`, `{"permission": true}`, `{"permission": ["allow"]}`} {
		var badCfg Config
		if err := json.Unmarshal([]byte(bad), &badCfg); err == nil {
			t.Errorf("expected error unmarshaling invalid permission %s, got nil", bad)
		}
	}
}

// TestReleaseOpencodePermission_ObjectOfPatternsOrder tests that opencode's
// object-of-patterns per tool preserves source file order with last-match-winning.
func TestReleaseOpencodePermission_ObjectOfPatternsOrder(t *testing.T) {
	// Case 1: "src/*.go" comes before "*". Because last match wins, "*" wins for src/main.go.
	data1 := `{
		"permission": {
			"edit": {
				"src/*.go": "allow",
				"*": "deny"
			}
		}
	}`
	var cfg1 Config
	if err := json.Unmarshal([]byte(data1), &cfg1); err != nil {
		t.Fatalf("Unmarshal object-of-patterns: %v", err)
	}
	if err := cfg1.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := cfg1.ResolvePermission("edit", "src/main.go", false); got != DecisionDeny {
		t.Errorf("ResolvePermission(edit, src/main.go) with * last = %q, want %q (last match wins)", got, DecisionDeny)
	}

	// Case 2: "*" comes before "src/*.go". Because last match wins, "src/*.go" wins for src/main.go.
	data2 := `{
		"permission": {
			"edit": {
				"*": "deny",
				"src/*.go": "allow"
			}
		}
	}`
	var cfg2 Config
	if err := json.Unmarshal([]byte(data2), &cfg2); err != nil {
		t.Fatalf("Unmarshal object-of-patterns: %v", err)
	}
	if err := cfg2.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := cfg2.ResolvePermission("edit", "src/main.go", false); got != DecisionAllow {
		t.Errorf("ResolvePermission(edit, src/main.go) with src/*.go last = %q, want %q (last match wins)", got, DecisionAllow)
	}
	if got := cfg2.ResolvePermission("edit", "other.txt", false); got != DecisionDeny {
		t.Errorf("ResolvePermission(edit, other.txt) = %q, want %q", got, DecisionDeny)
	}
}

// TestReleaseOpencodePermission_EditAliasDeniesBothEditAndWriteFile tests that
// an opencode "edit" rule covers both localcode "edit" and "write_file".
func TestReleaseOpencodePermission_EditAliasDeniesBothEditAndWriteFile(t *testing.T) {
	data := `{"permission": {"edit": "deny"}}`
	var cfg Config
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if got := cfg.ResolvePermission("edit", "main.go", false); got != DecisionDeny {
		t.Errorf("ResolvePermission(edit) = %q, want %q", got, DecisionDeny)
	}
	if got := cfg.ResolvePermission("write_file", "main.go", false); got != DecisionDeny {
		t.Errorf("ResolvePermission(write_file) = %q, want %q", got, DecisionDeny)
	}
}

// TestReleaseOpencodePermission_ReadAliasDeniesReadFile tests that an opencode
// "read" rule covers localcode's "read_file" tool.
func TestReleaseOpencodePermission_ReadAliasDeniesReadFile(t *testing.T) {
	data := `{"permission": {"read": "deny"}}`
	var cfg Config
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if got := cfg.ResolvePermission("read_file", "main.go", false); got != DecisionDeny {
		t.Errorf("ResolvePermission(read_file) = %q, want %q", got, DecisionDeny)
	}
}

// TestReleaseOpencodePermission_PrecedenceExplicitToolBeatsAliasBothDirections tests
// that a localcode tool name in the config always beats an alias in both directions:
// (alias deny + explicit allow -> allow; alias allow + explicit deny -> deny).
func TestReleaseOpencodePermission_PrecedenceExplicitToolBeatsAliasBothDirections(t *testing.T) {
	// Direction 1: alias deny + explicit allow -> allow
	data1 := `{
		"permission": {
			"edit": "deny",
			"write_file": "allow"
		}
	}`
	var cfg1 Config
	if err := json.Unmarshal([]byte(data1), &cfg1); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := cfg1.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := cfg1.ResolvePermission("write_file", "file.go", false); got != DecisionAllow {
		t.Errorf("Direction 1 ResolvePermission(write_file) = %q, want %q (explicit allow beats alias deny)", got, DecisionAllow)
	}
	if got := cfg1.ResolvePermission("edit", "file.go", false); got != DecisionDeny {
		t.Errorf("Direction 1 ResolvePermission(edit) = %q, want %q", got, DecisionDeny)
	}

	// Direction 2: alias allow + explicit deny -> deny
	data2 := `{
		"permission": {
			"edit": "allow",
			"write_file": "deny"
		}
	}`
	var cfg2 Config
	if err := json.Unmarshal([]byte(data2), &cfg2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := cfg2.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := cfg2.ResolvePermission("write_file", "file.go", false); got != DecisionDeny {
		t.Errorf("Direction 2 ResolvePermission(write_file) = %q, want %q (explicit deny beats alias allow)", got, DecisionDeny)
	}
	if got := cfg2.ResolvePermission("edit", "file.go", false); got != DecisionAllow {
		t.Errorf("Direction 2 ResolvePermission(edit) = %q, want %q", got, DecisionAllow)
	}
}

// TestReleaseOpencodePermission_DenyBiasMultipleAliases tests that when two aliases
// cover the same tool, the strictest decision wins (deny > ask > allow).
func TestReleaseOpencodePermission_DenyBiasMultipleAliases(t *testing.T) {
	// Temporarily register a second alias covering write_file.
	ToolAliases["modify"] = []string{"write_file"}
	t.Cleanup(func() {
		delete(ToolAliases, "modify")
	})

	cases := []struct {
		editDecision   string
		modifyDecision string
		want           Decision
	}{
		{"allow", "deny", DecisionDeny},
		{"deny", "allow", DecisionDeny},
		{"allow", "ask", DecisionAsk},
		{"ask", "allow", DecisionAsk},
		{"deny", "ask", DecisionDeny},
		{"ask", "deny", DecisionDeny},
		{"allow", "allow", DecisionAllow},
	}

	for _, tc := range cases {
		data := `{
			"permission": {
				"edit": "` + tc.editDecision + `",
				"modify": "` + tc.modifyDecision + `"
			}
		}`
		var cfg Config
		if err := json.Unmarshal([]byte(data), &cfg); err != nil {
			t.Fatalf("Unmarshal(%s, %s): %v", tc.editDecision, tc.modifyDecision, err)
		}
		if got := cfg.ResolvePermission("write_file", "file.go", false); got != tc.want {
			t.Errorf("ResolvePermission(write_file) with edit=%s, modify=%s = %q, want %q (strictest wins)",
				tc.editDecision, tc.modifyDecision, got, tc.want)
		}
	}
}

// TestReleaseOpencodePermission_TypoKeyRefusedWithName tests that Validate()
// rejects a permission key that is a typo, and the error message names the key.
func TestReleaseOpencodePermission_TypoKeyRefusedWithName(t *testing.T) {
	data := `{"permission": {"bashh": "allow"}}`
	var cfg Config
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected Validate() to reject typo key 'bashh', got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, `"bashh"`) && !strings.Contains(errMsg, `bashh`) {
		t.Errorf("error message %q should name the typo key 'bashh'", errMsg)
	}
	if !strings.Contains(errMsg, "bash") {
		t.Errorf("error message %q should list accepted tool names including 'bash'", errMsg)
	}
}

// TestReleaseOpencodePermission_IgnoredOpencodeToolsAccepted tests that opencode
// tools localcode does not implement (e.g. webfetch, doom_loop) are accepted at
// load time and change nothing at resolution time.
func TestReleaseOpencodePermission_IgnoredOpencodeToolsAccepted(t *testing.T) {
	data := `{
		"permission": {
			"webfetch": "deny",
			"doom_loop": "ask",
			"external_directory": "allow",
			"todowrite": "deny",
			"question": "ask",
			"lsp": "allow",
			"list": "deny",
			"websearch": "allow"
		}
	}`
	var cfg Config
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("Unmarshal ignored opencode tools: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() unexpectedly failed for known absent opencode tools: %v", err)
	}

	// Localcode tools must resolve cleanly and be unaffected by vacuous rules.
	if got := cfg.ResolvePermission("read_file", "test.go", false); got != DecisionAllow {
		t.Errorf("ResolvePermission(read_file) = %q, want %q", got, DecisionAllow)
	}
	if got := cfg.ResolvePermission("write_file", "test.go", true); got != DecisionAsk {
		t.Errorf("ResolvePermission(write_file) = %q, want %q", got, DecisionAsk)
	}
}

// TestReleaseOpencodePermission_RegressionExistingArrayAndBashSplitting tests that
// existing localcode configs using the array format resolve exactly as they did before,
// including bash segment splitting with &&.
func TestReleaseOpencodePermission_RegressionExistingArrayAndBashSplitting(t *testing.T) {
	data := `{
		"permission": {
			"bash": [
				{"match": "git *", "decision": "allow"},
				{"match": "rm *", "decision": "deny"}
			],
			"write_file": [
				{"match": "dist/*", "decision": "allow"},
				{"match": "src/*", "decision": "deny"}
			]
		}
	}`
	var cfg Config
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// Bash: git status is allowed on its own
	if got := cfg.ResolvePermission("bash", "git status", true); got != DecisionAllow {
		t.Errorf("ResolvePermission(bash, git status) = %q, want %q", got, DecisionAllow)
	}

	// Bash: rm -rf / is denied on its own
	if got := cfg.ResolvePermission("bash", "rm -rf /", true); got != DecisionDeny {
		t.Errorf("ResolvePermission(bash, rm -rf /) = %q, want %q", got, DecisionDeny)
	}

	// Bash: compound command "git status && rm -rf /" must be DENIED because rm is denied.
	// Segment splitting must not be bypassed!
	if got := cfg.ResolvePermission("bash", "git status && rm -rf /", true); got != DecisionDeny {
		t.Errorf("ResolvePermission(bash, compound command) = %q, want %q", got, DecisionDeny)
	}

	// Path tool: write_file
	if got := cfg.ResolvePermission("write_file", "dist/app.js", true); got != DecisionAllow {
		t.Errorf("ResolvePermission(write_file, dist/app.js) = %q, want %q", got, DecisionAllow)
	}
	if got := cfg.ResolvePermission("write_file", "src/main.go", true); got != DecisionDeny {
		t.Errorf("ResolvePermission(write_file, src/main.go) = %q, want %q", got, DecisionDeny)
	}
	// Fallback to static default for unmatched path
	if got := cfg.ResolvePermission("write_file", "docs/index.md", true); got != DecisionAsk {
		t.Errorf("ResolvePermission(write_file, docs/index.md) = %q, want %q", got, DecisionAsk)
	}
}

// TestReleaseOpencodePermission_MatchCommentDoesNotClaimOpencodeGlob verifies
// that rules.go does not describe Match as an opencode-style glob and explains
// that * matches / and that the secret guard depends on it.
func TestReleaseOpencodePermission_MatchCommentDoesNotClaimOpencodeGlob(t *testing.T) {
	data, err := os.ReadFile("rules.go")
	if err != nil {
		t.Fatalf("ReadFile rules.go: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "an opencode-style glob") {
		t.Errorf("rules.go still contains misleading comment claiming Match is 'an opencode-style glob'")
	}
	if !strings.Contains(content, "secretPatterns") && !strings.Contains(content, "secret guard") {
		t.Errorf("rules.go comment should explain that secret guard / secretPatterns depends on * matching /")
	}
}
