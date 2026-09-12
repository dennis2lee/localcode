package agent

import (
	"context"
	"testing"

	"localcode/internal/config"
	"localcode/internal/tools"
)

// Offering a tool whose permission can only be refused.
//
// Orchestrate asks on every call, and a run is up to 32 agent turns and
// half an hour. Unattended there is nobody to answer, so the model built
// a whole plan and discovered at the last step that it could not be
// started. Debate was kept out of those turns by hiding it; this one
// could not be, because whether the call is authorized depends on the
// rules rather than on the situation — skip_all, skip_tools or an allow
// rule all say yes, and a turn with one of those can orchestrate
// unattended perfectly well.

// orchestrateLoop is a loop with the Orchestrate switch on and two
// agents to delegate to, so the only thing left that can hide the tool is
// the answer under test.
func orchestrateHiddenLoop(t *testing.T, d tools.Decision) *Loop {
	t.Helper()
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"only": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"one": {Profile: "only"}, "two": {Profile: "only"}},
		DefaultProfile: "only",
	}
	loop := &Loop{Tools: decideAs(t, d), Config: cfg}
	loop.SetOrchestrateEnabled(true)
	return loop
}

// decideAs builds a registry whose resolver always answers d, which is
// the thing hiddenTools now asks.
func decideAs(t *testing.T, d tools.Decision) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry(func(context.Context, tools.Ask) (bool, error) { return true, nil })
	reg.Register(NewOrchestrateTool(nil))
	reg.Resolver = func(context.Context, tools.Query) tools.Outcome {
		return tools.Outcome{Decision: d}
	}
	return reg
}

func TestOrchestrateIsWithheldFromATurnThatCannotBeAsked(t *testing.T) {
	loop := orchestrateHiddenLoop(t, tools.DecisionAsk)

	// Somebody is there: the prompt can be answered, so the tool stands.
	if loop.hiddenTools(context.Background())[orchestrateToolName] {
		t.Error("Orchestrate was withheld from a conversation somebody is having")
	}

	// Nobody is: the prompt cannot be answered, and offering the tool
	// only buys a plan that cannot be run.
	if !loop.hiddenTools(WithUnattended(context.Background()))[orchestrateToolName] {
		t.Error("Orchestrate was offered to an unattended turn that would have to ask")
	}
}

// A rule or a switch that authorizes the call puts it back, which is the
// half a blanket "hide it when unattended" would have got wrong.
func TestAnAuthorizedUnattendedTurnKeepsOrchestrate(t *testing.T) {
	loop := orchestrateHiddenLoop(t, tools.DecisionAllow)

	if loop.hiddenTools(WithUnattended(context.Background()))[orchestrateToolName] {
		t.Error("Orchestrate was withheld from an unattended turn that is already allowed to run it")
	}
}

// And a rule that denies it keeps it hidden for the same reason: an
// offered tool that can only refuse is a turn spent finding out.
func TestADeniedToolIsNotOffered(t *testing.T) {
	loop := orchestrateHiddenLoop(t, tools.DecisionDeny)

	if !loop.hiddenTools(WithUnattended(context.Background()))[orchestrateToolName] {
		t.Error("a denied Orchestrate was still offered")
	}
}

// Registry.Decide is the question, asked before the call rather than
// during it.
func TestDecideAnswersBeforeTheCall(t *testing.T) {
	reg := decideAs(t, tools.DecisionAllow)
	if got := reg.Decide(context.Background(), orchestrateToolName); got != tools.DecisionAllow {
		t.Errorf("Decide = %q, want the resolver's answer", got)
	}
	// A tool that is not registered cannot be run, and saying "allow"
	// about one would be the wrong direction to be wrong in.
	if got := reg.Decide(context.Background(), "NotRegistered"); got != tools.DecisionDeny {
		t.Errorf("Decide on an unregistered tool = %q, want deny", got)
	}
	// With no resolver at all it is the tool's own static default, which
	// is what a Registry without one has always done.
	bare := tools.NewRegistry(nil)
	bare.Register(NewOrchestrateTool(nil))
	if got := bare.Decide(context.Background(), orchestrateToolName); got != tools.DecisionAsk {
		t.Errorf("Decide with no resolver = %q, want the tool's own default", got)
	}
}
