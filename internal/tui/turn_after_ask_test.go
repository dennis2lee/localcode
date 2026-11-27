package tui

import (
	"testing"

	"localcode/internal/client"
	"localcode/internal/events"
)

// A model question held across a resume that brought no stale resolution
// is let go of when a turn ends in the session: a turn ends only after the
// question it asked is resolved, so one still held is not waited on. The
// next Enter is a prompt, not an answer the daemon would refuse.
func TestATurnEndedAfterAHeldQuestionPutsItAway(t *testing.T) {
	for _, end := range []string{"turn.done", "turn.cancelled"} {
		t.Run(end, func(t *testing.T) { turnEndedAfterAHeldQuestion(t, end) })
	}
}

func turnEndedAfterAHeldQuestion(t *testing.T, end string) {
	const (
		user    = `{"seq":400,"type":"message.user","data":{"text":"pick one"}}`
		liveAsk = `{"seq":401,"type":"input.request","data":{"id":"qaaaaaa-1","question":"which one?","options":["a","b"]}}`
		next    = `{"seq":500,"type":"message.user","data":{"text":"next"}}`
	)
	done := `{"seq":501,"type":"` + end + `","data":{}}`
	m := newTestModel()
	for _, f := range []string{user, liveAsk} {
		upd, _ := m.handleServerEvent(eventMsg{gen: m.streamGen, ev: frame(t, f)})
		m = upd.(Model)
	}
	if m.asking == nil {
		t.Fatal("precondition: the live question did not arm the box")
	}
	upd, _ := m.handleServerEvent(eventMsg{gen: m.streamGen, ev: events.Event{Type: client.TypeReconnected}})
	m = upd.(Model)
	for _, f := range []string{next, done} {
		upd, _ := m.handleServerEvent(eventMsg{gen: m.streamGen, ev: frame(t, f)})
		m = upd.(Model)
	}
	if m.asking != nil {
		t.Errorf("a turn ran and ended after %q was asked, and the box is still armed for it", m.asking.id)
	}
	m.input.SetValue("now run the tests")
	after, _ := handleEnter(m)
	if !after.(Model).waiting {
		t.Error("Enter did not send the message as a prompt: it went to the question nobody waits on")
	}
}
