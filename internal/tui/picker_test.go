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

// openModelPicker drives what "/model" now does: ask the daemon which
// profiles exist, then open the list on the answer. The round trip is
// the point — which models this config can reach is the daemon's answer,
// and a config edit changes it.
func openModelPicker(t *testing.T, m Model, choices ...client.ModelChoice) Model {
	t.Helper()
	m, cmd := pressEnterWith(t, m, "/model")
	if cmd == nil {
		t.Fatal("/model asked the daemon nothing")
	}
	updated, _ := m.Update(modelViewMsg{
		view: client.ModelView{Agent: m.currentAgent, Source: "agent", Choices: choices},
		pick: true,
	})
	return updated.(Model)
}

// "/model" is the way to change who answers without already knowing what
// anything is called, which is the thing the TUI did not have: "/agent
// <name>" needs the name, and Tab cycles blind.
func TestModelCommandOpensTheAgentPicker(t *testing.T) {
	m := withAgents(newTestModel(), "general-purpose", "plan", "verify")
	m = openModelPicker(t, m)

	if m.picker == nil {
		t.Fatal("/model did not open a picker")
	}
	if len(m.picker.items) != 4 {
		t.Fatalf("picker has %d rows, want 3 agents and the way back", len(m.picker.items))
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

// The other half of the same list: a profile keeps the agent and changes
// only the model, which is what could not be asked for at all before.
func TestModelPickerOffersProfilesBesideAgents(t *testing.T) {
	m := withAgents(newTestModel(), "general-purpose")
	m = openModelPicker(t, m,
		client.ModelChoice{Profile: "big", Model: "claude-opus-5", Provider: "anthropic"},
		client.ModelChoice{Profile: "local", Model: "muse-glimmer-30b", Provider: "lmstudio"},
	)
	if m.picker == nil {
		t.Fatal("/model did not open a picker")
	}
	var labels []string
	for _, it := range m.picker.items {
		labels = append(labels, it.label)
	}
	joined := strings.Join(labels, " | ")
	for _, want := range []string{"agent general-purpose", "model big", "model local", "use the agent's model"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the picker has no %q row: %s", want, joined)
		}
	}
	// The two kinds are told apart by what picking them does, so the ids
	// have to carry which kind a row is.
	for _, it := range m.picker.items {
		if !strings.HasPrefix(it.id, "agent:") && !strings.HasPrefix(it.id, "profile:") {
			t.Errorf("row %q has an id that says nothing about what it is: %q", it.label, it.id)
		}
	}
}

// The list is what has focus while it is open. A letter typed over it
// must not land in the prompt box underneath, where it would be sent as
// part of the next message.
func TestAPickerHoldsTheKeyboard(t *testing.T) {
	m := withAgents(newTestModel(), "a", "b")
	m = openModelPicker(t, m)

	m = tapKey(t, m, 'x')
	if m.input.Value() != "" {
		t.Errorf("a keypress reached the prompt box behind the picker: %q", m.input.Value())
	}
	// It narrows the list instead, which is where a typed letter goes
	// now. No agent here is called anything with an x in it.
	if m.picker.filter != "x" {
		t.Errorf("filter = %q, want the letter typed over the picker", m.picker.filter)
	}
	if len(m.picker.items) != 0 {
		t.Errorf("filtering by x left %d rows, want none", len(m.picker.items))
	}
	// And Esc takes the narrowing back before it closes anything, so a
	// mistyped filter costs one key rather than the whole picker.
	m = tapKey(t, m, tea.KeyEscape)
	if m.picker == nil {
		t.Fatal("Esc closed the picker instead of clearing the filter")
	}
	if len(m.picker.items) != 3 {
		t.Errorf("clearing the filter left %d rows, want both agents and the way back", len(m.picker.items))
	}

	m = tapKey(t, m, tea.KeyDown)
	if m.picker.idx != 1 {
		t.Errorf("down moved the selection to %d, want 1", m.picker.idx)
	}
	// Selection stops at the ends rather than wrapping: jumping from the
	// last row to the first reads as the list moving, not the cursor.
	m = tapKey(t, m, tea.KeyDown)
	m = tapKey(t, m, tea.KeyDown)
	if m.picker.idx != 2 {
		t.Errorf("down past the last row moved to %d, want it to stay at 2", m.picker.idx)
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
	m = openModelPicker(t, m)
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
