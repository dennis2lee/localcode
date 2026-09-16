package tui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

// The roster a client shows must be the roster the daemon has. The daemon
// serves it at GET /api/agents and announces roster-changing flips with a
// settings.changed broadcast; a stream that comes back may be talking to a
// new process. These tests drive that whole path: the event (or re-attach)
// through Update, the re-request it issues through the real client call
// path, and the state the client shows afterwards. What is asserted is the
// offered roster, never which helper was called.

// rosterTransport answers GET /api/agents with one agent list, behind the
// real client call path, without a socket: it swaps the HTTP transport
// rather than the daemon.
type rosterTransport struct {
	agents []client.AgentInfo
}

func (rt rosterTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != "/api/agents" {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	}
	raw, _ := json.Marshal(rt.agents)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(raw)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

var rosterV1 = []client.AgentInfo{
	{Name: "general-purpose", Description: "the default agent", Model: "test-model-1"},
	{Name: "plan", Description: "read-only planner", Model: "test-model-2"},
}

var rosterV2 = []client.AgentInfo{
	{Name: "general-purpose", Description: "the default agent", Model: "test-model-9"},
	{Name: "explore", Description: "a smart specialist", Model: "test-model-3"},
}

// rosterModel builds a Model whose daemon serves agents, with a closed event
// channel so listen commands in the returned batches answer immediately
// instead of parking on a stream nobody feeds.
func rosterModel(agents []client.AgentInfo) Model {
	ch := make(chan events.Event)
	close(ch)
	c := client.New("http://daemon.invalid")
	c.HTTP = &http.Client{Transport: rosterTransport{agents: agents}}
	m := New(c, "s1", "general-purpose", ch, false)
	m.agents = append([]client.AgentInfo(nil), rosterV1...)
	return m
}

// runCmd executes one Update command, descending into batches, and returns
// every message the commands produced.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			out = append(out, sub())
		}
		return out
	}
	return []tea.Msg{msg}
}

// applyRoster feeds any agents answers in msgs back through Update and
// returns the resulting Model.
func applyRoster(t *testing.T, m Model, msgs []tea.Msg) Model {
	t.Helper()
	found := false
	for _, msg := range msgs {
		if am, ok := msg.(agentsMsg); ok {
			found = true
			if am.err != nil {
				t.Fatalf("agents fetch failed: %v", am.err)
			}
			upd, _ := m.Update(am)
			m = upd.(Model)
		}
	}
	if !found {
		t.Fatal("no roster re-request came back; want the daemon's current agents fetched")
	}
	return m
}

func offeredAgents(m Model) []string {
	names := make([]string, 0, len(m.agents))
	for _, a := range m.agents {
		names = append(names, a.Name)
	}
	return names
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// After the daemon broadcasts a settings change, the next roster on screen
// is the one the daemon serves now: the removed agent is gone, the added
// one is offered, and the kept one's label names its new model.
func TestSettingsChangeRefreshesTheAgentRoster(t *testing.T) {
	m := rosterModel(rosterV2)

	upd, cmd := m.handleServerEvent(eventMsg{
		gen: m.streamGen,
		ev:  events.Event{Type: events.TypeSettingsChanged, Data: map[string]any{"smart_agent": true}},
	})
	m = upd.(Model)
	m = applyRoster(t, m, runCmd(cmd))

	got := offeredAgents(m)
	if hasName(got, "plan") {
		t.Errorf("offered agents = %v, want no agent the daemon no longer serves", got)
	}
	if !hasName(got, "explore") {
		t.Errorf("offered agents = %v, want the agent the daemon now accepts", got)
	}
	model, ok := m.currentModel()
	if !ok || model != "test-model-9" {
		t.Errorf("currentModel = %q, %v; want the kept agent's new model test-model-9", model, ok)
	}
}

// A re-attach means the daemon may be a new process: the roster is
// re-requested with the fresh stream rather than trusted from startup.
func TestReattachingRefreshesTheAgentRoster(t *testing.T) {
	m := rosterModel(rosterV2)

	ch := make(chan events.Event)
	close(ch)
	upd, cmd := m.handleSessionSwitched(sessionSwitchedMsg{
		sessionID: "s1",
		agent:     "general-purpose",
		events:    ch,
		gen:       m.streamGen,
		reattach:  true,
	})
	m = upd.(Model)
	m = applyRoster(t, m, runCmd(cmd))

	if got := offeredAgents(m); hasName(got, "plan") || !hasName(got, "explore") {
		t.Errorf("offered agents after re-attach = %v, want the daemon's current roster", got)
	}
}

// Opening another conversation attaches to whatever daemon is behind the
// address now, so it re-requests the roster with the effort and model views.
func TestOpeningAnotherSessionRefreshesTheAgentRoster(t *testing.T) {
	m := rosterModel(rosterV2)

	ch := make(chan events.Event)
	close(ch)
	upd, cmd := m.handleSessionSwitched(sessionSwitchedMsg{
		sessionID: "s2",
		agent:     "general-purpose",
		events:    ch,
		gen:       m.streamGen,
	})
	m = upd.(Model)
	m = applyRoster(t, m, runCmd(cmd))

	if got := offeredAgents(m); hasName(got, "plan") || !hasName(got, "explore") {
		t.Errorf("offered agents after opening another session = %v, want the daemon's current roster", got)
	}
}

// The neighbour that must not change: ordinary events carry no roster news,
// so they must not cost a roster fetch. Only the settings broadcast and a
// (re)attach re-request.
func TestOrdinaryEventsDoNotReRequestTheRoster(t *testing.T) {
	m := rosterModel(rosterV2)

	upd, cmd := m.handleServerEvent(eventMsg{
		gen: m.streamGen,
		ev:  events.Event{Type: events.TypeUsage, Data: map[string]any{"percent": 12.5}},
	})
	m = upd.(Model)
	for _, msg := range runCmd(cmd) {
		if _, ok := msg.(agentsMsg); ok {
			t.Fatal("a usage event re-requested the roster; only the settings broadcast and a (re)attach may")
		}
	}

	if got := offeredAgents(m); hasName(got, "explore") || !hasName(got, "plan") {
		t.Errorf("offered agents = %v, want the startup roster untouched by an ordinary event", got)
	}
}
