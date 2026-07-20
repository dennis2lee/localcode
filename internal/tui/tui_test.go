package tui

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"localcode/internal/client"
	"localcode/internal/events"
)

// newTestModel builds a Model without touching the network — fine here
// since these tests only drive Update()/resizeLayout() directly, never
// Init() or a real tea.Program, so the client is never called.
func newTestModel() Model {
	ch := make(chan events.Event)
	return New(client.New("http://unused.invalid"), "s1", "general-purpose", ch)
}

func TestResizeLayoutGrowsWithContent(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	if got, want := m.input.LineCount(), 1; got != want {
		t.Fatalf("initial LineCount = %d, want %d", got, want)
	}
	initialViewportHeight := m.viewport.Height
	if want := 24 - 2 - borderLines - footerLines - 1; initialViewportHeight != want {
		t.Errorf("initial viewport height = %d, want %d", initialViewportHeight, want)
	}

	m.input.SetValue("line one\nline two\nline three\nline four")
	m.resizeLayout()

	if got, want := m.input.Height(), 4; got != want {
		t.Errorf("input height after 4-line content = %d, want %d", got, want)
	}
	if m.viewport.Height >= initialViewportHeight {
		t.Errorf("viewport height = %d, expected it to shrink below the 1-line baseline %d", m.viewport.Height, initialViewportHeight)
	}
	if want := 24 - 2 - borderLines - footerLines - 4; m.viewport.Height != want {
		t.Errorf("viewport height = %d, want %d", m.viewport.Height, want)
	}
}

func TestResizeLayoutClampsToMaxHeight(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	huge := ""
	for i := 0; i < inputMaxHeight+10; i++ {
		huge += "line\n"
	}
	m.input.SetValue(huge)
	m.resizeLayout()

	if got := m.input.Height(); got != inputMaxHeight {
		t.Errorf("input height = %d, want it clamped to inputMaxHeight = %d", got, inputMaxHeight)
	}
	if m.viewport.Height < 3 {
		t.Errorf("viewport height = %d, want it floored at 3 even when input maxes out", m.viewport.Height)
	}
}

func TestResizeLayoutShrinksBackAfterClear(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	m.input.SetValue("line one\nline two\nline three")
	m.resizeLayout()
	grownHeight := m.viewport.Height

	m.input.Reset()
	m.resizeLayout()

	if m.input.LineCount() != 1 {
		t.Fatalf("expected LineCount 1 after Reset, got %d", m.input.LineCount())
	}
	if m.viewport.Height <= grownHeight {
		t.Errorf("viewport height = %d, expected it to grow back above the grown-input height %d", m.viewport.Height, grownHeight)
	}
}

// pressEnterWith sets the input's content, then simulates pressing Enter
// exactly the way a real keypress would flow through Update.
func pressEnterWith(t *testing.T, m Model, text string) (Model, tea.Cmd) {
	t.Helper()
	m.input.SetValue(text)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return updated.(Model), cmd
}

