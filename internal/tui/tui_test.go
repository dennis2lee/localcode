package tui

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

// transcriptText joins every transcript entry's raw text, unstyled — what
// these tests check content against instead of the ANSI renderTranscript
// produces, which encodes an implementation detail (view.go's current
// styling) the tests below have no business depending on.
func (m Model) transcriptText() string {
	texts := make([]string, len(m.transcript))
	for i, e := range m.transcript {
		texts[i] = e.text
	}
	return strings.Join(texts, "\n\n")
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
	if !strings.Contains(m.transcriptText(), "Available commands") {
		t.Errorf("transcript = %q, want it to contain the help text", m.transcriptText())
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

	if !strings.Contains(m.transcriptText(), "1.2.3") {
		t.Errorf("transcript = %q, want it to contain the fetched version", m.transcriptText())
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
	if len(m.transcriptText()) != 0 {
		t.Errorf("transcript = %v, want it untouched by an agent.switched event (footer already shows the current agent)", m.transcriptText())
	}
}

func TestAgentCommandListsLocally(t *testing.T) {
	m := newTestModel()
	m.agents = []client.AgentInfo{{Name: "build", Description: "implements features"}}

	m, cmd := pressEnterWith(t, m, "/agent")
	if cmd != nil {
		t.Errorf("/agent (list) should not issue a command, got %v", cmd)
	}
	if !strings.Contains(m.transcriptText(), "build") || !strings.Contains(m.transcriptText(), "implements features") {
		t.Errorf("transcript = %q, want it to list the known agent", m.transcriptText())
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

// TestStreamedDeltasCoalesceIntoOneEntry pins the E5 structured-transcript
// invariant: consecutive message.part.delta events for the same model
// message accumulate into a single transcript entry rather than one entry
// per delta — otherwise a reply that streams in dozens of small chunks
// would turn into dozens of separate paragraphs once rendered.
func TestStreamedDeltasCoalesceIntoOneEntry(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "Hello"}})
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": ", "}})
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "world."}})

	if len(m.transcript) != 1 {
		t.Fatalf("transcript = %v, want exactly one entry for three deltas of the same message", m.transcript)
	}
	if got, want := m.transcript[0].text, "Hello, world."; got != want {
		t.Errorf("entry text = %q, want %q", got, want)
	}

	// message.part.end closes the entry; the next delta — a different model
	// message — must start a new one rather than continuing this one.
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd})
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "Second message."}})

	if len(m.transcript) != 2 {
		t.Fatalf("transcript = %v, want a second entry after message.part.end", m.transcript)
	}
	if got, want := m.transcript[1].text, "Second message."; got != want {
		t.Errorf("second entry text = %q, want %q", got, want)
	}
}

// TestUserMessageAlwaysStartsAFreshEntry guards the other half of the
// coalescing logic: a user message (or any non-delta append) must never be
// merged into an in-progress model entry, even if one happens to be open
// (which in practice it never is — a user message can't arrive mid-stream —
// but the entry boundary should not depend on that being true).
func TestUserMessageAlwaysStartsAFreshEntry(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "partial"}})
	m.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "hi"}})

	if len(m.transcript) != 2 {
		t.Fatalf("transcript = %v, want the user message kept separate from the open model entry", m.transcript)
	}
	if m.transcript[0].kind != entryModel || m.transcript[0].text != "partial" {
		t.Errorf("first entry = %+v, want the untouched model entry", m.transcript[0])
	}
	if m.transcript[1].kind != entryUser || m.transcript[1].text != "hi" {
		t.Errorf("second entry = %+v, want a fresh user entry", m.transcript[1])
	}

	// And streaming resumes as a new entry, not a continuation of either
	// entry above.
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "next"}})
	if len(m.transcript) != 3 || m.transcript[2].text != "next" {
		t.Errorf("transcript = %v, want a third, fresh model entry", m.transcript)
	}
}

