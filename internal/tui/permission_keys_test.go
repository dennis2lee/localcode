package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/events"
)

func armedPending(m Model, canAlways bool) Model {
	m.pending = &pendingPermission{
		id: "p1", tool: "bash", description: "rm -rf build/",
		rule: "rm *", canAlways: canAlways,
	}
	m.pendingHintShown = false
	// Long enough ago that the arming delay has passed.
	m.pendingSince = time.Now().Add(-time.Hour)
	return m
}

func press(m Model, key string) (Model, tea.Cmd, bool) {
	next, cmd, handled := m.handleKey(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
	return next.(Model), cmd, handled
}

// The reported bug: typing prose while a turn runs, a permission request
// arrives, and the next ordinary letter is taken as the answer.
//
// "yes, use the second approach" starts with y. Under the old rule that
// y approved `rm -rf build/` — a command the user had not read and had
// not even seen appear. "s" granted it for the whole session and "a"
// wrote a permanent rule into config.json, with no confirmation and
// nothing to undo it with.
func TestTypingLettersDoNotAnswerAPermission(t *testing.T) {
	for _, key := range []string{"y", "n", "s", "a"} {
		t.Run(key, func(t *testing.T) {
			m := armedPending(newTestModel(), true)
			m.input.SetValue("yes, use the second approac") // mid-sentence

			next, cmd, handled := press(m, key)
			if handled {
				t.Errorf("%q was consumed while the user was typing", key)
			}
			if cmd != nil {
				t.Errorf("%q sent a command while the user was typing", key)
			}
			if next.pending == nil {
				t.Errorf("%q cleared the pending request while the user was typing", key)
			}
		})
	}
}

// A keypress already travelling when the modal appears must not land on
// it. Nothing interrupts the user — the textarea keeps focus and the
// request simply appears below the prompt box — so without a pause the
// very next character answers a question that was not on screen when the
// finger started moving.
func TestAFreshRequestIsNotAnsweredImmediately(t *testing.T) {
	m := armedPending(newTestModel(), true)
	m.pendingSince = time.Now() // just arrived

	next, cmd, handled := press(m, "y")
	if handled || cmd != nil {
		t.Error("y answered a request that had only just appeared")
	}
	if next.pending == nil {
		t.Error("the request was cleared by a keypress that arrived with it")
	}
}

// The ordinary case still works: waiting on the model, empty prompt box,
// the request has been on screen. All four answers must go through.
func TestAnEmptyPromptBoxStillAnswersNormally(t *testing.T) {
	for _, key := range []string{"y", "n", "s", "a"} {
		t.Run(key, func(t *testing.T) {
			m := armedPending(newTestModel(), true)

			next, cmd, handled := press(m, key)
			if !handled {
				t.Fatalf("%q was not treated as an answer", key)
			}
			if cmd == nil {
				t.Errorf("%q produced no command", key)
			}
			if next.pending != nil {
				t.Errorf("%q left the request pending", key)
			}
		})
	}
}

// Whitespace is not a message. Someone who hit space or newline while
// waiting should still be able to answer.
func TestWhitespaceInThePromptBoxDoesNotBlockAnAnswer(t *testing.T) {
	m := armedPending(newTestModel(), true)
	m.input.SetValue("   \n  ")

	if _, cmd, handled := press(m, "y"); !handled || cmd == nil {
		t.Error("y did not answer with only whitespace in the prompt box")
	}
}

// The modal has to say why the keys are inert, or they look broken.
func TestTheModalExplainsItselfWhileTyping(t *testing.T) {
	p := pendingPermission{tool: "bash", description: "rm -rf build/", rule: "rm *", canAlways: true}
	if got := p.prompt(false); strings.Contains(got, "clear the prompt box") {
		t.Errorf("the hint is shown when the box is empty: %q", got)
	}
	if got := p.prompt(true); !strings.Contains(got, "clear the prompt box") {
		t.Errorf("no explanation while typing: %q", got)
	}
}

// Reopening a session replays its whole log, and both halves of a
// permission live in that log. Before this was handled, every request
// ever answered came back as a live modal and the TUI refused to send
// anything until it was answered a second time.
func TestResolvedPermissionDoesNotReplayAsAModal(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": "p1", "tool": "bash", "description": "rm -rf build/", "rule": "rm *",
	}})
	if m.pending == nil {
		t.Fatal("the request itself should still raise a modal")
	}
	m.applyEvent(events.Event{Type: events.TypePermissionResolved, Data: map[string]any{
		"id": "p1", "allow": true, "scope": "once",
	}})
	if m.pending != nil {
		t.Fatalf("answered request still pending after replay: %+v", m.pending)
	}

	// And the composer is usable again, which is the part the user meets:
	// handleEnter refuses every message while a request is pending.
	m.input.SetValue("hello")
	if _, cmd := handleEnter(m); cmd == nil {
		t.Fatal("Enter did nothing with no pending request; the session is still wedged")
	}
}

// The ids are a process-global counter, so a resolution belonging to some
// other session must not dismiss the request on screen — that would hide
// a real question and leave the turn behind it waiting forever.
func TestResolvedPermissionForAnotherIDIsIgnored(t *testing.T) {
	m := armedPending(newTestModel(), false)
	m.applyEvent(events.Event{Type: events.TypePermissionResolved, Data: map[string]any{
		"id": "p7", "allow": true, "scope": "once",
	}})
	if m.pending == nil {
		t.Fatal("an unrelated resolution dismissed the pending request")
	}
}

// A second client answering the request clears it here too, live. Before,
// the modal sat on screen until the user pressed a key that no longer
// meant anything.
func TestPermissionAnsweredElsewhereClearsTheModal(t *testing.T) {
	m := armedPending(newTestModel(), false)
	m.applyEvent(events.Event{Type: events.TypePermissionResolved, Data: map[string]any{
		"id": "p1", "allow": false, "scope": "once",
	}})
	if m.pending != nil {
		t.Fatal("modal survived the answer given in another client")
	}
}
