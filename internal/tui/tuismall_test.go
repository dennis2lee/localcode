package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
	"localcode/internal/session"
)

// TestRunningToolSaysHowLongItHasBeenRunning covers the elapsed time on
// the busy line: the tool-progress line already named the running tool,
// the queue depth, and the background-task count, and how long the
// current tool has been running is the piece people actually wait on.
func TestRunningToolSaysHowLongItHasBeenRunning(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m.runningTool = "bash"
	m.toolStartedAt = time.Now().Add(-90 * time.Second)

	line := m.busyLine()
	if !strings.Contains(line, "bash") {
		t.Errorf("busyLine = %q, want it to name the running tool", line)
	}
	if !strings.Contains(line, "1m30s") {
		t.Errorf("busyLine = %q, want the elapsed time beside the tool name", line)
	}

	// The clock is stamped by the start event and cleared by the end of
	// the tool and of the turn: a line that keeps counting after the
	// tool stopped is worse than one that never counted.
	m = newTestModel()
	m.applyEvent(events.Event{Type: events.TypeToolStart, Data: map[string]any{"name": "bash"}})
	if m.toolStartedAt.IsZero() {
		t.Error("tool.start did not stamp when the tool began")
	}
	m.applyEvent(events.Event{Type: events.TypeToolEnd, Data: map[string]any{"is_error": false}})
	if m.runningTool != "" || !m.toolStartedAt.IsZero() {
		t.Errorf("tool.end left tool=%q started=%v, want both cleared", m.runningTool, m.toolStartedAt)
	}

	m.applyEvent(events.Event{Type: events.TypeToolStart, Data: map[string]any{"name": "bash"}})
	m.applyEvent(events.Event{Type: events.TypeTurnDone})
	if !m.toolStartedAt.IsZero() {
		t.Error("the turn boundary did not clear the tool clock")
	}
}

