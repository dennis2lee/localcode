package config

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestAgentToolsShapeDecoding verifies that:
// 1. Array tools decode into Tools slice.
// 2. Object tools decode into ToolSwitches map.
// 3. Duplicate tools keys within the same agent definition are rejected.
func TestAgentToolsShapeDecoding(t *testing.T) {
	// Array shape
	arrJSON := `{"profile":"smart","tools":["read_file","grep"]}`
	var arrCfg AgentConfig
	if err := json.Unmarshal([]byte(arrJSON), &arrCfg); err != nil {
		t.Fatalf("Unmarshal array tools: %v", err)
	}
	if len(arrCfg.Tools) != 2 || arrCfg.Tools[0] != "read_file" || arrCfg.Tools[1] != "grep" {
		t.Errorf("expected Tools [read_file grep], got %v", arrCfg.Tools)
	}
	if arrCfg.ToolSwitches != nil {
		t.Errorf("expected nil ToolSwitches for array tools, got %v", arrCfg.ToolSwitches)
	}

	// Switch map shape (opencode style)
	mapJSON := `{"profile":"smart","tools":{"write":false,"bash":true}}`
	var mapCfg AgentConfig
	if err := json.Unmarshal([]byte(mapJSON), &mapCfg); err != nil {
		t.Fatalf("Unmarshal switch map tools: %v", err)
	}
	if len(mapCfg.Tools) != 0 {
		t.Errorf("expected empty Tools for map tools, got %v", mapCfg.Tools)
	}
	if mapCfg.ToolSwitches == nil || mapCfg.ToolSwitches["write"] != false || mapCfg.ToolSwitches["bash"] != true {
		t.Errorf("expected ToolSwitches {write: false, bash: true}, got %v", mapCfg.ToolSwitches)
	}

	// Duplicate "tools" key in the same agent object must be rejected
	dupJSON := `{"profile":"smart","tools":["read_file"],"tools":{"write":false}}`
	var dupCfg AgentConfig
	if err := json.Unmarshal([]byte(dupJSON), &dupCfg); err == nil {
		t.Error("expected error for duplicate tools key in agent definition, got nil")
	}
}

// TestAgentPermissionAliases verifies that an agent with permission {"edit": "deny"}
// is denied both edit and write_file (via aliases), while another agent is unaffected.
func TestAgentPermissionAliases(t *testing.T) {
	c := &Config{
		Agents: map[string]AgentConfig{
			"plan": {
				Profile: "smart",
				Permission: Permissions{
					"edit": {Flat: DecisionDeny},
				},
			},
			"build": {
				Profile: "smart",
			},
		},
	}

	ctxPlan := WithAgent(context.Background(), "plan")
	ctxBuild := WithAgent(context.Background(), "build")

	// For plan agent: "edit" is explicitly denied
	if got := c.ResolvePermissionFor(ctxPlan, "edit", "main.go", true); got != DecisionDeny {
		t.Errorf("plan edit: got %q, want %q", got, DecisionDeny)
	}
	// For plan agent: "write_file" is aliased to "edit", so it must also be denied
	if got := c.ResolvePermissionFor(ctxPlan, "write_file", "main.go", true); got != DecisionDeny {
		t.Errorf("plan write_file (aliased to edit): got %q, want %q", got, DecisionDeny)
	}

	// For build agent: unaffected, defaults apply (write_file defaults to ask or allow based on static)
	if got := c.ResolvePermissionFor(ctxBuild, "write_file", "main.go", true); got == DecisionDeny {
		t.Errorf("build write_file: unexpectedly denied (%q)", got)
	}
}

