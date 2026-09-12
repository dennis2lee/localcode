package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

// NOTES:
// This test file provides release verification for terminal commands and keys
// exercised through the TUI model.
//
// Per instructions, behaviours already covered in this package are not duplicated:
//
// - /fork: Already tested end-to-end with an httptest server in parity_test.go
//   (TestForkAsksTheDaemonAndOpensTheCopy). /new, /rename, and /delete are tested here.
// - /exit, /quit, /q, and bare words "exit", "quit", ":q", plus bare "q" as an ordinary message:
//   Already covered in parity_test.go (TestEveryWayOutLeaves, TestABareQIsStillAMessage)
//   and tui_test.go (TestExitCommandsQuit).
// - Aliases (/agents, /models, /mo, /sessions, /resume, /continue) resolving and taking arguments:
//   Already covered in parity_test.go (TestTheWordsPeopleArriveTypingAreAnswered,
//   TestAnAliasTakesTheSameArgument).
// - Ctrl+C clears a half-written prompt, and a second press quits:
//   Already covered in parity_test.go (TestCtrlCClearsADraftBeforeItLeaves).
// - Ctrl+E cycles effort and wraps, or reports when model has no levels:
//   Already covered in parity_test.go (TestCtrlECyclesTheEffortLevels,
//   TestCtrlEOnAModelWithNoLevelsSaysSo).
// - /help names every daemon command from the fetched list rather than client text:
//   Already covered in help_test.go (TestEveryDaemonCommandIsNamedInTheHelp).
// - Context readout in footer and tok/s only when show_tps is on:
//   Already covered in parity_test.go (TestTheFooterShowsHowFullTheWindowIs,
//   TestTheRateIsHiddenUnlessItIsTurnedOn).
//
// The remaining behaviours required for release verification are covered below:
// - /new, /rename, /delete dispatching and making the right daemon requests via httptest
// - Ctrl+B detaching a running sub-agent and reporting when none exists
// - Picker filter narrowing by typing, Backspace removing characters, Esc clearing
//   the filter before closing the list, and Enter on empty filter doing nothing.

// /new creates a new session via the daemon and triggers opening it.
func TestNewSessionAsksTheDaemon(t *testing.T) {
	var requestedPath string
	var requestedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		requestedMethod = r.Method
		if r.Method == http.MethodPost && r.URL.Path == "/api/sessions" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "new-session-42"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event))
	cmd, ok := dispatchLocalCommand(&m, "/new")
	if !ok || cmd == nil {
		t.Fatal("/new produced no command")
	}
	msg, isCreated := cmd().(sessionCreatedMsg)
	if !isCreated {
		t.Fatalf("/new returned %T, want sessionCreatedMsg", cmd())
	}
	if msg.err != nil {
		t.Fatalf("new session: %v", msg.err)
	}
	if requestedMethod != http.MethodPost || requestedPath != "/api/sessions" {
		t.Errorf("asked %s %s, want POST /api/sessions", requestedMethod, requestedPath)
	}
	if msg.id != "new-session-42" {
		t.Errorf("new session id = %q, want new-session-42", msg.id)
	}
}

