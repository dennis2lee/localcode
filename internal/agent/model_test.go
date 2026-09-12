package agent

import (
	"context"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/session"
)

// Choosing a model without choosing an agent.
//
// The two were one choice: an agent carries a prompt, a tool allowlist
// and a model, so answering on a different model meant writing a second
// agent that was the first one with another profile.

// twoProfileLoop is a loop whose config can reach two models through one
// agent, which is the shape this feature is about.
func twoProfileLoop(t *testing.T) (*Loop, string) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local":  {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"},
			"hosted": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:2"},
		},
		Profiles: map[string]config.Profile{
			"small": {Provider: "local", Model: "muse-glimmer-30b"},
			"big":   {Provider: "hosted", Model: "a-much-larger-model"},
		},
		Agents:         map[string]config.AgentConfig{"boy": {Profile: "small", Prompt: "you are the boy"}},
		DefaultProfile: "small",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	loop := &Loop{Store: store, Config: cfg}
	if _, err := store.CreateSession("s1", "", "boy", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return loop, "s1"
}

// Before anything is chosen, the agent's own profile answers.
func TestTheAgentsModelAnswersUntilOneIsChosen(t *testing.T) {
	loop, sid := twoProfileLoop(t)

	v := loop.ModelView(sid)
	if v.Model != "muse-glimmer-30b" {
		t.Errorf("model = %q, want the agent's own", v.Model)
	}
	if v.Source != "agent" {
		t.Errorf("source = %q, want agent", v.Source)
	}
	// And every profile is offered, so a client does not have to know
	// what this config declares.
	if len(v.Choices) != 2 {
		t.Fatalf("choices = %d, want both profiles", len(v.Choices))
	}
	if v.Choices[0].Profile != "big" || v.Choices[1].Profile != "small" {
		t.Errorf("choices are not in a stable order: %+v", v.Choices)
	}
}

// Choosing one changes what a turn resolves, and nothing else about the
// agent.
func TestChoosingAModelKeepsTheAgent(t *testing.T) {
	loop, sid := twoProfileLoop(t)

	v, err := loop.SetSessionModel(sid, "big")
	if err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	if v.Model != "a-much-larger-model" || v.Source != "conversation" {
		t.Errorf("view = %+v, want the chosen model sourced from the conversation", v)
	}
	// The resolution a turn actually performs, which is the half that
	// matters: the provider travels with the model, and the agent is
	// still the agent.
	name, profile, err := loop.profileFor(context.Background(), sid, "boy")
	if err != nil {
		t.Fatalf("profileFor: %v", err)
	}
	if name != "big" || profile.Model != "a-much-larger-model" {
		t.Errorf("a turn resolved %q/%q, want the chosen profile", name, profile.Model)
	}
	if profile.Provider != "hosted" {
		t.Errorf("provider = %q, want the one that serves the chosen model", profile.Provider)
	}
	if got := loop.agentConfig(context.Background(), "boy").Prompt; got != "you are the boy" {
		t.Errorf("the agent's prompt changed with the model: %q", got)
	}
}

// Clearing is a real third state: a conversation that never chose and one
// that chose the agent's own profile only look alike until the agent's
// config changes.
func TestClearingGoesBackToTheAgents(t *testing.T) {
	loop, sid := twoProfileLoop(t)

	if _, err := loop.SetSessionModel(sid, "big"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	if _, err := loop.SetSessionModel(sid, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	v := loop.ModelView(sid)
	if v.Source != "agent" || v.Model != "muse-glimmer-30b" {
		t.Errorf("view = %+v, want the agent's own back", v)
	}
	if loop.Store.ProfileFor(sid, "boy") != "" {
		t.Error("the store still holds a choice after it was cleared")
	}
}

// A name this config does not have is refused, and the refusal names the
// choices — the whole difficulty being that nobody memorises profile
// names.
func TestAnUnknownProfileIsRefusedByName(t *testing.T) {
	loop, sid := twoProfileLoop(t)

	_, err := loop.SetSessionModel(sid, "enormous")
	if err == nil {
		t.Fatal("an unknown profile was accepted")
	}
	for _, want := range []string{"enormous", "big", "small"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// The choice is per agent, because agents are pointed at models that suit
// them and a choice made while talking to one should not follow you to
// another.
func TestTheChoiceIsPerAgent(t *testing.T) {
	loop, sid := twoProfileLoop(t)
	loop.Config.Agents["girl"] = config.AgentConfig{Profile: "small"}

	if _, err := loop.SetSessionModel(sid, "big"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	// Still the agent's own for a different agent in the same
	// conversation.
	name, _, err := loop.profileFor(context.Background(), sid, "girl")
	if err != nil {
		t.Fatalf("profileFor: %v", err)
	}
	if name != "small" {
		t.Errorf("the other agent resolved %q, want its own profile", name)
	}
}

// A config edited under a running conversation is ordinary, and a choice
// that no longer names anything falls back rather than failing the turn.
func TestAChoiceThatNoLongerExistsFallsBack(t *testing.T) {
	loop, sid := twoProfileLoop(t)

	if _, err := loop.SetSessionModel(sid, "big"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	delete(loop.Config.Profiles, "big")

	name, profile, err := loop.profileFor(context.Background(), sid, "boy")
	if err != nil {
		t.Fatalf("profileFor: %v", err)
	}
	if name != "small" || profile.Model != "muse-glimmer-30b" {
		t.Errorf("resolved %q/%q, want the agent's own after the choice vanished", name, profile.Model)
	}
}

// "/model" answers in the Web UI, where there is no picker, and never
// reaches the model.
func TestTheModelCommandAnswersLocally(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "")

	if err := loop.SendMessage(context.Background(), sid, "boy", "/model"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if n := len(bodies()); n != 0 {
		t.Fatalf("/model reached the model (%d requests)", n)
	}
	reply := lastReply(t, loop, sid)
	for _, want := range []string{"model:", "This config can reach", "usage: /model"} {
		if !strings.Contains(reply, want) {
			t.Errorf("/model does not say %q: %s", want, reply)
		}
	}
}