// TestCtrlRSearchesThePromptHistory covers Ctrl+R on an empty prompt: it
// opens the recall history as a picker (the same narrow-by-typing list
// every other picker is), typing narrows it, Enter takes the row into
// the prompt box, and Esc leaves the box alone.
func TestCtrlRSearchesThePromptHistory(t *testing.T) {
	m := newTestModel()
	m.history = []string{"fix login", "add tests", "deploy"}
	m.historyIdx = len(m.history)

	ctrlR := tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	updated, _ := m.Update(ctrlR)
	m = updated.(Model)
	if m.picker == nil {
		t.Fatal("Ctrl+R on an empty prompt opened no search over the history")
	}
	if len(m.picker.items) != 3 {
		t.Fatalf("search has %d rows, want one per history entry", len(m.picker.items))
	}
	if m.picker.items[0].label != "deploy" {
		t.Errorf("first row = %q, want the newest entry (search starts where Up/Down starts)", m.picker.items[0].label)
	}

	m = tapKey(t, m, 'e')
	if len(m.picker.items) != 2 {
		t.Fatalf("typing e left %d rows, want deploy and add tests (fix login has no e)", len(m.picker.items))
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.picker != nil {
		t.Error("choosing a row left the search open")
	}
	if got := m.input.Value(); got != "deploy" {
		t.Errorf("input = %q, want the chosen entry taken into the box", got)
	}

	// Esc leaves the box alone: opening the search must not cost the
	// prompt, and closing it must not write anything.
	m = newTestModel()
	m.history = []string{"fix login", "add tests", "deploy"}
	m.historyIdx = len(m.history)
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = updated.(Model)
	m = tapKey(t, m, tea.KeyEscape)
	if m.picker != nil {
		t.Error("Esc did not close the search")
	}
	if got := m.input.Value(); got != "" {
		t.Errorf("input = %q, want the box untouched by opening and leaving the search", got)
	}
}

// TestSlashOnAnEmptyPromptWithHistoryStillTypes is the regression guard
// for the binding history search used to own: "/" on an empty prompt
// with history present opened the search and swallowed the slash, so no
// slash command (/help, /model, /compact, anything) could be started
// once a session had any history at all. The search is on Ctrl+R now,
// and "/" must always reach the box, including here, where every
// command starts.
func TestSlashOnAnEmptyPromptWithHistoryStillTypes(t *testing.T) {
	m := newTestModel()
	m.history = []string{"an earlier prompt"}
	m.historyIdx = len(m.history)

	updated, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(Model)
	if m.picker != nil {
		t.Error("/ on an empty prompt with history opened the search instead of typing")
	}
	if got := m.input.Value(); got != "/" {
		t.Errorf("input = %q, want the slash in the box", got)
	}
}

// TestSlashTypesNormallyUnlessThePromptIsEmpty guards the rest of the
// "/" key: with text already typed it is the start of a command (or
// just a character) and must reach the box, and with nothing to search
// there is no list to open.
func TestSlashTypesNormallyUnlessThePromptIsEmpty(t *testing.T) {
	m := newTestModel()
	m.history = []string{"something to find"}
	m.historyIdx = len(m.history)
	m.input.SetValue("/he")

	updated, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(Model)
	if m.picker != nil {
		t.Error("/ with text already typed opened the history search instead of typing")
	}
	if !strings.Contains(m.input.Value(), "/he") {
		t.Errorf("input = %q, want the typed command left alone", m.input.Value())
	}

	// Nothing to search is also typing, not an empty list: a search
	// over no entries answers nothing and costs the slash.
	m = newTestModel()
	updated, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(Model)
	if m.picker != nil {
		t.Error("/ with no history opened a search with nothing in it")
	}
}

// TestSessionPickerConfirmsBeforeDeleting covers ctrl+d in the
// /session picker: the first press only marks the row (the highlight is
// easy to be one row off), the second issues the delete, and the marked
// row is named on screen so the question says what would go.
func TestSessionPickerConfirmsBeforeDeleting(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(sessionsMsg{sessions: []session.Session{
		{ID: "s1", Title: "first", Agent: "general-purpose"},
		{ID: "s2", Title: "second", Agent: "plan"},
	}})
	m = updated.(Model)
	if m.picker == nil {
		t.Fatal("sessions did not open a picker")
	}
	if m.picker.onDelete == nil {
		t.Fatal("the session picker sets no delete, so ctrl+d could never mean anything there")
	}

	ctrlD := tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	m = tapKey(t, m, tea.KeyDown) // onto s2, so the test deletes a row that is not the open one

	updated, cmd := m.Update(ctrlD)
	m = updated.(Model)
	if m.picker == nil {
		t.Fatal("the first ctrl+d closed the picker instead of marking the row")
	}
	if cmd != nil {
		t.Error("the first ctrl+d issued a command without asking")
	}
	if m.picker.confirmID != "s2" {
		t.Errorf("marked row = %q, want s2 (the highlight, not the open session)", m.picker.confirmID)
	}
	if !strings.Contains(m.pickerView(80, 20), "confirm") {
		t.Error("the marked picker names no confirmation")
	}

	updated, cmd = m.Update(ctrlD)
	m = updated.(Model)
	if m.picker != nil {
		t.Error("confirming left the picker open over a list that just lost a row")
	}
	if cmd == nil {
		t.Fatal("the second ctrl+d issued no delete")
	}
}

// TestSessionPickerDeleteReachesTheDaemon covers the wire half: the
// confirmed deletion goes through the same daemon call /delete uses,
// and the daemon's answer lands the same way. In the httptest style the
// existing session tests already use.
func TestSessionPickerDeleteReachesTheDaemon(t *testing.T) {
	var deleted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/sessions/") {
			deleted = strings.TrimPrefix(r.URL.Path, "/api/sessions/")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event))
	updated, _ := m.Update(sessionsMsg{sessions: []session.Session{
		{ID: "s1", Title: "first", Agent: "general-purpose"},
		{ID: "s2", Title: "second", Agent: "plan"},
	}})
	m = updated.(Model)

	ctrlD := tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	m = tapKey(t, m, tea.KeyDown) // onto s2
	updated, _ = m.Update(ctrlD)
	m = updated.(Model)
	updated, cmd := m.Update(ctrlD)
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("the second ctrl+d issued no delete")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if deleted != "s2" {
		t.Errorf("daemon saw DELETE for %q, want s2 (the marked row, not the open session)", deleted)
	}
	if !strings.Contains(m.transcriptText(), "Deleted this conversation. It does not come back.") {
		t.Errorf("transcript = %q, want the daemon's answer the way /delete shows it", m.transcriptText())
	}
}