// TestAgentPermissionPrecedence verifies that:
// 1. Global deny + agent allow -> agent is allowed, other agents are denied.
// 2. Global allow + agent deny -> agent is denied, other agents are allowed.
// 3. skip_permissions cannot bypass an explicit deny from either agent or global.
func TestAgentPermissionPrecedence(t *testing.T) {
	c := &Config{
		Permissions: Permissions{
			"bash":      {Flat: DecisionDeny},
			"read_file": {Flat: DecisionAllow},
		},
		Agents: map[string]AgentConfig{
			"runner": {
				Profile: "smart",
				Permission: Permissions{
					"bash": {Flat: DecisionAllow},
				},
			},
			"restricted": {
				Profile: "smart",
				Permission: Permissions{
					"read_file": {Flat: DecisionDeny},
				},
			},
			"standard": {
				Profile: "smart",
			},
		},
	}

	ctxRunner := WithAgent(context.Background(), "runner")
	ctxRestricted := WithAgent(context.Background(), "restricted")
	ctxStandard := WithAgent(context.Background(), "standard")

	// Global deny + agent allow -> runner allowed, standard denied
	if got := c.ResolvePermissionFor(ctxRunner, "bash", "echo hello", true); got != DecisionAllow {
		t.Errorf("runner bash: got %q, want %q (agent allow should override global deny)", got, DecisionAllow)
	}
	if got := c.ResolvePermissionFor(ctxStandard, "bash", "echo hello", true); got != DecisionDeny {
		t.Errorf("standard bash: got %q, want %q (global deny applies)", got, DecisionDeny)
	}

	// Global allow + agent deny -> restricted denied, standard allowed
	if got := c.ResolvePermissionFor(ctxRestricted, "read_file", "secret.txt", false); got != DecisionDeny {
		t.Errorf("restricted read_file: got %q, want %q (agent deny should override global allow)", got, DecisionDeny)
	}
	if got := c.ResolvePermissionFor(ctxStandard, "read_file", "secret.txt", false); got != DecisionAllow {
		t.Errorf("standard read_file: got %q, want %q (global allow applies)", got, DecisionAllow)
	}

	// skip_permissions cannot bypass an explicit deny from either agent or global
	skip := true
	c.SkipPermissions = &skip
	// Agent deny with SkipPermissions: still deny
	if got := c.ResolvePermissionFor(ctxRestricted, "read_file", "secret.txt", false); got != DecisionDeny {
		t.Errorf("restricted read_file with SkipPermissions: got %q, want %q", got, DecisionDeny)
	}
	// Global deny with SkipPermissions: still deny
	if got := c.ResolvePermissionFor(ctxStandard, "bash", "echo hello", true); got != DecisionDeny {
		t.Errorf("standard bash with SkipPermissions: got %q, want %q", got, DecisionDeny)
	}
}

// TestAgentContextPinning verifies that context pinned via WithAgent retains
// the running agent's identity and permissions even if DefaultAgent is mutated.
func TestAgentContextPinning(t *testing.T) {
	c := &Config{
		DefaultAgent: "standard",
		Agents: map[string]AgentConfig{
			"standard": {
				Profile: "smart",
				Permission: Permissions{
					"edit": {Flat: DecisionAllow},
				},
			},
			"plan": {
				Profile: "smart",
				Permission: Permissions{
					"edit": {Flat: DecisionDeny},
				},
			},
		},
	}

	ctx := WithAgent(context.Background(), "plan")

	// Pinned context should resolve as "plan"
	if name, ok := AgentPinned(ctx); !ok || name != "plan" {
		t.Fatalf("AgentPinned(ctx) = (%q, %v), want (plan, true)", name, ok)
	}
	if got := c.ResolvePermissionFor(ctx, "edit", "file.go", true); got != DecisionDeny {
		t.Errorf("ResolvePermissionFor pinned plan: got %q, want %q", got, DecisionDeny)
	}

	// Mutating DefaultAgent does not change pinned context resolution
	c.DefaultAgent = "standard"
	if got := c.ResolvePermissionFor(ctx, "edit", "file.go", true); got != DecisionDeny {
		t.Errorf("after mutating DefaultAgent, ResolvePermissionFor pinned plan: got %q, want %q", got, DecisionDeny)
	}
}

// TestAgentRegressionResolvesIdentical verifies that a configuration without
// per-agent rules resolves identical decisions between ResolvePermission and
// ResolvePermissionFor.
func TestAgentRegressionResolvesIdentical(t *testing.T) {
	c := &Config{
		Permissions: Permissions{
			"bash": {Flat: DecisionAsk},
			"edit": {Flat: DecisionDeny},
		},
		Agents: map[string]AgentConfig{
			"default": {
				Profile: "smart",
			},
		},
	}

	ctx := WithAgent(context.Background(), "default")

	for _, tool := range []string{"bash", "edit", "read_file", "glob"} {
		want := c.ResolvePermission(tool, "arg", true)
		got := c.ResolvePermissionFor(ctx, tool, "arg", true)
		if got != want {
			t.Errorf("tool %q: ResolvePermissionFor = %q, ResolvePermission = %q", tool, got, want)
		}
	}
}

