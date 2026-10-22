package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localcode/internal/memory"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// These go through SendMessage, so they fail when a hook is taken out of
// the turn rather than only when the function behind it breaks: the
// function tests in memory_tools_test.go call those directly.

func textTurn(text string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: text},
		{Type: provider.EventMessageStop, StopReason: "end_turn"},
	}
}

func toolTurn(id, name, input string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventToolUseStart, ToolUseID: id, ToolName: name},
		{Type: provider.EventToolUseEnd, ToolUseID: id, ToolInput: json.RawMessage(input)},
		{Type: provider.EventMessageStop, StopReason: "tool_use"},
	}
}

func memoryScriptedLoop(t *testing.T, turns ...[]provider.StreamEvent) (*Loop, string, *scriptedProvider) {
	t.Helper()
	p := &scriptedProvider{turns: turns}
	reg := tools.NewRegistry(nil)
	reg.Register(tools.ReadFile{})
	loop, sid := scriptedLoop(t, p, reg)
	withMemory(t, loop)
	for _, tool := range NewMemoryTools(loop) {
		reg.Register(tool)
	}
	return loop, sid, p
}

func TestATurnCountsItsConversationAndANoteItNamesIsUsed(t *testing.T) {
	loop, sid, _ := memoryScriptedLoop(t, textTurn("Going by gateway-key, send the team header."))
	cfg := loop.Memory
	st := loop.projectStore(sid)
	if _, err := st.Save(memory.Entry{Name: "gateway-key", Type: memory.TypeReference, Body: "X-Team-Key"}, nil, cfg.now()); err != nil {
		t.Fatal(err)
	}
	later := cfg.now().AddDate(0, 0, 20)
	cfg.Now = func() time.Time { return later }
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "how do I call the gateway?"); err != nil {
		t.Fatal(err)
	}
	use, _, sessions := st.UsageOf()
	if sessions != 1 {
		t.Errorf("the turn counted %d conversations, want 1", sessions)
	}
	if !use["gateway-key"].LastUsed.Equal(later) {
		t.Errorf("the note the answer named was last used %v, want %v", use["gateway-key"].LastUsed, later)
	}
}

func TestANoteOpenedWithReadFileDuringATurnIsUsed(t *testing.T) {
	loop, sid, _ := memoryScriptedLoop(t,
		toolTurn("c1", "read_file", `{"path":"opened-note.md"}`),
		textTurn("done"),
	)
	cfg := loop.Memory
	st := loop.projectStore(sid)
	if _, err := st.Save(memory.Entry{Name: "opened-note", Type: memory.TypeProject, Body: "o"}, nil, cfg.now()); err != nil {
		t.Fatal(err)
	}
	// The workspace is the memory directory, so the read is inside it
	// and nobody is asked.
	if _, err := loop.Store.SetWorkspace(sid, cfg.ProjectDir); err != nil {
		t.Fatal(err)
	}
	later := cfg.now().AddDate(0, 0, 3)
	cfg.Now = func() time.Time { return later }
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "read it"); err != nil {
		t.Fatal(err)
	}
	if use, _, _ := st.UsageOf(); !use["opened-note"].LastUsed.Equal(later) {
		t.Errorf("read_file of a note was not counted: %+v", use["opened-note"])
	}
}

// A rewind takes back the notes of the turn it undoes, and a redo puts
// them back, live, without a restart in between.
func TestRewindAndRedoMoveTheTurnsSessionNotes(t *testing.T) {
	loop, sid, _ := memoryScriptedLoop(t,
		toolTurn("c1", memorySaveTool, `{"scope":"session","name":"plan-b","content":"try the second approach"}`),
		textTurn("noted"),
	)
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "remember plan b for now"); err != nil {
		t.Fatal(err)
	}
	if notes := loop.sessionNotesOf(sid); len(notes) != 1 || notes[0].Name != "plan-b" {
		t.Fatalf("notes after the turn: %+v", notes)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/rewind"); err != nil {
		t.Fatal(err)
	}
	if notes := loop.sessionNotesOf(sid); len(notes) != 0 {
		t.Errorf("notes after /rewind: %+v", notes)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/redo"); err != nil {
		t.Fatal(err)
	}
	if notes := loop.sessionNotesOf(sid); len(notes) != 1 || notes[0].Content != "try the second approach" {
		t.Errorf("notes after /redo: %+v", notes)
	}
}

// Compaction starts the cached prefix over, so it is when another
// session's notes, and this one's own, are read again.
func TestCompactionRereadsTheRecalledIndex(t *testing.T) {
	loop, sid, _ := memoryScriptedLoop(t,
		textTurn("first answer"),
		textTurn("the summary"),
	)
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(loop.memoryIndexFor(sid), "late-note") {
		t.Fatal("a note that does not exist yet")
	}
	other := memory.Store{Dir: loop.Memory.ProjectDir, Scope: memory.ScopeProject, Limits: memory.ProjectLimits}
	if _, err := other.Save(memory.Entry{Name: "late-note", Type: memory.TypeProject, Body: "saved elsewhere"}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(loop.memoryIndexFor(sid), "late-note") {
		t.Fatal("another session's save reached this session before anything refreshed it")
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/compact"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loop.memoryIndexFor(sid), "late-note") {
		t.Error("compaction did not read memory again")
	}
}

// Archiving lets go of what the process holds for a conversation, its
// session notes included, and retrieving rebuilds them from the log.
func TestArchivingReleasesSessionNotesAndRetrievingRebuildsThem(t *testing.T) {
	loop, sid := testLoop(t, "")
	withMemory(t, loop)
	mustMem(t, loop, sid, MemorySaveTool{loop}, `{"scope":"session","name":"n","content":"kept in the log"}`)
	loop.ReleaseSessionMemory(sid)
	if notes := loop.sessionNotesOf(sid); len(notes) != 0 {
		t.Fatalf("archiving kept the notes in memory: %+v", notes)
	}
	loop.RehydrateSession(sid)
	if notes := loop.sessionNotesOf(sid); len(notes) != 1 {
		t.Errorf("retrieving did not rebuild the notes: %+v", notes)
	}
	loop.ClearSessionState(sid)
	if notes := loop.sessionNotesOf(sid); len(notes) != 0 {
		t.Errorf("deleting kept the notes: %+v", notes)
	}
}

// The tools a turn is offered decide whether it is told about them, end
// to end: with the tools registered the request carries the policy.
func TestTheRequestCarriesThePolicyWhenTheToolsAreOffered(t *testing.T) {
	loop, sid, p := memoryScriptedLoop(t, textTurn("ok"))
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hi"); err != nil {
		t.Fatal(err)
	}
	sys := strings.Join(p.systemPrompts(), "\n")
	if !strings.Contains(sys, "Auto memory: the memory tools") {
		t.Errorf("the request lacks the memory policy:\n%s", sys)
	}
	if strings.Contains(sys, filepath.Base(loop.Memory.ProjectDir)+string(filepath.Separator)) {
		t.Errorf("the policy names a directory")
	}
}
