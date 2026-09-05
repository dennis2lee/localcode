package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

// tapKey drives one keypress and keeps only the model, which is what the
// picker tests below care about.
func tapKey(t *testing.T, m Model, code rune) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyPressMsg{Code: code})
	return updated.(Model)
}

func withAgents(m Model, names ...string) Model {
	for _, n := range names {
		m.agents = append(m.agents, client.AgentInfo{Name: n, Model: "model-for-" + n})
	}
	return m
}

// "/model" is the way to change the agent without already knowing what
// the agents are called, which is the thing the TUI did not have: "/agent
// <name>" needs the name, and Tab cycles blind.
func TestModelCommandOpensTheAgentPicker(t *testing.T) {
	m := withAgents(newTestModel(), "general-purpose", "plan", "verify")
	m, _ = pressEnterWith(t, m, "/model")

	if m.picker == nil {
		t.Fatal("/model did not open a picker")
	}
	if len(m.picker.items) != 3 {
		t.Fatalf("picker has %d agents, want 3", len(m.picker.items))
	}
	if !strings.Contains(m.picker.items[0].label, "current") {
		t.Errorf("the agent in use is not marked: %q", m.picker.items[0].label)
	}
	// The model each agent resolves to is the detail worth having when
	// choosing between them.
	if m.picker.items[1].detail != "model-for-plan" {
		t.Errorf("agent detail = %q, want the model it resolves to", m.picker.items[1].detail)
	}
}

// The list is what has focus while it is open. A letter typed over it
// must not land in the prompt box underneath, where it would be sent as
// part of the next message.
func TestAPickerHoldsTheKeyboard(t *testing.T) {
	m := withAgents(newTestModel(), "a", "b")
	m, _ = pressEnterWith(t, m, "/model")

	m = tapKey(t, m, 'x')
	if m.input.Value() != "" {
		t.Errorf("a keypress reached the prompt box behind the picker: %q", m.input.Value())
	}

	m = tapKey(t, m, tea.KeyDown)
	if m.picker.idx != 1 {
		t.Errorf("down moved the selection to %d, want 1", m.picker.idx)
	}
	// Selection stops at the ends rather than wrapping: jumping from the
	// last row to the first reads as the list moving, not the cursor.
	m = tapKey(t, m, tea.KeyDown)
	if m.picker.idx != 1 {
		t.Errorf("down past the last row moved to %d, want it to stay at 1", m.picker.idx)
	}

	m = tapKey(t, m, tea.KeyEscape)
	if m.picker != nil {
		t.Error("Esc did not close the picker")
	}
}

// Selecting an agent switches to it, which is the whole point of being
// able to see them.
func TestPickingAnAgentSwitchesToIt(t *testing.T) {
	var switched string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent") {
			var body struct {
				Agent string `json:"agent"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			switched = body.Agent
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"s1","agent":"plan"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	m := withAgents(New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event)), "general-purpose", "plan")
	m, _ = pressEnterWith(t, m, "/model")
	m = tapKey(t, m, tea.KeyDown)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.picker != nil {
		t.Error("selecting left the picker open")
	}
	if cmd == nil {
		t.Fatal("selecting an agent issued no request")
	}
	cmd()
	if switched != "plan" {
		t.Errorf("switched to %q, want plan", switched)
	}
}

// "/session" is the other half: before this, a session could be chosen
// once, on the listing printed before the program starts, and the only
// way to reach another was to restart.
func TestSessionCommandListsAndSwitches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[{"id":"s1","agent":"general-purpose","title":"first"},
			                {"id":"s2","agent":"plan","title":"second"}]`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event))
	m, cmd := pressEnterWith(t, m, "/session")
	if cmd == nil {
		t.Fatal("/session issued no listing request")
	}
	updated, _ := m.Update(cmd())
	m = updated.(Model)

	if m.picker == nil {
		t.Fatal("/session did not open a picker")
	}
	if len(m.picker.items) != 2 {
		t.Fatalf("picker has %d sessions, want 2", len(m.picker.items))
	}
	if !strings.Contains(m.picker.items[0].label, "current") {
		t.Errorf("the open session is not marked: %q", m.picker.items[0].label)
	}

	// Switching leaves the old conversation behind: its transcript is
	// not the new one's, and showing it under a different session's
	// replies is worse than showing nothing.
	m.transcript = append(m.transcript, transcriptEntry{kind: entryUser, text: "from the old session"})
	m.history = []string{"from the old session"}
	m = tapKey(t, m, tea.KeyDown)
	updated, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("selecting a session issued no command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)

	if m.sessionID != "s2" {
		t.Errorf("session id = %q, want s2", m.sessionID)
	}
	if m.currentAgent != "plan" {
		t.Errorf("agent = %q, want the new session's own agent", m.currentAgent)
	}
	if strings.Contains(m.transcriptText(), "from the old session") {
		t.Error("the previous session's transcript survived the switch")
	}
	if len(m.history) != 0 {
		t.Errorf("the previous session's recall history survived the switch: %v", m.history)
	}
}

// An event already in flight when the switch happened belongs to the
// conversation being left. Showing it in the new one is how a switch
// ends up with somebody else's reply at the top.
func TestAnEventFromTheOldSessionIsDropped(t *testing.T) {
	m := newTestModel()
	m.streamGen = 1

	updated, _ := m.Update(eventMsg{gen: 0, ev: events.Event{
		Type: events.TypeMessagePartEnd,
		Data: map[string]any{"text": "a reply from the session we left"},
	}})
	m = updated.(Model)

	if strings.Contains(m.transcriptText(), "session we left") {
		t.Errorf("an event from an old stream was shown: %q", m.transcriptText())
	}
}

// A path in a picker row is cut from the front, not the back.
//
// The row is rendered as label + detail and then truncated at the
// terminal's width from the right, so an absolute path put in the middle
// of a detail loses its tail — the project directory, which is the part
// that identifies the session — and pushes whatever follows it off the
// line first. The Web UI's shortenPath makes the same choice for the
// same reason.
func TestShortenPathKeepsTheEndThatIdentifiesTheProject(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/Users/someone/work/parser", "/Users/someone/work/parser"},
		{"/Users/someone/very/deep/tree/of/directories/louvre-master", "…/directories/louvre-master"},
		{"short", "short"},
	} {
		got := shortenPath(c.in, 28)
		if got != c.want {
			t.Errorf("shortenPath(%q) = %q, want %q", c.in, got, c.want)
		}
		if len([]rune(got)) > 28 {
			t.Errorf("shortenPath(%q) is %d runes, over the budget", c.in, len([]rune(got)))
		}
	}
	// And the tail survives whatever the head was.
	long := "/Users/someone/work/some/nested/place/parser-rewrite"
	if got := shortenPath(long, 28); !strings.HasSuffix(got, "parser-rewrite") {
		t.Errorf("shortenPath dropped the identifying tail: %q", got)
	}
}