// TestAgentStepsAndMaxSteps verifies parsing, agreement, disagreement, and validation of steps/maxSteps.
func TestAgentStepsAndMaxSteps(t *testing.T) {
	// steps only
	var cfg1 AgentConfig
	if err := json.Unmarshal([]byte(`{"profile":"p","steps":15}`), &cfg1); err != nil {
		t.Fatalf("Unmarshal steps: %v", err)
	}
	if cfg1.Steps != 15 {
		t.Errorf("cfg1.Steps = %d, want 15", cfg1.Steps)
	}

	// maxSteps alias
	var cfg2 AgentConfig
	if err := json.Unmarshal([]byte(`{"profile":"p","maxSteps":20}`), &cfg2); err != nil {
		t.Fatalf("Unmarshal maxSteps: %v", err)
	}
	if cfg2.Steps != 20 {
		t.Errorf("cfg2.Steps = %d, want 20", cfg2.Steps)
	}

	// steps and maxSteps agreeing
	var cfg3 AgentConfig
	if err := json.Unmarshal([]byte(`{"profile":"p","steps":25,"maxSteps":25}`), &cfg3); err != nil {
		t.Fatalf("Unmarshal agreeing: %v", err)
	}
	if cfg3.Steps != 25 {
		t.Errorf("cfg3.Steps = %d, want 25", cfg3.Steps)
	}

	// steps and maxSteps disagreeing
	var cfg4 AgentConfig
	if err := json.Unmarshal([]byte(`{"profile":"p","steps":10,"maxSteps":20}`), &cfg4); err == nil {
		t.Error("expected error for disagreeing steps and maxSteps, got nil")
	}

	// Negative steps rejected in Validate()
	c := &Config{
		Providers: map[string]ProviderConfig{"test": {Type: ProviderAnthropic}},
		Profiles:  map[string]Profile{"smart": {Provider: "test", Model: "m"}},
		Agents: map[string]AgentConfig{
			"bad": {
				Profile: "smart",
				Steps:   -1,
			},
		},
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "which is negative") {
		t.Errorf("expected validation error for negative steps, got: %v", err)
	}
}

// TestCompatPassesThroughPerAgentFields verifies that ConvertOpenCodeToLocalCode
// preserves agent tools switches, agent permission, and agent steps/maxSteps.
func TestCompatPassesThroughPerAgentFields(t *testing.T) {
	opencodeJSON := []byte(`{
		"agent": {
			"plan": {
				"model": "anthropic/claude-3-5-sonnet",
				"tools": {
					"write": false
				},
				"permission": {
					"edit": "deny"
				},
				"maxSteps": 12
			}
		}
	}`)

	res, err := NormalizeOpencode(opencodeJSON)
	if err != nil {
		t.Fatalf("NormalizeOpencode failed: %v", err)
	}

	var parsed struct {
		Agents map[string]AgentConfig `json:"agents"`
	}
	if err := json.Unmarshal(res.JSON, &parsed); err != nil {
		t.Fatalf("failed to unmarshal converted config: %v", err)
	}

	plan, ok := parsed.Agents["plan"]
	if !ok {
		t.Fatalf("converted config missing 'plan' agent: %s", string(res.JSON))
	}
	if plan.ToolSwitches == nil || plan.ToolSwitches["write"] != false {
		t.Errorf("plan agent missing ToolSwitches: %+v", plan.ToolSwitches)
	}
	if plan.Permission == nil || plan.Permission["edit"].Flat != DecisionDeny {
		t.Errorf("plan agent missing permission: %+v", plan.Permission)
	}
	if plan.Steps != 12 {
		t.Errorf("plan agent steps = %d, want 12", plan.Steps)
	}
}

// The two opencode vocabularies, side by side, because they were folded
// into one once and each folding is wrong in a different direction.
//
// In a permission block "edit" is the file-modification permission and
// there is no "write" key at all. In a tools block "write" and "edit" are
// separate tools, and opencode's own docs switch both off together to
// make a review-only agent. Folding tools into permission denies more
// than the file asked; folding permission into tools allows more.
func TestTheTwoOpencodeToolVocabulariesStayApart(t *testing.T) {
	// permission: edit covers both of localcode's modification tools.
	perm := ToolsCoveredBy("edit")
	if len(perm) != 2 || perm[0] != "edit" || perm[1] != "write_file" {
		t.Errorf(`ToolsCoveredBy("edit") = %v, want both modification tools`, perm)
	}
	if got := ToolsCoveredBy("write"); len(got) != 1 || got[0] != "write" {
		t.Errorf(`ToolsCoveredBy("write") = %v, and opencode has no "write" permission key`, got)
	}

	// tools: write is the write tool and edit is the edit tool.
	if got := ToolsSwitchedBy("write"); len(got) != 1 || got[0] != "write_file" {
		t.Errorf(`ToolsSwitchedBy("write") = %v, want just write_file`, got)
	}
	if got := ToolsSwitchedBy("edit"); len(got) != 1 || got[0] != "edit" {
		t.Errorf(`ToolsSwitchedBy("edit") = %v, want just edit`, got)
	}
	// And the patch tool, under either spelling, is an edit.
	for _, name := range []string{"patch", "apply_patch"} {
		if got := ToolsSwitchedBy(name); len(got) != 1 || got[0] != "edit" {
			t.Errorf("ToolsSwitchedBy(%q) = %v, want edit", name, got)
		}
	}

	// A name neither table knows is itself, so a localcode tool name
	// written into either block still means that tool.
	for _, name := range []string{"bash", "check", "Schedule"} {
		if got := ToolsSwitchedBy(name); len(got) != 1 || got[0] != name {
			t.Errorf("ToolsSwitchedBy(%q) = %v", name, got)
		}
	}
}