// TestRefreshViewportPreservesScrollPosition is a regression guard for the
// UX bug E5 set out to fix: every transcript update used to call
// viewport.GotoBottom() unconditionally, so scrolling up mid-stream to
// reread something was immediately undone by the next delta. Scroll
// position must now only auto-follow when the viewport was already at the
// bottom.
func TestRefreshViewportPreservesScrollPosition(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = updated.(Model)

	// Enough content that the viewport can actually be scrolled away from
	// the bottom.
	for i := 0; i < 40; i++ {
		m.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": fmt.Sprintf("line %d", i)}})
	}
	if !m.viewport.AtBottom() {
		t.Fatal("setup: expected the viewport to be at the bottom after populating the transcript")
	}

	m.viewport.GotoTop()
	if m.viewport.AtBottom() {
		t.Fatal("setup: expected GotoTop to move the viewport away from the bottom")
	}

	// A new event refreshes the viewport; since it was scrolled up, it must
	// stay put instead of snapping back down.
	m.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "new message"}})
	if m.viewport.AtBottom() {
		t.Error("refreshViewport moved the viewport back to the bottom even though the user had scrolled up")
	}

	// But once genuinely at the bottom, new content keeps following it —
	// the common case while actively watching a reply stream in.
	m.viewport.GotoBottom()
	m.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "another message"}})
	if !m.viewport.AtBottom() {
		t.Error("refreshViewport should keep following the bottom when the viewport was already there")
	}
}

// Events that only move the status line must not re-render the transcript.
// Beyond the wasted work on a long session, an unconditional refresh is a
// standing chance to disturb a scroll position: tool.start/tool.end fire
// repeatedly throughout a turn, so someone scrolled up to reread something
// while tools run should not have the view touched at all.
func TestStatusOnlyEventsDoNotTouchTheViewport(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = updated.(Model)

	for i := 0; i < 40; i++ {
		m.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": fmt.Sprintf("line %d", i)}})
	}
	m.viewport.GotoTop()
	before := m.viewport.YOffset()

	for _, ev := range []events.Event{
		{Type: events.TypeToolStart, Data: map[string]any{"name": "bash"}},
		{Type: events.TypeToolEnd, Data: map[string]any{"name": "bash"}},
		{Type: events.TypeAgentSwitched, Data: map[string]any{"agent": "plan"}},
		{Type: events.TypeTaskSpawned, Data: map[string]any{"task_id": "t1", "agent": "plan"}},
		{Type: events.TypeTaskStatus, Data: map[string]any{"task_id": "t1", "status": "done"}},
		{Type: events.TypeTurnDone},
	} {
		m.applyEvent(ev)
		if got := m.viewport.YOffset(); got != before {
			t.Errorf("%s moved the viewport from %d to %d", ev.Type, before, got)
		}
	}

	// The state those events carry still landed, of course — skipping the
	// re-render must not mean skipping the update.
	if m.runningTool != "" || m.currentAgent != "plan" || m.tasks["t1"].status != "done" {
		t.Errorf("status-only events were skipped entirely: tool=%q agent=%q tasks=%v", m.runningTool, m.currentAgent, m.tasks)
	}

	// And an event that does write to the transcript still re-renders.
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "a reply"}})
	m.viewport.GotoBottom() // the new text is at the end; the view is still at the top
	if !strings.Contains(m.viewport.View(), "a reply") {
		t.Error("a transcript-writing event did not refresh the viewport")
	}
}

// TestEnterSendsPlainPromptWhileWaiting: a prompt typed during a turn
// goes out immediately. The daemon hands it to the running turn, which
// picks it up at its next tool call — so a correction reaches the model
// while it is still working, rather than after it has finished the wrong
// thing. Holding it here until the turn ended was the old behaviour.
func TestEnterSendsPlainPromptWhileWaiting(t *testing.T) {
	m := newTestModel()
	m.waiting = true

	m, cmd := pressEnterWith(t, m, "second question")

	if cmd == nil {
		t.Error("a prompt typed mid-turn was not sent; it has nowhere else to go")
	}
	if len(m.queue) != 0 {
		t.Errorf("queue = %v, want it sent rather than held", m.queue)
	}
	if m.input.Value() != "" {
		t.Errorf("input = %q, want it cleared after sending", m.input.Value())
	}
	if !strings.Contains(m.transcriptText(), "second question") {
		t.Errorf("transcript = %q, want the prompt echoed so the user can see it was accepted", m.transcriptText())
	}
	if !m.waiting {
		t.Error("waiting should remain true — the original turn hasn't finished")
	}
}

