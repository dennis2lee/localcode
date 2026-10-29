package tui

import (
	"fmt"
	"testing"

	"localcode/internal/events"
)

// A send whose message.user has already replaced its echo, and whose
// result then reports a failure (a connection reset after the daemon took
// it), removes nothing: its echo is gone, and the number is no other
// entry's.
func TestSendFailureAfterMessageUserDoesNotRemoveWrongEntry(t *testing.T) {
	m := newTestModel()
	echo1 := m.appendPendingUser("same prompt")

	// 1. Simulate daemon message arriving before turnDoneMsg error
	updated, _ := m.Update(eventMsg{
		ev: events.Event{
			Type: events.TypeUserMessage,
			Data: map[string]any{"text": "same prompt"},
		},
	})
	m = updated.(Model)

	// Ensure the user entry is present
	foundUser := false
	for _, e := range m.transcript {
		if e.kind == entryUser && e.text == "same prompt" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Fatal("precondition failed: entryUser not found after user.message")
	}

	// 2. Add another pending echo of the same prompt (e.g. another send)
	echo2 := m.appendPendingUser("same prompt")

	// 3. Now the first send fails with an error
	updated, _ = m.Update(turnDoneMsg{
		text:       "same prompt",
		err:        fmt.Errorf("connection reset by peer"),
		startsTurn: true,
		echo:       echo1,
	})
	m = updated.(Model)

	// 4. Verify transcript state:
	// - entryUser for send 1 should STILL BE PRESENT (send=0, kind=entryUser)
	// - echo 2 should STILL BE PRESENT (send=echo2, kind=entryPending)
	hasUser := false
	hasEcho2 := false
	for _, e := range m.transcript {
		if e.kind == entryUser && e.text == "same prompt" {
			hasUser = true
		}
		if e.send == echo2 && e.kind == entryPending && e.text == "same prompt" {
			hasEcho2 = true
		}
	}
	if !hasUser {
		t.Errorf("the resolved entryUser was incorrectly removed when send 1 failed! transcript = %v", m.transcript)
	}
	if !hasEcho2 {
		t.Errorf("echo 2 was incorrectly removed when send 1 failed! transcript = %v", m.transcript)
	}
}

// The same for a send into a running turn.
func TestMidTurnSendFailureAfterMessageUserDoesNotRemoveWrongEntry(t *testing.T) {
	m := newTestModel()
	echo1 := m.appendSent("[sent — the model will pick this up at its next step] mid-turn line")

	// 1. Simulate daemon message arriving before turnDoneMsg error
	updated, _ := m.Update(eventMsg{
		ev: events.Event{
			Type: events.TypeUserMessage,
			Data: map[string]any{"text": "mid-turn line"},
		},
	})
	m = updated.(Model)

	// Ensure the user entry is present
	foundUser := false
	for _, e := range m.transcript {
		if e.kind == entryUser && e.text == "mid-turn line" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Fatal("precondition failed: entryUser not found after user.message")
	}

	// 2. Add another sent echo of the same prompt
	echo2 := m.appendSent("[sent — the model will pick this up at its next step] mid-turn line")

	// 3. Send 1 fails
	updated, _ = m.Update(turnDoneMsg{
		text:       "mid-turn line",
		err:        fmt.Errorf("server error"),
		startsTurn: false,
		echo:       echo1,
	})
	m = updated.(Model)

	// 4. Verify:
	// - entryUser is still there
	// - echo 2 is still there
	hasUser := false
	hasEcho2 := false
	for _, e := range m.transcript {
		if e.kind == entryUser && e.text == "mid-turn line" {
			hasUser = true
		}
		if e.send == echo2 && e.kind == entrySent {
			hasEcho2 = true
		}
	}
	if !hasUser {
		t.Errorf("entryUser was removed when mid-turn send 1 failed! transcript = %v", m.transcript)
	}
	if !hasEcho2 {
		t.Errorf("echo 2 was removed when mid-turn send 1 failed! transcript = %v", m.transcript)
	}
}
