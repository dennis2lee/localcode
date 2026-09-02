package tui

import (
	"testing"

	"localcode/internal/events"
)

// switchTo drives the two halves of a session switch the way Update does:
// openSession runs locally and everything that belonged to the old
// conversation is let go of there, and the message that comes back from
// the daemon adopts the new one.
func (m *Model) switchTo(t *testing.T, id string) {
	t.Helper()
	m.openSession(id)
	updated, _ := m.handleSessionSwitched(sessionSwitchedMsg{
		sessionID: id,
		agent:     "general-purpose",
		gen:       m.streamGen,
		events:    make(chan events.Event),
	})
	*m = updated.(Model)
}

// A prompt half typed belongs to the conversation it was typed in.
//
// One prompt box serves every session, and a switch replaced everything
// around it — transcript, tasks, resume point, recall — while leaving
// whatever was in the box sitting there. A sentence composed in one
// project followed you into another, where the next Enter would have sent
// it to a different model in a different directory.
func TestAnUnsentPromptStaysWithItsSession(t *testing.T) {
	m := newTestModel()
	m.setInputTo("delete the build directory")

	m.switchTo(t, "s2")
	if got := m.input.Value(); got != "" {
		t.Errorf("the draft followed the switch: prompt box holds %q", got)
	}

	m.switchTo(t, "s1")
	if got := m.input.Value(); got != "delete the build directory" {
		t.Errorf("coming back lost what was being composed: prompt box holds %q", got)
	}
}

// Two conversations, two drafts, neither of them the other's.
func TestEachSessionKeepsItsOwnUnsentPrompt(t *testing.T) {
	m := newTestModel()
	m.setInputTo("in the first session")

	m.switchTo(t, "s2")
	m.setInputTo("in the second session")

	m.switchTo(t, "s1")
	if got := m.input.Value(); got != "in the first session" {
		t.Errorf("s1 came back holding %q", got)
	}
	m.switchTo(t, "s2")
	if got := m.input.Value(); got != "in the second session" {
		t.Errorf("s2 came back holding %q", got)
	}
}

// A switch that never happened must not take the box with it: the client
// is still in the conversation the text was typed in.
func TestAFailedSwitchLeavesTheDraftAlone(t *testing.T) {
	m := newTestModel()
	m.setInputTo("half a sentence")

	m.openSession("s2")
	updated, _ := m.handleSessionSwitched(sessionSwitchedMsg{
		gen: m.streamGen,
		err: errSwitchFailed,
	})
	m = updated.(Model)

	if got := m.input.Value(); got != "half a sentence" {
		t.Errorf("a failed switch emptied the prompt box: %q", got)
	}
}

var errSwitchFailed = errTest("session s2 is gone")

type errTest string

func (e errTest) Error() string { return string(e) }
