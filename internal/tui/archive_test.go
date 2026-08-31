package tui

import (
	"strings"
	"testing"
	"time"

	"localcode/internal/session"
)

// /archive and /retrieve.
//
// Local commands, not daemon ones. They change server state, but routing
// them through POST /messages would send them to the one endpoint that
// refuses an archived conversation, and the completion guard in
// internal/agent requires every command literal there to be in
// SlashCommands(). renderHelp and completionCandidates both read
// localCommands(), so both are covered with no second list.

func localCommandNamed(t *testing.T, name string) localCommand {
	t.Helper()
	for _, c := range localCommands() {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no local command %q", name)
	return localCommand{}
}

func TestArchiveAndRetrieveAreLocalCommands(t *testing.T) {
	for _, name := range []string{"/archive", "/retrieve"} {
		c := localCommandNamed(t, name)
		if c.help == "" {
			t.Errorf("%s has no help, so /help does not describe it", name)
		}
		if c.run == nil {
			t.Errorf("%s does nothing", name)
		}
	}
	// /retrieve takes an id, /archive takes nothing: the conversation it
	// archives is the one you are in.
	if localCommandNamed(t, "/archive").takesArg {
		t.Error("/archive takes an argument")
	}
	if !localCommandNamed(t, "/retrieve").takesArg {
		t.Error("/retrieve cannot be given an id")
	}
}

// Both are completable and both are in /help, for free, because the table
// that dispatches them is the one both read.
func TestArchiveAndRetrieveAreCompletableAndDocumented(t *testing.T) {
	m := Model{}
	cands := m.completionCandidates()
	for _, want := range []string{"/archive", "/retrieve"} {
		found := false
		for _, c := range cands {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s cannot be completed", want)
		}
		if !strings.Contains(renderHelp(), want) {
			t.Errorf("%s is not in /help", want)
		}
	}
}

// The picker is the one /session uses, aimed at the other list.
func TestRetrieveOffersThePutAwayConversations(t *testing.T) {
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	m := Model{sessionID: "s1"}
	got, _ := m.handleArchivedSessionsMsg(archivedSessionsMsg{sessions: []session.Session{
		{ID: "s7", Title: "the one about parsers", Agent: "oracle", ArchivedAt: &at},
		{ID: "s8", Agent: "general-purpose", ArchivedAt: &at},
	}})
	mm := got.(Model)
	if mm.picker == nil {
		t.Fatal("no picker opened")
	}
	if mm.picker.title != "Archived" {
		t.Errorf("title = %q", mm.picker.title)
	}
	if len(mm.picker.items) != 2 {
		t.Fatalf("%d items", len(mm.picker.items))
	}
	if mm.picker.items[0].label != "the one about parsers" {
		t.Errorf("label = %q", mm.picker.items[0].label)
	}
	// An id, for the one with no title, since that is all there is to
	// show.
	if mm.picker.items[1].label != "s8" {
		t.Errorf("untitled label = %q", mm.picker.items[1].label)
	}
	if !strings.Contains(mm.picker.items[0].detail, "archived") {
		t.Errorf("detail does not say when it was put away: %q", mm.picker.items[0].detail)
	}
	// No "(current)": the conversation you are in is never in this list.
	for _, it := range mm.picker.items {
		if strings.Contains(it.label, "current") {
			t.Errorf("an archived row is marked current: %q", it.label)
		}
	}
}

// An empty archive answers what was typed rather than raising an error.
func TestRetrieveWithAnEmptyArchiveSaysSo(t *testing.T) {
	m := Model{}
	got, cmd := m.handleArchivedSessionsMsg(archivedSessionsMsg{})
	mm := got.(Model)
	if mm.picker != nil {
		t.Error("a picker was opened with nothing in it")
	}
	if cmd != nil {
		t.Error("an empty archive issued a command")
	}
	if !strings.Contains(transcriptText(mm), "No archived conversations") {
		t.Errorf("transcript = %q", transcriptText(mm))
	}
	if mm.errMsg != "" {
		t.Errorf("an answer to what was typed was put on the error line: %q", mm.errMsg)
	}
}

// The daemon's refusal names what is still running, and it is passed
// through rather than summarised: "wait for them or cancel them first" is
// the part worth reading.
func TestARefusedArchiveKeepsTheReason(t *testing.T) {
	m := Model{sessionID: "s1"}
	got, cmd := m.handleSessionArchived(sessionArchivedMsg{
		id:  "s1",
		err: errString("session s1 has 2 background task(s) still running; wait for them or cancel them first"),
	})
	mm := got.(Model)
	if cmd != nil {
		t.Error("a refused archive still went looking for somewhere to land")
	}
	if !strings.Contains(mm.errMsg, "background task") || !strings.Contains(mm.errMsg, "cancel them") {
		t.Errorf("errMsg = %q", mm.errMsg)
	}
}

// The landing list is read after the archive, and the conversation just
// archived is never the answer.
func TestLandingSkipsTheConversationJustArchived(t *testing.T) {
	m := Model{sessionID: "s1"}
	_, cmd := m.handleLandingSessions(landingSessionsMsg{sessions: []session.Session{
		{ID: "s1"}, {ID: "s2"},
	}})
	if cmd == nil {
		t.Fatal("nothing was opened")
	}

	// Nothing left at all: a new conversation rather than a dead end. The
	// TUI has no /new and the pre-TUI picker is behind a restart.
	m2 := Model{sessionID: "s1"}
	got, cmd2 := m2.handleLandingSessions(landingSessionsMsg{sessions: []session.Session{{ID: "s1"}}})
	if cmd2 == nil {
		t.Fatal("the only conversation was archived and nothing was started")
	}
	if !strings.Contains(transcriptText(got.(Model)), "only conversation") {
		t.Errorf("transcript = %q", transcriptText(got.(Model)))
	}
}

// transcriptText is everything the model has written to its transcript,
// joined, so a test can assert on what the reader was told.
func transcriptText(m Model) string {
	var b strings.Builder
	for _, e := range m.transcript {
		b.WriteString(e.text)
		b.WriteString("\n")
	}
	return b.String()
}

type errString string

func (e errString) Error() string { return string(e) }
