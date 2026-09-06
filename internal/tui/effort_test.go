package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

// The level in the footer, and the one way it can go stale.
//
// It is kept per model, and one conversation changes model whenever the
// agent changes — so a footer that only ever updated on a session switch
// went on naming the level of the model it had just left, which on a
// family with one switch is a number that means nothing. The daemon
// announces it from the agent-switch handler, and the whole answer is in
// the event, so this client needs no request of its own.
func TestTheFooterTakesTheLevelFromTheEvent(t *testing.T) {
	m := newTestModel()
	upd, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = upd.(Model)
	m.effort = client.EffortView{
		Model: "muse-glimmer-30b", Level: "xhigh", Source: "session",
		Levels: []string{"off", "low", "medium", "high", "xhigh"},
	}
	if !strings.Contains(footerOf(m), "effort: xhigh") {
		t.Fatalf("the level is not in the footer: %q", footerOf(m))
	}

	// The agent changed, so the model changed, so the daemon says what
	// the level is on the new one.
	m.applyEvent(events.Event{Type: events.TypeAgentSwitched, Data: map[string]any{"agent": "plan"}})
	m.applyEvent(events.Event{Type: events.TypeEffortChanged, Data: map[string]any{
		"model": "claude-sonnet-5", "agent": "plan", "level": "",
		"source": "unset", "levels": []any{"off", "high"}, "note": "one switch",
	}})

	if got := footerOf(m); strings.Contains(got, "xhigh") {
		t.Errorf("the footer still names the level of the model it left: %q", got)
	}
	if m.effort.Model != "claude-sonnet-5" {
		t.Errorf("effort.model = %q, want the model now in hand", m.effort.Model)
	}
	if len(m.effort.Levels) != 2 {
		t.Errorf("levels = %v, want the two the new model tells apart", m.effort.Levels)
	}

	// And a level on the new model shows.
	m.applyEvent(events.Event{Type: events.TypeEffortChanged, Data: map[string]any{
		"model": "claude-sonnet-5", "level": "high", "source": "session", "levels": []any{"off", "high"},
	}})
	if got := footerOf(m); !strings.Contains(got, "effort: high") {
		t.Errorf("footer = %q, want the new model's level", got)
	}
}

// A level nobody has chosen is not named. A footer that lists every
// setting nobody has touched is a footer nobody reads.
func TestAnUnsetLevelIsNotInTheFooter(t *testing.T) {
	m := newTestModel()
	upd, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = upd.(Model)
	m.effort = client.EffortView{Model: "claude-sonnet-5", Level: "", Source: "unset"}
	if strings.Contains(footerOf(m), "effort:") {
		t.Errorf("footer names an effort nobody set: %q", footerOf(m))
	}
}

// footerOf is the status line under the prompt: the one carrying "agent:".
func footerOf(m Model) string {
	for _, line := range strings.Split(m.View().Content, "\n") {
		if strings.Contains(line, "agent:") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
