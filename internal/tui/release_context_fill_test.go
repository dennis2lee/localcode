package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

// The context percentage is a reading of the history the daemon holds. An
// event that replaces that history has to take the reading with it, or
// the status line goes on saying 85% over a conversation that is nearly
// empty, until the next turn reports and nobody was looking any more.
func TestAReplacedHistoryBlanksTheContextGauge(t *testing.T) {
	for _, replacement := range []events.Type{events.TypeCleared, events.TypeCompacted, events.TypeRewound, events.TypeRedone} {
		t.Run(string(replacement), func(t *testing.T) {
			m := New(client.New("http://unused.invalid"), "s1", "general-purpose", make(chan events.Event), false)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
			m = updated.(Model)
			m.applyEvent(events.Event{Type: events.TypeUsage, Data: map[string]any{"percent": 85.0, "tps": 12.0, "show_tps": true}})
			if !strings.Contains(m.View().Content, "context: 85.0%") {
				t.Fatalf("precondition: the gauge is not showing 85%%:\n%s", m.View().Content)
			}

			m.applyEvent(events.Event{Type: replacement, Data: map[string]any{"summary_length": 12}})

			if got := m.View().Content; strings.Contains(got, "context:") {
				t.Errorf("after %s the status line still shows a fill for a history that is gone:\n%s", replacement, got)
			}
			if !strings.Contains(m.View().Content, "tok/s") {
				t.Errorf("after %s the rate went with the fill, and it is about the model, not the history", replacement)
			}
		})
	}
}
