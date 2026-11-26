package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

// A long permission question, written into the transcript before it can be
// answered, and the stale-question handling: a stale request writes
// nothing, and a stale resolution puts a held one away and brings up the
// next.

func zzLongDesc() string {
	return "schedule for 09:00: run the tests\n\nthen do the second thing"
}

func zzStalePermRes(id string) events.Event {
	return events.Event{Type: events.TypePermissionResolved, Data: map[string]any{
		"id": id, "allow": false, "unanswered": true, "stale": true,
	}}
}

// A stale long request is skipped whole: nothing in the transcript, no
// longShown entry, nothing on screen.
func TestAStaleLongRequestWritesNothing(t *testing.T) {
	m := newTestModel()
	ev := permissionEvent("d1-p1", zzLongDesc())
	ev.Data["stale"] = true
	before := len(m.transcript)
	m.applyEvent(ev)
	if len(m.transcript) != before {
		t.Errorf("a stale long request was written into the transcript: %q", m.transcriptText())
	}
	if _, ok := m.longShown["d1-p1"]; ok {
		t.Error("a stale long request is recorded as shown")
	}
	if m.pending != nil {
		t.Errorf("a stale long request is on screen: %+v", m.pending)
	}
}

// A held long question put away by a stale resolution: the footer goes,
// longShown forgets it, the transcript keeps the question once, and the
// layout gives the transcript its rows back.
func TestAStaleResolutionPutsAwayAHeldLongQuestion(t *testing.T) {
	m := newTestModel()
	upd, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = upd.(Model)
	free := m.viewport.Height()
	upd, _ = m.Update(eventMsg{gen: m.streamGen, ev: permissionEvent("d1-p1", zzLongDesc())})
	m = upd.(Model)
	if m.viewport.Height() >= free {
		t.Fatalf("precondition: the question took no rows from the transcript (%d before, %d with it)", free, m.viewport.Height())
	}
	if m.pending == nil || !m.pending.long {
		t.Fatalf("precondition: a long question on screen, got %+v", m.pending)
	}
	heldHeight := m.viewport.Height()
	upd, _ = m.Update(eventMsg{gen: m.streamGen, ev: events.Event{Type: client.TypeReconnected}})
	m = upd.(Model)
	upd, _ = m.Update(eventMsg{gen: m.streamGen, ev: zzStalePermRes("d1-p1")})
	m = upd.(Model)
	if m.pending != nil {
		t.Errorf("still on screen after the stale resolution: %+v", m.pending)
	}
	if _, ok := m.longShown["d1-p1"]; ok {
		t.Error("longShown still holds the question put away")
	}
	if n := strings.Count(m.transcriptText(), "then do the second thing"); n != 1 {
		t.Errorf("the question is in the transcript %d times, want once", n)
	}
	for _, line := range m.statusBand() {
		if strings.Contains(line, "transcript above") || strings.Contains(line, "y: allow") {
			t.Errorf("the band still draws the question: %q", line)
		}
	}
	if m.viewport.Height() <= heldHeight {
		t.Errorf("the transcript did not get its rows back: %d with the question, %d after", heldHeight, m.viewport.Height())
	}
	if m.bandRows != m.statusBandRows() {
		t.Errorf("layout computed for %d band rows, band takes %d", m.bandRows, m.statusBandRows())
	}
}

// A held long question queued behind another, the one on screen put away
// stale: the queued one comes up long, and the transcript holds it once.
func TestAStaleResolutionPromotesAQueuedLongQuestion(t *testing.T) {
	m := newTestModel()
	m.applyEvent(permissionEvent("d1-p1", "bash git status"))
	m.applyEvent(permissionEvent("d1-p2", zzLongDesc()))
	m.applyEvent(zzStalePermRes("d1-p1"))
	if m.pending == nil || m.pending.id != "d1-p2" || !m.pending.long {
		t.Fatalf("want d1-p2 long on screen, got %+v", m.pending)
	}
	if n := strings.Count(m.transcriptText(), "then do the second thing"); n != 1 {
		t.Errorf("the promoted question is in the transcript %d times, want once", n)
	}
	if !strings.Contains(m.pending.prompt(false), "transcript above") {
		t.Errorf("the promoted long question does not say where it is: %q", m.pending.prompt(false))
	}
}
