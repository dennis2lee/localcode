package tui

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
	initialViewportHeight := m.viewport.Height()
	if want := 24 - 2 - borderLines - footerLines - 1; initialViewportHeight != want {
		t.Errorf("initial viewport height = %d, want %d", initialViewportHeight, want)
	}

	m.input.SetValue("line one\nline two\nline three\nline four")
	m.resizeLayout()

	if got, want := m.input.Height(), 4; got != want {
		t.Errorf("input height after 4-line content = %d, want %d", got, want)
	}
	if m.viewport.Height() >= initialViewportHeight {
		t.Errorf("viewport height = %d, expected it to shrink below the 1-line baseline %d", m.viewport.Height(), initialViewportHeight)
	}
	if want := 24 - 2 - borderLines - footerLines - 4; m.viewport.Height() != want {
		t.Errorf("viewport height = %d, want %d", m.viewport.Height(), want)
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
	if m.viewport.Height() < 3 {
		t.Errorf("viewport height = %d, want it floored at 3 even when input maxes out", m.viewport.Height())
	}
}

func TestResizeLayoutShrinksBackAfterClear(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	m.input.SetValue("line one\nline two\nline three")
	m.resizeLayout()
	grownHeight := m.viewport.Height()

	m.input.Reset()
	m.resizeLayout()

	if m.input.LineCount() != 1 {
		t.Fatalf("expected LineCount 1 after Reset, got %d", m.input.LineCount())
	}
	if m.viewport.Height() <= grownHeight {
		t.Errorf("viewport height = %d, expected it to grow back above the grown-input height %d", m.viewport.Height(), grownHeight)
	}
}

// pressEnterWith sets the input's content, then simulates pressing Enter
// exactly the way a real keypress would flow through Update.
func pressEnterWith(t *testing.T, m Model, text string) (Model, tea.Cmd) {
	t.Helper()
	m.input.SetValue(text)
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
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
		updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m = updated.(Model)
		if cmd != nil {
			msg := cmd()
			updated, _ = m.Update(msg)
			m = updated.(Model)
		}
	}
}

func TestCurrentModelResolvesFromAgentsList(t *testing.T) {
	m := newTestModel()
	m.currentAgent = "explore"
	m.agents = []client.AgentInfo{
		{Name: "build", Model: "us.anthropic.claude-opus-4-6-v1"},
		{Name: "explore", Model: "qwen3-30b-a3b"},
	}

	model, ok := m.currentModel()
	if !ok || model != "qwen3-30b-a3b" {
		t.Errorf("currentModel() = (%q, %v), want (\"qwen3-30b-a3b\", true)", model, ok)
	}
}

func TestCurrentModelFalseWhenUnknownOrUnset(t *testing.T) {
	m := newTestModel()
	m.currentAgent = "explore"

	if _, ok := m.currentModel(); ok {
		t.Error("currentModel() should report ok=false before the agents list has loaded")
	}

	m.agents = []client.AgentInfo{{Name: "explore", Model: ""}}
	if _, ok := m.currentModel(); ok {
		t.Error("currentModel() should report ok=false when the matched agent has no model set")
	}
}

func TestViewFooterShowsModelNotTabHint(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.currentAgent = "explore"
	m.agents = []client.AgentInfo{
		{Name: "build", Model: "us.anthropic.claude-opus-4-6-v1"},
		{Name: "explore", Model: "qwen3-30b-a3b"},
	}

	view := m.View().Content
	if !strings.Contains(view, "agent: explore") {
		t.Errorf("View() = %q, want it to show the current agent", view)
	}
	if !strings.Contains(view, "model: qwen3-30b-a3b") {
		t.Errorf("View() = %q, want it to show the current agent's model", view)
	}
	if strings.Contains(view, "tab to switch") {
		t.Errorf("View() = %q, want the Tab-cycle hint removed in favor of the model name", view)
	}
}