// TestEnterSendsSeveralPromptsWhileWaiting: several corrections can stack
// up during one long turn, and each is delivered in the order it was
// typed rather than the later ones replacing the earlier.
func TestEnterSendsSeveralPromptsWhileWaiting(t *testing.T) {
	m := newTestModel()
	m.waiting = true

	for _, text := range []string{"first", "second", "third"} {
		var cmd tea.Cmd
		m, cmd = pressEnterWith(t, m, text)
		if cmd == nil {
			t.Errorf("%q was not sent", text)
		}
	}
	if len(m.queue) != 0 {
		t.Errorf("queue = %v, want nothing held back", m.queue)
	}
	for _, text := range []string{"first", "second", "third"} {
		if !strings.Contains(m.transcriptText(), text) {
			t.Errorf("transcript = %q, want %q acknowledged", m.transcriptText(), text)
		}
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
	// Seeded directly: a prompt typed mid-turn now goes straight to the
	// daemon, so the only thing that still lands in this queue is a send
	// the daemon refused as busy — see TestBusySendRequeuesInsteadOfError,
	// which covers how it gets here. This test is about it being drained.
	m.queue = []string{"queued prompt"}

	// applyEvent is exactly what Update's eventMsg case calls before
	// checking whether to dequeue — drive it directly rather than through
	// Update/tea.Batch, since Batch's Cmd only returns a BatchMsg describing
	// the sub-commands rather than running them, which would make cmd()
	// here a no-op instead of the real HTTP send this test wants to check.
	// turn.done, not message.part.end, is the drain signal: part.end fires
	// per model message and a turn with tool calls has several.
	m.applyEvent(events.Event{Type: events.TypeTurnDone})
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

// pressKey drives one keypress through Update the way a real one would flow.
func pressKey(t *testing.T, m Model, code rune) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(tea.KeyPressMsg{Code: code})
	return updated.(Model), cmd
}

// newHistoryModel returns a sized model that has already submitted two
// prompts, so history recall has something to walk through.
func newHistoryModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.waiting = true // keeps submissions in the queue path, no HTTP needed
	m, _ = pressEnterWith(t, m, "first prompt")
	m, _ = pressEnterWith(t, m, "second prompt")
	m.waiting = false
	return m
}

// TestHistoryUpRecallsPreviousPrompts walks backwards through history the
// way Up does in a shell: newest first, then older, stopping at the oldest.
func TestHistoryUpRecallsPreviousPrompts(t *testing.T) {
	m := newHistoryModel(t)

	m, _ = pressKey(t, m, tea.KeyUp)
	if got := m.input.Value(); got != "second prompt" {
		t.Errorf("after one Up, input = %q, want the most recent prompt", got)
	}
	m, _ = pressKey(t, m, tea.KeyUp)
	if got := m.input.Value(); got != "first prompt" {
		t.Errorf("after two Ups, input = %q, want the older prompt", got)
	}
	// Past the oldest entry there is nothing to recall, so it stays put
	// rather than blanking the box.
	m, _ = pressKey(t, m, tea.KeyUp)
	if got := m.input.Value(); got != "first prompt" {
		t.Errorf("Up past the oldest entry changed the input to %q, want it unchanged", got)
	}
}

// TestHistoryDownRestoresTheDraft is the part that is easy to get wrong:
// text typed but not sent must survive a trip into history and come back
// when you walk forward past the newest entry.
func TestHistoryDownRestoresTheDraft(t *testing.T) {
	m := newHistoryModel(t)
	m.input.SetValue("half-typed thought")

	m, _ = pressKey(t, m, tea.KeyUp)
	if got := m.input.Value(); got != "second prompt" {
		t.Fatalf("after Up, input = %q, want the recalled prompt", got)
	}
	m, _ = pressKey(t, m, tea.KeyDown)
	if got := m.input.Value(); got != "half-typed thought" {
		t.Errorf("after Down back past the newest entry, input = %q, want the stashed draft restored", got)
	}
}

