package tui

import (
	"net/http"
	"testing"

	"localcode/internal/client"
)

// A send the daemon answers busy is queued, and the "[queued]" line
// stands for it from then on. Its own echo goes: a queued prompt is sent
// again with no echo of its own, so if that later send fails, nothing
// would take the first echo away, and it went on saying the prompt was
// sent (or, for a send into a running turn, that the model would pick it
// up) under the error that said it was not.
func TestAQueuedSendThatFailsLeavesNoEcho(t *testing.T) {
	busy := &client.StatusError{Status: http.StatusConflict, Message: "409: a turn is running"}
	failed := &client.StatusError{Status: http.StatusInternalServerError, Message: "500: the daemon fell over"}
	for _, tc := range []struct {
		name       string
		startsTurn bool
	}{
		{"new turn", true},
		{"into a running turn", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			var echo int
			if tc.startsTurn {
				echo = m.appendPendingUser("check the logs")
			} else {
				echo = m.appendSent("[sent — the model will pick this up at its next step] check the logs")
			}
			m.waiting = true

			updated, _ := m.Update(turnDoneMsg{text: "check the logs", err: busy, startsTurn: tc.startsTurn, echo: echo})
			m = updated.(Model)
			if len(m.queue) != 1 {
				t.Fatalf("queue = %q, want the busy send in it", m.queue)
			}

			// The running turn ends; the queued prompt goes out and fails.
			m.waiting = false
			if cmd := m.dequeue(); cmd == nil {
				t.Fatal("dequeue sent nothing")
			}
			updated, _ = m.Update(turnDoneMsg{text: "check the logs", err: failed, startsTurn: true})
			m = updated.(Model)

			for _, e := range m.transcript {
				if e.kind == entryPending || e.kind == entrySent {
					t.Errorf("an echo of the failed send is still on screen: %q (kind %v)", e.text, e.kind)
				}
			}
		})
	}
}