// TestViewPutsCursorInsidePromptBox is the regression test for Korean (and
// any other IME) input appearing below the prompt box. Terminals draw IME
// composition — a Hangul syllable still being assembled — at the *physical*
// cursor. The TUI used to leave that cursor wherever the frame ended, which
// is the footer line below the prompt box, so half-typed characters showed
// up there and only jumped into the box once committed. View() must now
// report a cursor, and it must land on the prompt box's own line.
func TestViewPutsCursorInsidePromptBox(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.input.SetValue("hello")

	v := m.View()
	if v.Cursor == nil {
		t.Fatal("View().Cursor = nil, want a real terminal cursor so the IME composes inside the prompt box")
	}

	lines := strings.Split(v.Content, "\n")
	if v.Cursor.Position.Y < 0 || v.Cursor.Position.Y >= len(lines) {
		t.Fatalf("cursor row = %d, outside the frame's %d lines", v.Cursor.Position.Y, len(lines))
	}
	if got := lines[v.Cursor.Position.Y]; !strings.Contains(got, "hello") {
		t.Errorf("cursor sits on row %d (%q), want the prompt box row holding the typed text", v.Cursor.Position.Y, got)
	}
	if v.Cursor.Position.Y == len(lines)-1 {
		t.Error("cursor is on the last frame row (the agent/model footer) — that is exactly the old bug, IME text renders below the prompt box")
	}
}

// TestViewCursorAccountsForWideRunes checks the cursor column advances by
// display width, not rune count: Hangul is double width, so a 2-rune "한글"
// has to move the cursor 4 cells. Getting this wrong puts the IME
// composition (and the caret) in the middle of the text already typed.
func TestViewCursorAccountsForWideRunes(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	// Baseline with an empty prompt: whatever X the textarea's own prompt
	// glyph occupies, so the test doesn't hardcode bubbles' prompt width.
	base := m.View()
	if base.Cursor == nil {
		t.Fatal("View().Cursor = nil on an empty prompt")
	}
	originX := base.Cursor.Position.X

	m.input.SetValue("한글")
	got := m.View()
	if got.Cursor == nil {
		t.Fatal("View().Cursor = nil after typing")
	}
	if want := originX + 4; got.Cursor.Position.X != want {
		t.Errorf("cursor column after typing 2 Hangul runes = %d, want %d (4 cells, not 2 — Hangul is double width)", got.Cursor.Position.X, want)
	}
}

// TestLongModelReplyWrapsToViewportWidth is a regression test for a real
// bug: bubbles' viewport.View() renders each stored line through a
// lipgloss style with both Width() and MaxWidth() applied — the latter
// *truncates* a too-long line rather than wrapping it. Since a model
// reply is appended as one continuous string with no embedded newlines
// (the common case for prose), the whole reply became a single very
// long "line" internally, and only the first viewport-width's worth of
// it ever became visible — the rest was silently cut off, not just
// visually overflowing but actually unreachable (this TUI has no
// horizontal-scroll keybinding). refreshViewport now word-wraps the
// transcript into real newlines at the viewport's width *before* it
// reaches the viewport, so MaxWidth never has anything to truncate.
func TestLongModelReplyWrapsToViewportWidth(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = updated.(Model)

	// Distinct numbered tokens, not repeated ones, so a truncated-vs-
	// wrapped render is actually distinguishable in the assertion below.
	words := make([]string, 60)
	for i := range words {
		words[i] = fmt.Sprintf("word%d", i)
	}
	long := strings.Join(words, " ") // one long unbroken line, no newlines
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": long}})

	rendered := m.viewport.View()
	for _, line := range strings.Split(rendered, "\n") {
		if lipgloss.Width(line) > 40 {
			t.Errorf("viewport line %q is %d cells wide, want it wrapped to <= 40", line, lipgloss.Width(line))
		}
	}
	if !strings.Contains(rendered, "word59") {
		t.Errorf("viewport view = %q, want the tail of a long reply (\"word59\") still visible — MaxWidth truncation used to silently drop it", rendered)
	}
}