// TestHistoryDownWithoutNavigatingIsANoop: Down on a fresh prompt should
// not wipe what is being typed.
func TestHistoryDownWithoutNavigatingIsANoop(t *testing.T) {
	m := newHistoryModel(t)
	m.input.SetValue("in progress")

	m, _ = pressKey(t, m, tea.KeyDown)
	if got := m.input.Value(); got != "in progress" {
		t.Errorf("Down while not navigating history changed the input to %q, want it untouched", got)
	}
}

// TestUpMovesCursorInsideMultiLinePromptBeforeRecalling is the boundary
// rule: history recall must not steal Up from a multi-line prompt. Only
// once the cursor is already on the first row does Up reach for history.
func TestUpMovesCursorInsideMultiLinePromptBeforeRecalling(t *testing.T) {
	m := newHistoryModel(t)
	m.input.SetValue("line one\nline two")
	m.resizeLayout()
	if m.input.Line() != 1 {
		t.Fatalf("cursor starts on line %d, want the last line of a 2-line prompt", m.input.Line())
	}

	// First Up: still inside the prompt, so it moves the cursor and leaves
	// the text alone.
	m, _ = pressKey(t, m, tea.KeyUp)
	if got := m.input.Value(); got != "line one\nline two" {
		t.Fatalf("first Up recalled history (%q) instead of moving the cursor within the prompt", got)
	}
	if m.input.Line() != 0 {
		t.Fatalf("first Up left the cursor on line %d, want it moved to line 0", m.input.Line())
	}

	// Second Up: now at the top, so history takes over.
	m, _ = pressKey(t, m, tea.KeyUp)
	if got := m.input.Value(); got != "second prompt" {
		t.Errorf("Up at the top of the prompt = %q, want it to recall history", got)
	}
}

// TestSubmittingResetsHistoryNavigation confirms a recalled-and-sent entry
// puts navigation back at the composing position rather than leaving it
// parked mid-history.
func TestSubmittingResetsHistoryNavigation(t *testing.T) {
	m := newHistoryModel(t)
	m.waiting = true

	m, _ = pressKey(t, m, tea.KeyUp)
	m, _ = pressEnterWith(t, m, m.input.Value())

	if m.historyIdx != len(m.history) {
		t.Errorf("historyIdx = %d after submitting, want %d (back at the composing position)", m.historyIdx, len(m.history))
	}
	if got := m.input.Value(); got != "" {
		t.Errorf("input = %q after submitting, want it cleared", got)
	}
}

// TestHistoryCollapsesConsecutiveDuplicates keeps a repeated message from
// burying everything older behind copies of itself.
func TestHistoryCollapsesConsecutiveDuplicates(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.waiting = true
	for i := 0; i < 3; i++ {
		m, _ = pressEnterWith(t, m, "same thing")
	}
	if len(m.history) != 1 {
		t.Errorf("history = %q, want consecutive duplicates collapsed to one entry", m.history)
	}
}

// TestMidTurnPartEndKeepsWaiting is the regression test for typing during
// tool execution 409ing instead of queuing. message.part.end fires per
// model message — a turn with a tool call streams one before the tool
// runs and another after — so the first one must NOT end the wait. Only
// turn.done, emitted by the daemon after it clears its busy flag, does.
func TestMidTurnPartEndKeepsWaiting(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.waiting = true

	// First model message ends; the daemon is about to run a tool.
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "running git pull"}})
	if !m.waiting {
		t.Fatal("waiting cleared on a mid-turn message.part.end — this is exactly what made prompts typed during tool execution 409")
	}

	// A prompt typed right now goes out to the running turn.
	m, cmd := pressEnterWith(t, m, "typed during the tool")
	if cmd == nil {
		t.Error("a prompt typed mid-turn was not sent")
	}
	if len(m.queue) != 0 {
		t.Errorf("queue = %v, want the mid-turn prompt sent rather than held", m.queue)
	}

	// The real turn boundary arrives; now the wait ends.
	m.applyEvent(events.Event{Type: events.TypeTurnDone})
	if m.waiting {
		t.Error("waiting should clear on turn.done")
	}
}

