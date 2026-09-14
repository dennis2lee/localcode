package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

// After an agent switch the footer must name the model the next turn will
// actually run on: the new agent's own choice if this conversation made one
// for that agent, otherwise what the new agent's profile resolves to. Never
// the previous agent's. The server keeps one choice per agent; the footer
// used to trust the cached choice without comparing which agent it belongs
// to, so Tab kept showing the agent just left next to the new agent's name.

func switchTestModel() Model {
	m := newTestModel()
	upd, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = upd.(Model)
	m.agents = []client.AgentInfo{
		{Name: "general-purpose", Model: "gp-model"},
		{Name: "plan", Model: "plan-model"},
	}
	return m
}

func TestSwitchingAgentsDropsThePreviousAgentsChosenModel(t *testing.T) {
	m := switchTestModel()
	m.model = client.ModelView{Agent: "general-purpose", Source: "conversation", Model: "old-choice"}
	if got := footerOf(m); !strings.Contains(got, "old-choice") {
		t.Fatalf("setup: footer = %q, want the chosen model", got)
	}

	m.applyEvent(events.Event{Type: events.TypeAgentSwitched, Data: map[string]any{"agent": "plan"}})

	got := footerOf(m)
	if !strings.Contains(got, "agent: plan") {
		t.Errorf("footer = %q, want the new agent", got)
	}
	if !strings.Contains(got, "plan-model") {
		t.Errorf("footer = %q, want the new agent's profile model", got)
	}
	if strings.Contains(got, "old-choice") {
		t.Errorf("footer = %q, still names the agent just left", got)
	}
}

func TestSwitchingAgentsNamesTheNewAgentsOwnChoice(t *testing.T) {
	m := switchTestModel()
	m.model = client.ModelView{Agent: "general-purpose", Source: "conversation", Model: "old-choice"}

	m.applyEvent(events.Event{Type: events.TypeAgentSwitched, Data: map[string]any{"agent": "plan"}})
	// The daemon announces the new agent's view right after the switch.
	m.applyEvent(events.Event{Type: events.TypeModelChanged, Data: map[string]any{
		"agent": "plan", "model": "new-choice", "source": "conversation",
	}})

	got := footerOf(m)
	if !strings.Contains(got, "model: new-choice") {
		t.Errorf("footer = %q, want the new agent's own choice", got)
	}
	if strings.Contains(got, "old-choice") {
		t.Errorf("footer = %q, still names the agent just left", got)
	}
}

func TestSwitchingAgentsWithNoChoicesNamesTheNewAgentsProfile(t *testing.T) {
	m := switchTestModel()

	m.applyEvent(events.Event{Type: events.TypeAgentSwitched, Data: map[string]any{"agent": "plan"}})
	m.applyEvent(events.Event{Type: events.TypeModelChanged, Data: map[string]any{
		"agent": "plan", "model": "plan-model", "source": "agent",
	}})

	got := footerOf(m)
	if !strings.Contains(got, "agent: plan") {
		t.Errorf("footer = %q, want the new agent", got)
	}
	if !strings.Contains(got, "plan-model") {
		t.Errorf("footer = %q, want the new agent's profile model", got)
	}
}

// The consumer guard itself: a cached choice that names another agent is
// stale no matter how it got there, so the footer must not trust it even
// if the switch handler ever forgets to drop it.
func TestAChoiceForAnotherAgentIsNotNamed(t *testing.T) {
	m := switchTestModel()
	m.currentAgent = "plan"
	m.model = client.ModelView{Agent: "general-purpose", Source: "conversation", Model: "old-choice"}

	got, ok := m.currentModel()
	if !ok {
		t.Fatalf("currentModel = %q, false; want the new agent's profile model", got)
	}
	if got != "plan-model" {
		t.Errorf("currentModel = %q, want %q", got, "plan-model")
	}
	if got := footerOf(m); strings.Contains(got, "old-choice") {
		t.Errorf("footer = %q, names a choice made for another agent", got)
	}
}

// Opening another conversation must not inherit the previous one's model:
// a conversation that never chose names its own agent's profile.
func TestSwitchingSessionsResetsTheModelView(t *testing.T) {
	m := switchTestModel()
	m.model = client.ModelView{Agent: "general-purpose", Source: "conversation", Model: "old-choice"}

	ch := make(chan events.Event)
	upd, _ := m.Update(sessionSwitchedMsg{sessionID: "s2", agent: "plan", events: ch})
	m = upd.(Model)

	if m.model.Model != "" || m.model.Source != "" || m.model.Agent != "" {
		t.Errorf("m.model = %+v after a session switch, want it reset", m.model)
	}
	m.agents = []client.AgentInfo{
		{Name: "general-purpose", Model: "gp-model"},
		{Name: "plan", Model: "plan-model"},
	}
	got := footerOf(m)
	if strings.Contains(got, "old-choice") {
		t.Errorf("footer = %q, inherits the previous conversation's model", got)
	}
	if !strings.Contains(got, "plan-model") {
		t.Errorf("footer = %q, want the opened conversation's agent profile model", got)
	}
}
