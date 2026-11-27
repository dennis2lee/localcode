package tui

import (
	"net/http"
	"testing"

	"localcode/internal/client"
	"localcode/internal/events"
)

// A send refused as busy goes into the queue, to be sent again. The queue
// is the shown conversation's, so a refusal that comes after a switch
// would send the prompt there. It goes back into the box of the
// conversation it was typed into instead. The Web UI does the same
// (test/webui/switch_refusal.test.js).

var errBusy = &client.StatusError{Status: http.StatusConflict, Message: "409: session s1 is already processing a message"}

// switchTo opens id as the picker does: openSession parks the box and
// starts the switch, and its answer, which needs a daemon, is given here.
func switchTo(t *testing.T, m Model, id string) Model {
	t.Helper()
	_ = m.openSession(id)
	updated, _ := m.Update(sessionSwitchedMsg{sessionID: id, agent: "general-purpose", events: make(chan events.Event), cancel: func() {}, gen: m.streamGen})
	m = updated.(Model)
	if m.sessionID != id {
		t.Fatalf("precondition: switched to %q, on %q", id, m.sessionID)
	}
	return m
}

func TestAPromptRefusedAfterASwitchGoesBackToItsOwnBox(t *testing.T) {
	for _, tc := range []struct {
		name       string
		startsTurn bool
	}{
		{"sent to an idle session", true},
		{"sent mid-turn", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.waiting = !tc.startsTurn
			m, _ = pressEnterWith(t, m, "for the first conversation")
			echo := m.sends
			m = switchTo(t, m, "s2")

			updated, _ := m.Update(turnDoneMsg{text: "for the first conversation", err: errBusy, startsTurn: tc.startsTurn, echo: echo, session: "s1"})
			m = updated.(Model)
			if len(m.queue) != 0 {
				t.Errorf("a prompt typed into s1 is in s2's queue: %q", m.queue)
			}
			if got := m.drafts["s1"]; got != "for the first conversation" {
				t.Errorf("s1's box holds %q, want the refused prompt", got)
			}
			m = switchTo(t, m, "s1")
			if got := m.input.Value(); got != "for the first conversation" {
				t.Errorf("back in s1 the box holds %q", got)
			}
		})
	}
}

func TestARefusedPromptGoesAheadOfWhatItsBoxAlreadyHeld(t *testing.T) {
	m := newTestModel()
	m, _ = pressEnterWith(t, m, "first thought")
	echo := m.sends
	m.input.SetValue("second thought")
	m = switchTo(t, m, "s2")

	updated, _ := m.Update(turnDoneMsg{text: "first thought", err: errBusy, startsTurn: true, echo: echo, session: "s1"})
	m = updated.(Model)
	if got, want := m.drafts["s1"], "first thought\nsecond thought"; got != want {
		t.Errorf("s1's box holds %q, want %q", got, want)
	}
}

// A refusal for the conversation shown is queued, as it always was.
func TestARefusalForTheShownConversationIsQueued(t *testing.T) {
	m := newTestModel()
	m, _ = pressEnterWith(t, m, "later")
	updated, _ := m.Update(turnDoneMsg{text: "later", err: errBusy, startsTurn: true, echo: m.sends, session: "s1"})
	m = updated.(Model)
	if len(m.queue) != 1 || m.queue[0] != "later" {
		t.Errorf("want the prompt queued for s1, got %q", m.queue)
	}
	if got := m.drafts["s1"]; got != "" {
		t.Errorf("a queued prompt was also put back in the box: %q", got)
	}
}
