package tui

import (
	"strings"
	"testing"

	"localcode/internal/events"
)

// The daemon's copy of a reply is authoritative, and message.part.end
// carries it.
//
// Closing the open entry without redrawing was the defect. Deltas can be
// missed: an SSE reconnect resumes from the last id this client saw, so
// the fragments sent while the connection was down are never replayed,
// and what was drawn from the ones that did arrive is the reply with a
// hole in it. The entry was already open, so this closed it and kept the
// hole for the life of the process.
func TestAReplyIsRepairedFromTheDaemonsCopy(t *testing.T) {
	m := newTestModel()

	// The first fragment arrives, then the connection drops and the rest
	// of them are never replayed.
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "the beginning "}})
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "the beginning and the end"}})

	if got := m.transcriptText(); !strings.Contains(got, "the beginning and the end") {
		t.Errorf("transcript = %q, want the whole reply as the daemon recorded it", got)
	}
	if strings.Count(m.transcriptText(), "the beginning") != 1 {
		t.Errorf("transcript = %q, want the reply once rather than the fragment and the whole thing", m.transcriptText())
	}
}

// The ordinary case has to stay ordinary: every fragment arrived, and
// part.end writes back the same characters rather than a second copy.
func TestAReplyThatMissedNothingIsNotDrawnTwice(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "all "}})
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "of it"}})
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "all of it"}})

	if got := m.transcriptText(); got != "all of it" {
		t.Errorf("transcript = %q, want %q exactly once", got, "all of it")
	}
}

// Replay is the other direction: the daemon drops the fragments of
// finished replies, so part.end is the only place the text arrives and it
// has to start an entry of its own.
func TestOnReplayThePartEndDrawsTheReply(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "a finished reply"}})

	if got := m.transcriptText(); !strings.Contains(got, "a finished reply") {
		t.Errorf("transcript = %q, want the replayed reply", got)
	}
}

// A re-attach has to name a real position in the log.
//
// It used to ask from ^uint64(0), meaning "replay nothing", and that
// number does not survive the trip: the daemon seeds its live filter with
// it and then drops every event whose sequence fails to exceed it. No
// sequence can exceed the maximum, so the stream came up healthy and
// delivered nothing at all, for as long as the client stayed on that
// session.
func TestAReattachResumesFromWhereThisClientGotTo(t *testing.T) {
	m := newTestModel()
	for _, seq := range []uint64{1, 2, 7} {
		m.applyEventAt(t, seq)
	}
	if m.lastSeq != 7 {
		t.Fatalf("lastSeq = %d after events up to 7", m.lastSeq)
	}

	// A transient event carries no sequence and must not move the resume
	// point: coming back with it would re-deliver everything after 7.
	m.applyEventAt(t, 0)
	if m.lastSeq != 7 {
		t.Errorf("lastSeq = %d after a transient event, want it left at 7", m.lastSeq)
	}
}

// A sequence numbers one session's log and means nothing in another, so
// opening a different conversation starts the resume point over.
func TestSwitchingSessionsForgetsTheResumePoint(t *testing.T) {
	m := newTestModel()
	m.applyEventAt(t, 9)

	updated, _ := m.handleSessionSwitched(sessionSwitchedMsg{
		sessionID: "s2",
		agent:     "general-purpose",
		gen:       m.streamGen,
		events:    make(chan events.Event),
	})
	if got := updated.(Model).lastSeq; got != 0 {
		t.Errorf("lastSeq = %d after switching sessions, want 0", got)
	}
}

// applyEventAt drives one event through the same path Update takes, which
// is where the resume point is recorded.
func (m *Model) applyEventAt(t *testing.T, seq uint64) {
	t.Helper()
	updated, _ := m.handleServerEvent(eventMsg{
		gen: m.streamGen,
		ev: events.Event{
			Seq:  seq,
			Type: events.TypeUserMessage,
			Data: map[string]any{"text": "x"},
		},
	})
	*m = updated.(Model)
}