// TestBusySendRequeuesInsteadOfError covers the fallback: if a send does
// reach the daemon while it is busy (a race, or a turn another client
// started), the 409 becomes a queued prompt rather than a red error line.
func TestBusySendRequeuesInsteadOfError(t *testing.T) {
	m := newTestModel()
	m.queue = []string{"already queued"}

	updated, _ := m.Update(turnDoneMsg{
		text: "bounced prompt",
		err:  &client.StatusError{Status: http.StatusConflict, Message: "409: busy"},
	})
	m = updated.(Model)

	if m.errMsg != "" {
		t.Errorf("errMsg = %q, want no error shown for a busy bounce", m.errMsg)
	}
	if len(m.queue) != 2 || m.queue[0] != "bounced prompt" {
		t.Errorf("queue = %v, want the bounced prompt re-queued at the front", m.queue)
	}
	if !m.waiting {
		t.Error("waiting should be true after a busy bounce, so the next turn.done drains the queue")
	}
	if !strings.Contains(m.transcriptText(), "[queued] bounced prompt") {
		t.Errorf("transcript = %q, want a [queued] line so the user sees what happened", m.transcriptText())
	}
}

// TestNonBusySendErrorStillShows guards the other direction: only 409
// gets the queue treatment; a real failure still surfaces.
func TestNonBusySendErrorStillShows(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(turnDoneMsg{
		text: "some prompt",
		err:  &client.StatusError{Status: http.StatusInternalServerError, Message: "500: boom"},
	})
	m = updated.(Model)

	if m.errMsg == "" {
		t.Error("a 500 should still show as an error")
	}
	if len(m.queue) != 0 {
		t.Errorf("queue = %v, want a non-busy failure not silently re-queued", m.queue)
	}
}

// TestToolEventsLeaveNoTranscriptLines is the headline of this change:
// the "[tool] running bash..." / "[tool] done" noise is gone from the
// conversation. The activity lives in the indicator below the prompt box
// instead.
func TestToolEventsLeaveNoTranscriptLines(t *testing.T) {
	m := newTestModel()
	m.waiting = true

	m.applyEvent(events.Event{Type: events.TypeToolStart, Data: map[string]any{"name": "bash"}})
	if strings.Contains(m.transcriptText(), "[tool]") {
		t.Errorf("transcript = %q, want no [tool] line", m.transcriptText())
	}
	if m.runningTool != "bash" {
		t.Errorf("runningTool = %q, want the indicator to know which tool is running", m.runningTool)
	}
	if !strings.Contains(m.busyLine(), "bash") {
		t.Errorf("busyLine = %q, want it to name the running tool", m.busyLine())
	}

	m.applyEvent(events.Event{Type: events.TypeToolEnd, Data: map[string]any{"is_error": false}})
	if strings.Contains(m.transcriptText(), "[tool]") {
		t.Errorf("transcript = %q, want no [tool] line after tool.end either", m.transcriptText())
	}
	if m.runningTool != "" {
		t.Errorf("runningTool = %q, want it cleared when the tool ends", m.runningTool)
	}
}

// TestIndicatorClearsAtEveryTurnBoundary: the indicator must vanish when
// the model stops, however the turn ends.
func TestIndicatorClearsAtEveryTurnBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   events.Event
	}{
		{"turn.done", events.Event{Type: events.TypeTurnDone}},
		{"turn.cancelled", events.Event{Type: events.TypeTurnCancelled}},
		{"error", events.Event{Type: events.TypeError, Data: map[string]any{"error": "boom"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.waiting = true
			m.applyEvent(events.Event{Type: events.TypeToolStart, Data: map[string]any{"name": "bash"}})

			m.applyEvent(tc.ev)

			if m.waiting {
				t.Error("waiting should clear at the turn boundary")
			}
			if m.runningTool != "" {
				t.Errorf("runningTool = %q, want cleared", m.runningTool)
			}
			if m.busy() {
				t.Error("busy() should be false, so the indicator disappears")
			}
		})
	}
}