// TestSessionPickerDeleteIsDisarmedByAnythingElse covers the other half
// of the confirmation: moving, typing, or Esc after the first ctrl+d
// keeps the conversation, so an armed row cannot survive a change of
// selection.
func TestSessionPickerDeleteIsDisarmedByAnythingElse(t *testing.T) {
	// No daemon round trip: the picker opens straight off the listing
	// message, so the confirm/disarm flow needs no server at all.
	open := func(t *testing.T) Model {
		t.Helper()
		m := newTestModel()
		updated, _ := m.Update(sessionsMsg{sessions: []session.Session{
			{ID: "s1", Title: "first", Agent: "general-purpose"},
			{ID: "s2", Title: "second", Agent: "plan"},
		}})
		m = updated.(Model)
		if m.picker == nil {
			t.Fatal("sessions did not open a picker")
		}
		return m
	}
	ctrlD := tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}

	m := open(t)
	updated, _ := m.Update(ctrlD)
	m = updated.(Model)
	if m.picker.confirmID == "" {
		t.Fatal("the first ctrl+d marked no row")
	}
	m = tapKey(t, m, tea.KeyDown)
	if m.picker == nil || m.picker.confirmID != "" {
		t.Fatal("moving after the first ctrl+d kept the row armed under a moved highlight")
	}
	updated, cmd := m.Update(ctrlD)
	m = updated.(Model)
	if cmd != nil {
		t.Error("ctrl+d after moving deleted: the mark should have died with the move")
	}

	m = open(t)
	updated, _ = m.Update(ctrlD)
	m = updated.(Model)
	m = tapKey(t, m, tea.KeyEscape)
	if m.picker == nil {
		t.Error("Esc after the first ctrl+d closed the picker instead of just keeping the row")
	} else if m.picker.confirmID != "" {
		t.Error("Esc left the row armed")
	}
	if len(m.picker.items) != 2 {
		t.Errorf("picker has %d rows after Esc, want both sessions still there", len(m.picker.items))
	}
}

// TestOnlyTheSessionPickerDeletes covers the key's scope: ctrl+d in any
// other picker is swallowed like every other non-filter key, because no
// other list set what deleting means.
func TestOnlyTheSessionPickerDeletes(t *testing.T) {
	m := newTestModel()
	m.history = []string{"one", "two"}
	m.historyIdx = len(m.history)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m = updated.(Model)
	if m.picker == nil {
		t.Fatal("history search did not open")
	}

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = updated.(Model)
	if m.picker == nil {
		t.Error("ctrl+d closed a picker that cannot delete")
	}
	if cmd != nil {
		t.Error("ctrl+d in the history search issued a command")
	}
}

// TestFooterDrawsABarBesideTheContextNumber covers the graphical bar:
// it stands next to the percentage, its fill grows with the number, and
// the warning thresholds are exactly where they were (amber past 70,
// red past 90, matching the Web UI).
func TestFooterDrawsABarBesideTheContextNumber(t *testing.T) {
	sized := func(t *testing.T, m Model) Model {
		t.Helper()
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		return updated.(Model)
	}

	m := sized(t, newTestModel())
	m.usagePercent = 42.5
	view := m.View().Content
	if !strings.Contains(view, "context: 42.5%") {
		t.Fatalf("footer lost the number the bar stands beside: %q", lastLine(view))
	}
	bar42 := contextBar(42.5)
	if !strings.Contains(view, bar42) {
		t.Errorf("footer = %q, want the bar %q beside the number", lastLine(view), bar42)
	}

	// The fill grows with the number: a bar that does not is decoration.
	if fill := strings.Count(contextBar(80), "█"); fill <= strings.Count(bar42, "█") {
		t.Errorf("80%% fills %d cells, not more than 42.5%%'s %d", fill, strings.Count(bar42, "█"))
	}

	// Half-full at the compaction point, not nearly empty: in a default
	// setup auto-compaction fires at 50%, so the window never simply
	// fills to 100% and the bar must not read as a countdown to it.
	half := contextBar(50)
	if got := strings.Count(half, "█"); got != contextBarWidth/2 {
		t.Errorf("50%% fills %d of %d cells, want half", got, contextBarWidth)
	}
	if got := len([]rune(half)); got != contextBarWidth {
		t.Errorf("bar is %d cells wide, want %d at every fill", got, contextBarWidth)
	}
	if got := len([]rune(contextBar(0))); got != contextBarWidth {
		t.Errorf("an empty window still draws %d cells, want %d (the bar keeps its shape)", got, contextBarWidth)
	}
	if got := strings.Count(contextBar(100), "░"); got != 0 {
		t.Errorf("100%% leaves %d empty cells, want a full bar", got)
	}

	// Out of range is clamped, not wrapped or panicking: the number
	// comes off the wire and has been surprising before.
	if got := strings.Count(contextBar(140), "░"); got != 0 {
		t.Errorf("140%% leaves %d empty cells, want it clamped to full", got)
	}
	if got := strings.Count(contextBar(-3), "█"); got != 0 {
		t.Errorf("-3%% fills %d cells, want it clamped to empty", got)
	}
}
