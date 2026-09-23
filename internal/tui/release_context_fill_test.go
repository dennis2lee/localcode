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
	for _, replacement := range []events.Type{events.TypeCleared, events.TypeCompacted, events.TypeRewound, events.TypeRedone, events.TypeDebateEnded} {
		t.Run(string(replacement), func(t *testing.T) {
			m := New(client.New("http://unused.invalid"), "s1", "general-purpose", make(chan events.Event), false)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
			m = updated.(Model)
			m.applyEvent(events.Event{Type: events.TypeUsage, Data: map[string]any{"percent": 85.0, "tps": 12.0, "show_tps": true}})
			if !strings.Contains(m.View().Content, "context: 85.0%") {
				t.Fatalf("precondition: the gauge is not showing 85%%:\n%s", m.View().Content)
			}

			m.applyEvent(events.Event{Type: replacement, Data: map[string]any{"summary_length": 12, "collapsed": true}})

			if got := m.View().Content; strings.Contains(got, "context:") {
				t.Errorf("after %s the status line still shows a fill for a history that is gone:\n%s", replacement, got)
			}
			if !strings.Contains(m.View().Content, "tok/s") {
				t.Errorf("after %s the rate went with the fill, and it is about the model, not the history", replacement)
			}
		})
	}
}

// A debate that ended with nothing to collapse left the history as it was,
// and the daemon's count with it: the reading still describes what the
// model will be sent. One from a daemon too old to say is let go of.
func TestADebateThatCollapsedNothingKeepsTheContextGauge(t *testing.T) {
	for _, c := range []struct {
		name string
		data map[string]any
		kept bool
	}{
		{"nothing collapsed", map[string]any{"note": "debate ended after 1 round.", "collapsed": false}, true},
		{"an older daemon", map[string]any{"note": "debate ended after 1 round."}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := New(client.New("http://unused.invalid"), "s1", "general-purpose", make(chan events.Event), false)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
			m = updated.(Model)
			m.applyEvent(events.Event{Type: events.TypeUsage, Data: map[string]any{"percent": 85.0, "tps": 12.0, "show_tps": true}})
			m.applyEvent(events.Event{Type: events.TypeDebateEnded, Data: c.data})
			if kept := strings.Contains(m.View().Content, "context: 85.0%"); kept != c.kept {
				t.Errorf("gauge kept = %v, want %v:\n%s", kept, c.kept, m.View().Content)
			}
		})
	}
}

// A trim that dropped the oldest messages to fit the window replaced the
// history through the same path a compaction does, and the turn can still
// fail after it, so no usage event comes to replace the old fill. The
// trim's own notice says the history was replaced; any other recovered
// notice leaves the reading alone.
func TestATrimThatReplacedTheHistoryBlanksTheContextGauge(t *testing.T) {
	for _, c := range []struct {
		name string
		data map[string]any
		kept bool
	}{
		{"a trim", map[string]any{"error": "still too long", "recovered": true, "history_replaced": true}, false},
		{"another notice", map[string]any{"error": "retrying", "recovered": true}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := New(client.New("http://unused.invalid"), "s1", "general-purpose", make(chan events.Event), false)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
			m = updated.(Model)
			m.applyEvent(events.Event{Type: events.TypeUsage, Data: map[string]any{"percent": 85.0, "tps": 12.0, "show_tps": true}})
			m.applyEvent(events.Event{Type: events.TypeError, Data: c.data})
			if kept := strings.Contains(m.View().Content, "context: 85.0%"); kept != c.kept {
				t.Errorf("gauge kept = %v, want %v:\n%s", kept, c.kept, m.View().Content)
			}
		})
	}
}
