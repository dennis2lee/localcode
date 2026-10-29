package tui

import (
	"net/http"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
)

// A refused send's echo leaves the screen as well as the transcript.
func TestAHeldRefusalFromIdleClearsItsEchoFromTheScreen(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	m.input.SetValue("!git status")
	updated, _ = handleEnter(m)
	m = updated.(Model)
	if !strings.Contains(m.viewport.View(), "!git status") {
		t.Fatalf("precondition: expected pending echo in viewport, got:\n%s", m.viewport.View())
	}

	heldErr := &client.StatusError{
		Status:  http.StatusConflict,
		Message: "! decides what this conversation is, and another client is mid-turn on it",
		Held:    "!",
	}
	updated, _ = m.Update(turnDoneMsg{text: "!git status", err: heldErr, startsTurn: true, echo: m.sends})
	m = updated.(Model)

	if m.waiting {
		t.Fatal("waiting should be false when held command was sent from idle")
	}
	if strings.Contains(m.viewport.View(), "!git status") {
		t.Fatalf("refused '!git status' echo was removed from m.transcript (%q) but remains painted in m.viewport:\n%s",
			m.transcriptText(), m.viewport.View())
	}
}

// A refusal of a !cmd sent into a running turn is on screen while that
// turn runs. The status line shows the turn, not the error, until it ends.
func TestAHeldRefusalMidTurnIsOnScreen(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	m.waiting = true
	m.input.SetValue("!git status")
	updated, _ = handleEnter(m)
	m = updated.(Model)

	heldErr := &client.StatusError{
		Status:  http.StatusConflict,
		Message: "! decides what this conversation is, and a turn is running on it",
		Held:    "!",
	}
	updated, _ = m.Update(turnDoneMsg{text: "!git status", err: heldErr})
	m = updated.(Model)

	if !m.waiting {
		t.Fatal("expected m.waiting to stay true during running turn")
	}
	rendered := m.View().Content
	if !strings.Contains(rendered, "decides what this conversation is") {
		t.Fatalf("409 refusal reason is invisible in View() while turn is running:\n%s", rendered)
	}
}

// A send that failed outright takes its echo off the screen too.
func TestAFailedSendClearsItsEchoFromTheScreen(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.input.SetValue("hello there")
	updated, _ = handleEnter(m)
	m = updated.(Model)
	if !strings.Contains(m.viewport.View(), "hello there") {
		t.Fatalf("precondition: expected the pending echo on screen, got:\n%s", m.viewport.View())
	}
	failed := &client.StatusError{Status: http.StatusInternalServerError, Message: "500: the daemon fell over"}
	updated, _ = m.Update(turnDoneMsg{text: "hello there", err: failed, startsTurn: true, echo: m.sends})
	m = updated.(Model)
	if strings.Contains(m.viewport.View(), "hello there") {
		t.Errorf("the failed send's echo is gone from the transcript but still on screen:\n%s", m.viewport.View())
	}
}