// TestEnterQueuesPlainPromptWhileWaiting is a regression test for a UX gap:
// pressing Enter while a turn is streaming used to silently drop the typed
// text (the input was cleared? no — text just sat there and Update did
// nothing), forcing the user to notice and retype it once the reply
// finished. A plain prompt typed mid-turn should queue instead.
func TestEnterQueuesPlainPromptWhileWaiting(t *testing.T) {
	m := newTestModel()
	m.waiting = true

	m, cmd := pressEnterWith(t, m, "second question")

	if cmd != nil {
		t.Errorf("queueing a prompt should not issue a send command yet, got %v", cmd)
	}
	if len(m.queue) != 1 || m.queue[0] != "second question" {
		t.Errorf("queue = %v, want [\"second question\"]", m.queue)
	}
	if m.input.Value() != "" {
		t.Errorf("input = %q, want it cleared after queueing", m.input.Value())
	}
	if !strings.Contains(m.transcript, "second question") {
		t.Errorf("transcript = %q, want the queued prompt echoed so the user can see it was accepted", m.transcript)
	}
	if !m.waiting {
		t.Error("waiting should remain true — the original turn hasn't finished")
	}
}

// TestEnterQueuesMultiplePrompts confirms the queue holds more than one
// message, per the requirement that several prompts can stack up while a
// single long turn is in progress.
func TestEnterQueuesMultiplePrompts(t *testing.T) {
	m := newTestModel()
	m.waiting = true

	m, _ = pressEnterWith(t, m, "first")
	m, _ = pressEnterWith(t, m, "second")
	m, _ = pressEnterWith(t, m, "third")

	if got, want := m.queue, []string{"first", "second", "third"}; !reflect.DeepEqual(got, want) {
		t.Errorf("queue = %v, want %v", got, want)
	}
}

// TestEnterDoesNotQueueCommandsWhileWaiting: local/server commands aren't
// queued, since replaying them later via sendMessage would send them as
// literal chat text to the model instead of running them as commands.
func TestEnterDoesNotQueueCommandsWhileWaiting(t *testing.T) {
	m := newTestModel()
	m.waiting = true

	m, cmd := pressEnterWith(t, m, "/help")

	if cmd != nil {
		t.Errorf("a command while waiting should still be a no-op, got %v", cmd)
	}
	if len(m.queue) != 0 {
		t.Errorf("queue = %v, want commands never queued", m.queue)
	}
}

// TestQueuedPromptAutoSendsWhenTurnFinishes drives the full loop: a prompt
// queued during a turn should be sent automatically — without the user
// pressing Enter again — the moment that turn's message.part.end event
// arrives.
func TestQueuedPromptAutoSendsWhenTurnFinishes(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event))
	m.waiting = true
	m, _ = pressEnterWith(t, m, "queued prompt")
	if len(m.queue) != 1 {
		t.Fatalf("queue = %v, want 1 queued prompt before the turn finishes", m.queue)
	}

	// applyEvent is exactly what Update's eventMsg case calls before
	// checking whether to dequeue — drive it directly rather than through
	// Update/tea.Batch, since Batch's Cmd only returns a BatchMsg describing
	// the sub-commands rather than running them, which would make cmd()
	// here a no-op instead of the real HTTP send this test wants to check.
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd})
	cmd := m.dequeue()

	if len(m.queue) != 0 {
		t.Errorf("queue = %v, want it drained once the turn finished", m.queue)
	}
	if !m.waiting {
		t.Error("waiting should be true again — the queued prompt is now its own in-flight turn")
	}
	if cmd == nil {
		t.Fatal("expected a command to send the queued prompt")
	}
	cmd() // execute the HTTP send synchronously
	if !strings.Contains(gotBody, "queued prompt") {
		t.Errorf("POST body = %q, want it to contain the queued prompt", gotBody)
	}
}

// TestDequeueNoopWhenEmptyOrStillWaiting guards the two conditions under
// which dequeue must do nothing: no turn has actually finished yet, or
// there's simply nothing queued.
func TestDequeueNoopWhenEmptyOrStillWaiting(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m.queue = []string{"pending"}
	if cmd := m.dequeue(); cmd != nil {
		t.Error("dequeue while still waiting should be a no-op")
	}

	m.waiting = false
	m.queue = nil
	if cmd := m.dequeue(); cmd != nil {
		t.Error("dequeue with an empty queue should be a no-op")
	}
}