// /rename asks the daemon to set the session's title and reports confirmation.
func TestRenameSessionAsksTheDaemon(t *testing.T) {
	var requestedPath string
	var requestedMethod string
	var sentTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		requestedMethod = r.Method
		if r.Method == http.MethodPost && r.URL.Path == "/api/sessions/s1/rename" {
			var body struct {
				Title string `json:"title"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			sentTitle = body.Title
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "s1", "title": body.Title})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event))
	cmd, ok := dispatchLocalCommand(&m, "/rename awesome title")
	if !ok || cmd == nil {
		t.Fatal("/rename produced no command")
	}
	msg, isRenamed := cmd().(sessionRenamedMsg)
	if !isRenamed {
		t.Fatalf("/rename returned %T, want sessionRenamedMsg", cmd())
	}
	if msg.err != nil {
		t.Fatalf("rename session: %v", msg.err)
	}
	if requestedMethod != http.MethodPost || requestedPath != "/api/sessions/s1/rename" {
		t.Errorf("asked %s %s, want POST /api/sessions/s1/rename", requestedMethod, requestedPath)
	}
	if sentTitle != "awesome title" {
		t.Errorf("sent title = %q, want awesome title", sentTitle)
	}
	if msg.title != "awesome title" {
		t.Errorf("renamed title = %q, want awesome title", msg.title)
	}

	updated, _ := m.Update(msg)
	m = updated.(Model)
	if !strings.Contains(m.transcriptText(), "Renamed this conversation to awesome title.") {
		t.Errorf("transcript missing rename confirmation: %q", m.transcriptText())
	}
}

// /delete asks the daemon to remove the session and initiates landing navigation.
func TestDeleteSessionAsksTheDaemon(t *testing.T) {
	var requestedPath string
	var requestedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		requestedMethod = r.Method
		if r.Method == http.MethodDelete && r.URL.Path == "/api/sessions/s1" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event))
	cmd, ok := dispatchLocalCommand(&m, "/delete")
	if !ok || cmd == nil {
		t.Fatal("/delete produced no command")
	}
	msg, isDeleted := cmd().(sessionDeletedMsg)
	if !isDeleted {
		t.Fatalf("/delete returned %T, want sessionDeletedMsg", cmd())
	}
	if msg.err != nil {
		t.Fatalf("delete session: %v", msg.err)
	}
	if requestedMethod != http.MethodDelete || requestedPath != "/api/sessions/s1" {
		t.Errorf("asked %s %s, want DELETE /api/sessions/s1", requestedMethod, requestedPath)
	}
	if msg.id != "s1" {
		t.Errorf("deleted session id = %q, want s1", msg.id)
	}

	updated, landingCmd := m.Update(msg)
	m = updated.(Model)
	if !strings.Contains(m.transcriptText(), "Deleted this conversation. It does not come back.") {
		t.Errorf("transcript missing delete confirmation: %q", m.transcriptText())
	}
	if landingCmd == nil {
		t.Error("delete did not trigger landing fetch")
	}
}

// Ctrl+B asks the daemon to detach the sub-agent this turn is waiting on.
func TestCtrlBDetachesChildSubAgent(t *testing.T) {
	var requestedPath string
	var requestedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		requestedMethod = r.Method
		if r.Method == http.MethodPost && r.URL.Path == "/api/sessions/s1/detach" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"detached": true,
				"task_id":  "task-sub-1",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event))
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("Ctrl+B produced no command")
	}
	msg := cmd()
	detMsg, ok := msg.(detachedMsg)
	if !ok {
		t.Fatalf("Ctrl+B cmd returned %T, want detachedMsg", msg)
	}
	if detMsg.err != nil {
		t.Fatalf("detach error: %v", detMsg.err)
	}
	if requestedMethod != http.MethodPost || requestedPath != "/api/sessions/s1/detach" {
		t.Errorf("asked %s %s, want POST /api/sessions/s1/detach", requestedMethod, requestedPath)
	}
	if !detMsg.detached || detMsg.taskID != "task-sub-1" {
		t.Errorf("unexpected detach response: %+v", detMsg)
	}

	updated, _ = m.Update(detMsg)
	m = updated.(Model)
	if !strings.Contains(m.transcriptText(), "Let go of task-sub-1.") {
		t.Errorf("transcript missing detach confirmation: %q", m.transcriptText())
	}
}

// Ctrl+B reports when the turn is not waiting on any sub-agent.
func TestCtrlBReportsWhenNoSubAgentRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/sessions/s1/detach" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"detached": false,
				"task_id":  "",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event))
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("Ctrl+B produced no command")
	}
	msg := cmd()
	detMsg, ok := msg.(detachedMsg)
	if !ok {
		t.Fatalf("Ctrl+B cmd returned %T, want detachedMsg", msg)
	}
	if detMsg.err != nil {
		t.Fatalf("detach error: %v", detMsg.err)
	}
	if detMsg.detached {
		t.Errorf("expected detached=false, got true")
	}

	updated, _ = m.Update(detMsg)
	m = updated.(Model)
	if !strings.Contains(m.transcriptText(), "Nothing to let go of: this turn is not waiting on a sub-agent.") {
		t.Errorf("transcript missing empty notice: %q", m.transcriptText())
	}
}

// Typing narrows any picker, Backspace removes a character, Esc clears the filter
// before closing the list, and Enter on an empty result does nothing.
func TestPickerFilterLifecycle(t *testing.T) {
	m := withAgents(newTestModel(), "agent-alpha", "agent-beta")
	m = openModelPicker(t, m)

	if m.picker == nil {
		t.Fatal("picker did not open")
	}
	initialCount := len(m.picker.items)
	if initialCount < 3 {
		t.Fatalf("initial items count = %d, want at least 3", initialCount)
	}

	// 1. Typing narrows the picker.
	m = tapKey(t, m, 'b')
	if m.picker.filter != "b" {
		t.Errorf("filter = %q, want 'b'", m.picker.filter)
	}
	if len(m.picker.items) != 1 || !strings.Contains(m.picker.items[0].label, "agent-beta") {
		t.Errorf("filter 'b' matched %d items, want 1 (agent-beta)", len(m.picker.items))
	}

	// Narrow further to zero matches.
	m = tapKey(t, m, 'z')
	if m.picker.filter != "bz" {
		t.Errorf("filter = %q, want 'bz'", m.picker.filter)
	}
	if len(m.picker.items) != 0 {
		t.Errorf("filter 'bz' matched %d items, want 0", len(m.picker.items))
	}

	// 2. Enter on an empty result does nothing (picker stays open, cmd is nil).
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.picker == nil {
		t.Fatal("Enter on empty result closed the picker")
	}
	if cmd != nil {
		t.Errorf("Enter on empty result returned cmd %v, want nil", cmd)
	}
	if m.picker.filter != "bz" {
		t.Errorf("filter changed to %q after Enter on empty result", m.picker.filter)
	}

	// 3. Backspace removes a character and re-applies filter.
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = updated.(Model)
	if m.picker.filter != "b" {
		t.Errorf("filter after backspace = %q, want 'b'", m.picker.filter)
	}
	if len(m.picker.items) != 1 || !strings.Contains(m.picker.items[0].label, "agent-beta") {
		t.Errorf("filter after backspace matched %d items, want 1", len(m.picker.items))
	}

	// 4. Esc clears the filter before it closes the list.
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if m.picker == nil {
		t.Fatal("Esc closed the picker instead of clearing filter")
	}
	if m.picker.filter != "" {
		t.Errorf("filter after first Esc = %q, want empty", m.picker.filter)
	}
	if len(m.picker.items) != initialCount {
		t.Errorf("items count after filter cleared = %d, want initial %d", len(m.picker.items), initialCount)
	}

	// 5. Second Esc closes the list.
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(Model)
	if m.picker != nil {
		t.Error("second Esc did not close the picker")
	}
}
