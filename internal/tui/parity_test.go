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

// What a command-parity review against opencode turned up on this side:
// four ordinary things a terminal could not do at all, a set of words
// people arrive already typing, and a key that threw away a draft.

// The words opencode calls these. Before this they matched nothing, fell
// through as prose, and went to the model as a turn.
func TestTheWordsPeopleArriveTypingAreAnswered(t *testing.T) {
	for _, name := range []string{"/agents", "/models", "/mo", "/sessions", "/resume", "/continue"} {
		t.Run(name, func(t *testing.T) {
			m := withAgents(newTestModel(), "a", "b")
			_, ok := dispatchLocalCommand(&m, name)
			if !ok {
				t.Errorf("%s matched no local command, so it would have gone to the model", name)
			}
		})
	}
}

// An alias takes an argument wherever the command it stands for does.
func TestAnAliasTakesTheSameArgument(t *testing.T) {
	m := withAgents(newTestModel(), "plan", "code")
	if _, ok := dispatchLocalCommand(&m, "/models plan"); !ok {
		t.Fatal("/models <name> matched nothing")
	}
	if m.picker != nil {
		t.Error("/models <name> opened the picker instead of switching directly")
	}
}

// Leaving was three keystrokes people did not have: opencode has /exit,
// /quit and /q, and typing "quit" here reached the model as a prompt.
func TestEveryWayOutLeaves(t *testing.T) {
	for _, text := range []string{"/exit", "/quit", "/q", "exit", ":q", "quit"} {
		t.Run(text, func(t *testing.T) {
			m := newTestModel()
			_, cmd := pressEnterWith(t, m, text)
			if !isQuitCmd(t, cmd) {
				t.Errorf("%q did not quit", text)
			}
		})
	}
}

// One letter is too easily a real message, and /q covers the habit.
func TestABareQIsStillAMessage(t *testing.T) {
	m := newTestModel()
	_, cmd := pressEnterWith(t, m, "q")
	if isQuitCmd(t, cmd) {
		t.Error(`a bare "q" quit; it should be an ordinary message`)
	}
}

// Ctrl+C used to take a half-written prompt with it. Now the first one
// clears the line, the way a shell does, and the second leaves.
func TestCtrlCClearsADraftBeforeItLeaves(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("a message I was still writing")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = updated.(Model)
	if isQuitCmd(t, cmd) {
		t.Fatal("Ctrl+C quit with a draft in the box")
	}
	if m.input.Value() != "" {
		t.Errorf("the draft was not cleared: %q", m.input.Value())
	}
	if !strings.Contains(m.transcriptText(), "Ctrl+C again") {
		t.Errorf("nothing said how to actually leave: %q", m.transcriptText())
	}

	_, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !isQuitCmd(t, cmd) {
		t.Error("the second Ctrl+C did not leave")
	}
}

// Four session operations existed only as Web UI buttons.
func TestTheSessionCommandsExist(t *testing.T) {
	for _, name := range []string{"/new", "/fork", "/delete", "/rename a title"} {
		t.Run(name, func(t *testing.T) {
			m := newTestModel()
			if _, ok := dispatchLocalCommand(&m, name); !ok {
				t.Errorf("%s matched no local command", name)
			}
		})
	}
}

// "/rename" with nothing to rename to says so rather than sending an
// empty title.
func TestRenameWithoutATitleSaysHow(t *testing.T) {
	m := newTestModel()
	if _, ok := dispatchLocalCommand(&m, "/rename"); !ok {
		t.Fatal("/rename matched nothing")
	}
	if !strings.Contains(m.transcriptText(), "usage: /rename") {
		t.Errorf("no usage line: %q", m.transcriptText())
	}
}

// "/fork" copies the conversation and opens the copy, through the same
// message a new conversation arrives as.
func TestForkAsksTheDaemonAndOpensTheCopy(t *testing.T) {
	var forked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/fork") {
			forked = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/fork")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "copy-of-it"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event))
	cmd, ok := dispatchLocalCommand(&m, "/fork")
	if !ok || cmd == nil {
		t.Fatal("/fork produced no command")
	}
	msg, isCreated := cmd().(sessionCreatedMsg)
	if !isCreated {
		t.Fatalf("/fork returned %T, want the message a new conversation arrives as", cmd())
	}
	if msg.err != nil {
		t.Fatalf("fork: %v", msg.err)
	}
	if forked != "s1" {
		t.Errorf("forked %q, want the open conversation", forked)
	}
	if msg.id != "copy-of-it" {
		t.Errorf("opened %q, want the copy the daemon made", msg.id)
	}
}

// Ctrl+E steps to the next level this model tells apart, so effort can be
// dialled up and down without a list in the way.
func TestCtrlECyclesTheEffortLevels(t *testing.T) {
	m := newTestModel()
	m.effort = client.EffortView{Model: "m", Level: "low", Levels: []string{"low", "medium", "high"}}

	if next, ok := m.nextEffort(); !ok || next != "medium" {
		t.Errorf("next after low = %q (%v), want medium", next, ok)
	}
	m.effort.Level = "high"
	// Wrapping, unlike the picker's arrows: stopping at the top would
	// leave the only way back through a list.
	if next, ok := m.nextEffort(); !ok || next != "low" {
		t.Errorf("next after high = %q (%v), want it to wrap to low", next, ok)
	}
	// A level the list does not hold starts from the beginning rather
	// than reporting nothing to do.
	m.effort.Level = "something-else"
	if next, ok := m.nextEffort(); !ok || next != "low" {
		t.Errorf("next after an unknown level = %q (%v), want low", next, ok)
	}
}

// A model with no levels to set says so instead of pressing nothing.
func TestCtrlEOnAModelWithNoLevelsSaysSo(t *testing.T) {
	m := newTestModel()
	m.effort = client.EffortView{Model: "m"}

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = updated.(Model)
	if cmd != nil {
		t.Error("a model with no levels still sent a request")
	}
	if !strings.Contains(m.transcriptText(), "no reasoning level") {
		t.Errorf("nothing was said about it: %q", m.transcriptText())
	}
}

// "/rewind" gives the undone prompt back, so the turn can be retyped from
// where it went wrong rather than from nothing.
func TestRewindPutsTheUndonePromptBackInTheBox(t *testing.T) {
	m := newTestModel()

	m.applyEvent(events.Event{Type: events.TypeRewound, Data: map[string]any{
		"turn_text": "write the parser", "prompt": "write the parser, but in one pass",
	}})
	if got := m.input.Value(); got != "write the parser, but in one pass" {
		t.Errorf("the prompt was not restored: %q", got)
	}
}

// Whatever is already typed is a newer intention than the one being
// undone, so it is not overwritten.
func TestRewindLeavesATypedPromptAlone(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("something else entirely")

	m.applyEvent(events.Event{Type: events.TypeRewound, Data: map[string]any{
		"prompt": "write the parser, but in one pass",
	}})
	if got := m.input.Value(); got != "something else entirely" {
		t.Errorf("a typed prompt was overwritten with the undone one: %q", got)
	}
}
