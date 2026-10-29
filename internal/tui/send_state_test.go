package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

// A line sent into the TUI's own running turn that fails outright (not a
// 409) leaves waiting as it was: the turn runs on, and so do its spinner,
// its "esc to cancel" hint and the lost turn check.
func TestAFailedSendIntoARunningTurnKeepsWaiting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"the daemon could not take the message"}`)
	}))
	defer srv.Close()
	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event), false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.waiting = true // the TUI's own turn is running

	m, cmd := pressEnterWith(t, m, "also fix b.go")
	done, ok := turnDoneFrom(cmd)
	if !ok {
		t.Fatal("Enter sent nothing")
	}
	if done.startsTurn {
		t.Fatal("precondition: a send into a running turn says it starts one")
	}
	updated, _ = m.Update(done)
	m = updated.(Model)
	if !m.waiting {
		t.Error("a failed send into a running turn set waiting false while that turn still runs")
	}
}

// The same line sent twice: the first from idle, accepted, its turn not
// yet drawn; the second into that turn, refused as held. The refusal
// takes away its own echo, not the oldest echo of the text, which is the
// accepted send's.
func TestAHeldRefusalTakesItsOwnEcho(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":"! does not go to a turn already running","held":"!"}`)
	}))
	defer srv.Close()
	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event), false)
	m, _ = pressEnterWith(t, m, "!make test")
	updated, _ := m.Update(turnDoneMsg{text: "!make test", startsTurn: true}) // accepted
	m = updated.(Model)

	m, cmd := pressEnterWith(t, m, "!make test")
	done, ok := turnDoneFrom(cmd)
	if !ok {
		t.Fatal("Enter sent nothing")
	}
	updated, _ = m.Update(done)
	m = updated.(Model)
	if strings.Contains(m.transcriptText(), "[sent — the model will pick this up at its next step] !make test") {
		t.Errorf("the refused send's echo is still on screen, saying the model will pick it up; transcript = %q", m.transcriptText())
	}
	pendingLeft := false
	for _, e := range m.transcript {
		if e.kind == entryPending && e.text == "!make test" {
			pendingLeft = true
		}
	}
	if !pendingLeft {
		t.Error("the accepted send's echo was taken away by the refusal of the second send")
	}
}

// A send into a running turn that fails takes its own echo away, not the
// echo of an earlier send of the same line that was taken: that one stays
// above the model output that followed it.
func TestAFailedSendTakesItsOwnEchoNotAnEarlierOne(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		call := atomic.AddInt32(&callCount, 1)
		if call == 1 {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"status":"ok"}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"server overloaded"}`)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event), false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.waiting = true // a turn is running

	// Send 1 into the running turn: accepted
	m, cmd1 := pressEnterWith(t, m, "run tests")
	done1, ok := turnDoneFrom(cmd1)
	if !ok {
		t.Fatal("first Enter sent nothing")
	}
	updated, _ = m.Update(done1)
	m = updated.(Model)

	// Model streams some output in response to the running turn
	updated, _ = m.Update(eventMsg{
		ev: events.Event{
			Type: events.TypeMessagePartDelta,
			Data: map[string]any{"text": "model delta 1"},
		},
	})
	m = updated.(Model)

	// Send 2 into the running turn: same text, but fails
	m, cmd2 := pressEnterWith(t, m, "run tests")
	done2, ok := turnDoneFrom(cmd2)
	if !ok {
		t.Fatal("second Enter sent nothing")
	}
	updated, _ = m.Update(done2)
	m = updated.(Model)

	// In the transcript:
	// Send 1's echo should still be BEFORE "model delta 1".
	// The echo AFTER "model delta 1" (from Send 2) should have been removed!
	transcript := m.transcriptText()

	// Find the positions of the sent echo and the model delta.
	sentPos := strings.Index(transcript, "[sent — the model will pick this up at its next step] run tests")
	deltaPos := strings.Index(transcript, "model delta 1")

	if sentPos == -1 {
		t.Fatalf("no sent echo found in transcript: %q", transcript)
	}
	if deltaPos == -1 {
		t.Fatalf("no model delta found in transcript: %q", transcript)
	}

	if sentPos > deltaPos {
		t.Errorf("resolveOwnEcho removed the earlier accepted send (which came before model delta) and kept the failed send's echo (which came after model delta):\ntranscript = %q", transcript)
	}
}
