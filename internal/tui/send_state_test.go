package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