// TestBackgroundTaskShowsInIndicator covers the second request: a running
// background task is visible even though it produces no transcript lines.
func TestBackgroundTaskShowsInIndicator(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeTaskSpawned, Data: map[string]any{
		"task_id": "t1", "agent": "explore", "prompt": "find TODOs",
	}})

	if strings.Contains(m.transcriptText(), "[task]") {
		t.Errorf("transcript = %q, want no [task] line", m.transcriptText())
	}
	if !m.busy() {
		t.Error("busy() should be true while a background task runs, so the indicator shows")
	}
	if !strings.Contains(m.busyLine(), "1 background task") {
		t.Errorf("busyLine = %q, want the background task count", m.busyLine())
	}

	m.applyEvent(events.Event{Type: events.TypeTaskStatus, Data: map[string]any{"task_id": "t1", "status": "completed"}})
	if m.busy() {
		t.Error("busy() should be false once the task completes, so the indicator disappears")
	}
}

// TestBackgroundTaskDoesNotQueuePrompts is the key interaction: a task
// runs in its own child session, so the parent session is idle and a new
// prompt must go out immediately rather than sitting in the queue.
func TestBackgroundTaskDoesNotQueuePrompts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event))
	m.tasks = map[string]taskState{"t1": {agent: "explore", status: "running"}}

	m, cmd := pressEnterWith(t, m, "a new question")

	if len(m.queue) != 0 {
		t.Errorf("queue = %v, want the prompt sent immediately — a background task does not block the foreground session", m.queue)
	}
	if cmd == nil {
		t.Fatal("expected a send command while only a background task is running")
	}
	if !m.waiting {
		t.Error("waiting should be true after sending")
	}
}

// TestTasksCommandListsAndIsLocal: /tasks answers from client-side state,
// no model call and no server round trip for the listing itself.
func TestTasksCommandListsAndIsLocal(t *testing.T) {
	m := newTestModel()
	m.tasks = map[string]taskState{
		"t1": {agent: "explore", status: "running", prompt: "find TODOs"},
	}

	m, cmd := pressEnterWith(t, m, "/tasks")
	if cmd != nil {
		t.Errorf("/tasks should answer locally, got %v", cmd)
	}
	for _, want := range []string{"t1", "running", "explore", "find TODOs"} {
		if !strings.Contains(m.transcriptText(), want) {
			t.Errorf("transcript = %q, want it to mention %q", m.transcriptText(), want)
		}
	}
}

// TestSpinnerLoopIsSingle guards the animation: only an idle->busy edge
// may start a tick loop, or the indicator animates at double speed and
// never stops cleanly.
func TestSpinnerLoopIsSingle(t *testing.T) {
	m := newTestModel()
	m.waiting = true

	if cmd := m.startSpin(); cmd == nil {
		t.Fatal("first startSpin should return a tick command")
	}
	if cmd := m.startSpin(); cmd != nil {
		t.Error("a second startSpin while already spinning must be a no-op")
	}

	// The loop dies on its first tick after the work finishes.
	m.waiting = false
	updated, cmd := m.Update(spinTickMsg{})
	m = updated.(Model)
	if cmd != nil {
		t.Error("the tick loop should stop rescheduling once nothing is running")
	}
	if m.spinning {
		t.Error("spinning should be false after the loop stops, so a later turn can start a fresh one")
	}
}

// TestPastedTextRendersInThePromptBox is the regression test for pasted
// content showing as a blank/black run in the prompt box while Enter
// still sent the right thing — i.e. the value was correct but the frame
// did not show it.
func TestPastedTextRendersInThePromptBox(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(tea.PasteMsg{Content: "pasted content here"})
	m = updated.(Model)

	if got := m.input.Value(); got != "pasted content here" {
		t.Fatalf("input value = %q, want the pasted text", got)
	}
	if !strings.Contains(m.View().Content, "pasted content here") {
		t.Errorf("rendered frame does not show the pasted text; input box renders as %q", m.input.View())
	}
}

