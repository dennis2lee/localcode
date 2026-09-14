package agent

import (
	"context"
	"testing"

	"localcode/internal/config"
	"localcode/internal/session"
)

// Requirement under test: switching a session's agent (PATCH
// /sessions/{id} -> Store.SetAgent, see handleSwitchAgent) must change
// what the NEXT turn runs as. The tool allowlist and the model profile
// are per-agent facts, so they must follow the new agent; no cached
// answer from the previous agent may survive. The four permission
// switches and remembered session approvals are per-session facts by
// design (they describe the work, not who does it), so they must
// survive the switch unchanged.
//
// A bug of the reported shape — the session keeping the previous
// agent's state — would show up here as the post-switch turn resolving
// the old agent's tools or profile.
func TestAgentSwitchMovesPerAgentStateKeepsSessionState(t *testing.T) {
	ctx := context.Background()

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"},
		},
		Profiles: map[string]config.Profile{
			"p-read":  {Provider: "local", Model: "model-read"},
			"p-write": {Provider: "local", Model: "model-write"},
		},
		DefaultProfile: "p-read",
		Agents: map[string]config.AgentConfig{
			"reader": {Profile: "p-read", Tools: []string{"read_file"}},
			"writer": {Profile: "p-write", Tools: []string{"write_file"}},
		},
	}
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	const sessID = "s-switch"
	if _, err := store.CreateSessionIn(sessID, "", "reader", "", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	loop := &Loop{Store: store, Config: cfg}

	resolve := func() (agent string, allowed []string, model string) {
		t.Helper()
		agent = loop.sessionAgent(sessID)
		agentCfg := loop.agentConfig(ctx, agent)
		allowed = loop.toolsForTurn(ctx, agentCfg)
		_, profile, err := loop.profileFor(ctx, sessID, agent)
		if err != nil {
			t.Fatalf("profileFor(%q): %v", agent, err)
		}
		return agent, allowed, profile.Model
	}

	// Baseline: talking to the reader resolves the reader's facts.
	agent, allowed, model := resolve()
	if agent != "reader" || len(allowed) != 1 || allowed[0] != "read_file" || model != "model-read" {
		t.Fatalf("before switch: got agent=%q tools=%v model=%q, want reader [read_file] model-read",
			agent, allowed, model)
	}

	// Session-scoped answers given while talking to the reader.
	policy := NewPermissionPolicy(store, cfg)
	yes := true
	if err := policy.Set(sessID, session.SwitchSkipTools, &yes); err != nil {
		t.Fatalf("set switch: %v", err)
	}
	broker := NewPermissionBroker(store)
	broker.grant(sessID, "bash", "npm *")

	// The switch itself: exactly what handleSwitchAgent persists.
	if _, err := store.SetAgent(sessID, "writer"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}

	// Per-agent facts must now be the writer's. If the session held the
	// previous agent's state, this still reports read_file/model-read.
	agent, allowed, model = resolve()
	if agent != "writer" {
		t.Fatalf("after switch: session agent = %q, want writer", agent)
	}
	if len(allowed) != 1 || allowed[0] != "write_file" {
		t.Fatalf("after switch: tools = %v, want [write_file] (stale reader state)", allowed)
	}
	if model != "model-write" {
		t.Fatalf("after switch: model = %q, want model-write (stale reader state)", model)
	}

	// Session-scoped facts must survive: the switch answers the same
	// project, only with a different agent.
	if on, src := policy.Effective(sessID, session.SwitchSkipTools); !on || src != SourceSession {
		t.Fatalf("after switch: skip_tools effective=%v src=%v, want true session", on, src)
	}
	if !broker.isGranted(sessID, "bash", "npm *") {
		t.Fatalf("after switch: session approval for %q lost", "bash npm *")
	}
}