func isQuitCmd(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestExitCommandsQuit(t *testing.T) {
	for _, text := range []string{"exit", "EXIT", ":q", " :q "} {
		m := newTestModel()
		_, cmd := pressEnterWith(t, m, text)
		if !isQuitCmd(t, cmd) {
			t.Errorf("input %q: expected a quit command, got %v", text, cmd)
		}
	}
}

func TestHelpCommandRendersLocally(t *testing.T) {
	m := newTestModel()
	m, cmd := pressEnterWith(t, m, "/help")

	if cmd != nil {
		t.Errorf("/help should not issue a command (no server round trip), got %v", cmd)
	}
	if !strings.Contains(m.transcript, "Available commands") {
		t.Errorf("transcript = %q, want it to contain the help text", m.transcript)
	}
	if m.input.Value() != "" {
		t.Errorf("input should be cleared after /help, got %q", m.input.Value())
	}
}

func TestVersionCommandFetchesFromDaemon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"1.2.3"}`)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event))
	m, cmd := pressEnterWith(t, m, "/version")
	if cmd == nil {
		t.Fatal("/version should issue a command to fetch the daemon's version")
	}

	msg := cmd()
	updated, _ := m.Update(msg)
	m = updated.(Model)

	if !strings.Contains(m.transcript, "1.2.3") {
		t.Errorf("transcript = %q, want it to contain the fetched version", m.transcript)
	}
}

func TestNextAgentCyclesAndWraps(t *testing.T) {
	m := newTestModel()
	m.currentAgent = "plan"
	m.agents = []client.AgentInfo{{Name: "build"}, {Name: "explore"}, {Name: "plan"}}

	next, ok := m.nextAgent()
	if !ok || next != "build" {
		t.Errorf("nextAgent() from plan = (%q, %v), want (\"build\", true) — wraps to the start", next, ok)
	}

	m.currentAgent = "build"
	next, ok = m.nextAgent()
	if !ok || next != "explore" {
		t.Errorf("nextAgent() from build = (%q, %v), want (\"explore\", true)", next, ok)
	}
}

func TestNextAgentNoopWithFewerThanTwoAgents(t *testing.T) {
	m := newTestModel()
	if _, ok := m.nextAgent(); ok {
		t.Error("nextAgent() with no known agents should return ok=false")
	}
	m.agents = []client.AgentInfo{{Name: "solo"}}
	if _, ok := m.nextAgent(); ok {
		t.Error("nextAgent() with exactly one known agent should return ok=false (nothing to cycle to)")
	}
}

// TestTabKeySwitchesAgent drives Tab through Update against a real daemon
// stand-in and confirms it POSTs to the switch-agent endpoint for the
// *next* agent in the list.
func TestTabKeySwitchesAgent(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/agent") {
			buf, _ := io.ReadAll(r.Body)
			gotBody = string(buf)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"s1","agent":"build"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "plan", make(chan events.Event))
	m.agents = []client.AgentInfo{{Name: "build"}, {Name: "plan"}}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if cmd == nil {
		t.Fatal("Tab with 2+ known agents should issue a switch command")
	}
	cmd() // execute the HTTP call synchronously

	if !strings.Contains(gotBody, "build") {
		t.Errorf("POST body = %q, want it to request switching to \"build\"", gotBody)
	}
}

func TestAgentSwitchedEventUpdatesCurrentAgent(t *testing.T) {
	m := newTestModel()
	m.currentAgent = "plan"

	ev := events.Event{Type: events.TypeAgentSwitched, Data: map[string]any{"agent": "build"}}
	m.applyEvent(ev)

	if m.currentAgent != "build" {
		t.Errorf("currentAgent = %q, want %q after an agent.switched event", m.currentAgent, "build")
	}
	// Regression check: agent.switched must NOT also write a transcript
	// line — View() already renders the current agent in the footer on
	// every frame, so writing one here as well would leave a permanent
	// "switched to X" line behind on every single Tab press.
	if m.transcript != "" {
		t.Errorf("transcript = %q, want it untouched by an agent.switched event (footer already shows the current agent)", m.transcript)
	}
}

func TestAgentCommandListsLocally(t *testing.T) {
	m := newTestModel()
	m.agents = []client.AgentInfo{{Name: "build", Description: "implements features"}}

	m, cmd := pressEnterWith(t, m, "/agent")
	if cmd != nil {
		t.Errorf("/agent (list) should not issue a command, got %v", cmd)
	}
	if !strings.Contains(m.transcript, "build") || !strings.Contains(m.transcript, "implements features") {
		t.Errorf("transcript = %q, want it to list the known agent", m.transcript)
	}
}

// TestRepeatedTabSwitchesDoNotPanic is a regression test for a real crash:
// Model.Update has a value receiver, so bubbletea's Program copies the
// whole Model by value on every call. transcript used to be a
// strings.Builder, which embeds a self-referential pointer it uses to
// detect illegal copies — once the transcript had any content, copying
// the Model (as every Update call does) and then writing to the copy
// panicked with "strings: illegal use of non-zero Builder copied by
// value". Rapid repeated Tab presses hit this reliably in practice.
// transcript is now a plain string; this drives the exact repro sequence
// (write something to the transcript, then cycle Tab many times) to
// guard against a regression back to a self-referential type.
func TestRepeatedTabSwitchesDoNotPanic(t *testing.T) {
	m := newTestModel()
	m.agents = []client.AgentInfo{{Name: "build"}, {Name: "explore"}, {Name: "plan"}, {Name: "review"}}

	// Give the transcript non-empty content first, the same way a real
	// session would (a /help response, a streamed reply, etc.).
	m, _ = pressEnterWith(t, m, "/help")

	for i := 0; i < 50; i++ {
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(Model)
		if cmd != nil {
			msg := cmd()
			updated, _ = m.Update(msg)
			m = updated.(Model)
		}
	}
}