// TestPastedMultilineTextGrowsTheBox: a multi-line paste must make the
// prompt box taller, or later lines render outside it.
func TestPastedMultilineTextGrowsTheBox(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(tea.PasteMsg{Content: "line one\nline two\nline three"})
	m = updated.(Model)

	if got := m.input.Height(); got < 3 {
		t.Errorf("input height = %d after a 3-line paste, want at least 3 — resizeLayout must run on paste", got)
	}
	content := m.View().Content
	for _, want := range []string{"line one", "line two", "line three"} {
		if !strings.Contains(content, want) {
			t.Errorf("rendered frame missing %q from the multi-line paste", want)
		}
	}
}

// TestMalformedErrorEventStillClearsWaiting is the regression test for a
// real bug: previously, m.waiting was only cleared inside the
// `if msg, ok := ev.Data["error"].(string); ok` branch of the TypeError
// case, so an error event whose payload wasn't a string left the spinner
// running forever with no way to recover short of restarting the TUI.
// endTurn() is now called unconditionally before the type assertion.
func TestMalformedErrorEventStillClearsWaiting(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m.runningTool = "bash"

	// Data["error"] is a number, not a string — the malformed case.
	m.applyEvent(events.Event{Type: events.TypeError, Data: map[string]any{"error": 42}})

	if m.waiting {
		t.Error("waiting should clear even when the error event's payload is malformed")
	}
	if m.runningTool != "" {
		t.Errorf("runningTool = %q, want cleared", m.runningTool)
	}
	if m.errMsg == "" {
		t.Error("errMsg should still be set to something, so the user sees an error happened")
	}
}

// TestEnterWhileWaitingStillQuits confirms exit/:q work even mid-turn —
// previously only Ctrl+C could quit while a turn was in progress, since
// isPlainPrompt treats "exit"/":q" as non-plain and the waiting branch
// used to just silently drop anything that wasn't a plain prompt.
func TestEnterWhileWaitingStillQuits(t *testing.T) {
	for _, text := range []string{"exit", "EXIT", ":q"} {
		m := newTestModel()
		m.waiting = true
		_, cmd := pressEnterWith(t, m, text)
		if !isQuitCmd(t, cmd) {
			t.Errorf("input %q while waiting: expected a quit command, got %v", text, cmd)
		}
	}
}

// TestEnterWhileWaitingExplainsWhyACommandCantQueue is the other half of
// the same fix: a command typed mid-turn used to vanish with the input box
// silently cleared and no feedback at all — indistinguishable from a
// successfully queued prompt. It must now say why, instead of just being
// dropped.
func TestEnterWhileWaitingExplainsWhyACommandCantQueue(t *testing.T) {
	m := newTestModel()
	m.waiting = true

	m, cmd := pressEnterWith(t, m, "/compact")

	if cmd != nil {
		t.Errorf("a command while waiting should still be a no-op, got %v", cmd)
	}
	if len(m.queue) != 0 {
		t.Errorf("queue = %v, want commands never queued", m.queue)
	}
	if !strings.Contains(m.transcriptText(), "/compact") || !strings.Contains(m.transcriptText(), "in progress") {
		t.Errorf("transcript = %q, want an explanation that the command can't run mid-turn", m.transcriptText())
	}
}

// TestAgentsFetchErrorIsReported and TestCommandsFetchErrorIsReported pin
// B8: these two message types used to be swallowed silently on error (no
// errMsg, no transcript line), which left Tab a dead key with zero
// explanation after a failed GET /api/agents.
func TestAgentsFetchErrorIsReported(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(agentsMsg{err: fmt.Errorf("connection refused")})
	m = updated.(Model)

	if m.errMsg == "" {
		t.Error("errMsg should be set when fetching agents fails, not silently swallowed")
	}
}

func TestCommandsFetchErrorIsReported(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(commandsMsg{err: fmt.Errorf("connection refused")})
	m = updated.(Model)

	if m.errMsg == "" {
		t.Error("errMsg should be set when fetching custom commands fails, not silently swallowed")
	}
}

// TestSlashCommandsAreCaseInsensitive is the regression test for a real
// bug: "/Agent foo" used to fall through to the model as ordinary chat
// text, because the old dispatch's exact-match switch lowercased its
// subject but the separate CutPrefix-based argument checks (for "/agent
// <name>", "/tasks <id>") didn't.
func TestSlashCommandsAreCaseInsensitive(t *testing.T) {
	m := newTestModel()
	m.agents = []client.AgentInfo{{Name: "build"}, {Name: "plan"}}

	m, cmd := pressEnterWith(t, m, "/Agent")
	if cmd != nil {
		t.Errorf("/Agent (list) should not issue a command, got %v", cmd)
	}
	if !strings.Contains(m.transcriptText(), "build") {
		t.Errorf("transcript = %q, want /Agent (mixed case) to list agents same as /agent", m.transcriptText())
	}

	m2 := newTestModel()
	m2.agents = []client.AgentInfo{{Name: "build"}, {Name: "plan"}}
	_, cmd2 := pressEnterWith(t, m2, "/AGENT build")
	if cmd2 == nil {
		t.Error("/AGENT build (mixed case) should still issue a switch-agent command")
	}
}

// Regression: a model reply almost always ends with a newline of its own,
// which stacked on top of the "\n\n" separator between entries and left
// two blank lines where one was meant. The separator owns the spacing, so
// each entry's surrounding newlines are trimmed before joining.
func TestEntriesAreSeparatedByExactlyOneBlankLine(t *testing.T) {
	got := renderTranscript([]transcriptEntry{
		{kind: entryModel, text: "Here is the answer.\n"},
		{kind: entryModel, text: "\nAnd the follow-up.\n\n"},
	})
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("more than one blank line between entries: %q", got)
	}
	if !strings.Contains(got, "Here is the answer.\n\nAnd the follow-up.") {
		t.Errorf("entries not separated by exactly one blank line: %q", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("trailing newline left on the transcript: %q", got)
	}
}

// Trimming is newlines only: the leading spaces of a code line the model
// emitted are content, not spacing.
func TestRenderingKeepsIndentationInsideAnEntry(t *testing.T) {
	got := renderTranscript([]transcriptEntry{
		{kind: entryModel, text: "\nfunc main() {\n    println(\"hi\")\n}\n"},
	})
	if !strings.Contains(got, "    println") {
		t.Errorf("indentation was stripped: %q", got)
	}
}

// An entry that is nothing but newlines contributes no separator of its
// own — otherwise it would show up as a stray gap with no content.
func TestBlankEntriesDoNotProduceGaps(t *testing.T) {
	got := renderTranscript([]transcriptEntry{
		{kind: entryModel, text: "first"},
		{kind: entryModel, text: "\n\n"},
		{kind: entryModel, text: "second"},
	})
	if got != "first\n\nsecond" {
		t.Errorf("blank entry left a gap: %q", got)
	}
}

// A context-window overflow is recovered from inside the loop: the history
// is summarized and the request retried, so the turn is still running and
// a reply is still on its way. The notice the loop writes for it travels
// as an error event with recovered:true, and treating that like any other
// error stopped the spinner and painted a red failure over a session that
// then answered normally — which is precisely the "this keeps erroring"
// experience the recovery was added to remove.
func TestRecoveredErrorDoesNotEndTheTurn(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m.runningTool = "bash"

	m.applyEvent(events.Event{Type: events.TypeError, Data: map[string]any{
		"error":     "the conversation no longer fits in this model's context window; summarizing it and retrying",
		"recovered": true,
	}})

	if !m.waiting {
		t.Error("the turn is still running; the spinner should not have stopped")
	}
	if m.runningTool != "bash" {
		t.Errorf("runningTool = %q, want the running tool left alone", m.runningTool)
	}
	if m.errMsg != "" {
		t.Errorf("errMsg = %q, want no error banner for something already handled", m.errMsg)
	}
	if !strings.Contains(m.transcriptText(), "summarizing it and retrying") {
		t.Error("the notice should still be visible in the transcript, as a note")
	}
}
