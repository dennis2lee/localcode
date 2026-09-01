package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// The file half of "/rewind", driven through the real tool registry.
//
// Through the registry rather than by calling the sink is the point: the
// hook has to fire where a write actually happens, for exactly two tools,
// and not for a call that was refused. Every one of those is a way to be
// silently wrong — a checkpoint of a path a write never reached restores
// a file nobody changed.

// checkpointLoop is a loop with a real session directory, because the
// pre-images live beside the log and an in-memory store deliberately
// keeps none.
func checkpointLoop(t *testing.T) (*Loop, *tools.Registry, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := session.NewStore(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	registry := tools.NewRegistry(func(context.Context, tools.Ask) (bool, error) { return true, nil })
	registry.Register(tools.WriteFile{})
	registry.Register(tools.Edit{})
	registry.Register(tools.Bash{})
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	loop := New(store, registry, map[string]provider.Provider{"local": nil}, cfg)
	registry.BeforeWrite = loop.CheckpointWrite
	return loop, registry, dir
}

// writeThrough drives a real write_file through the registry, in a session and a
// workspace, the way a turn does.
func writeThrough(t *testing.T, loop *Loop, reg *tools.Registry, sessionID, work, path, content string) tools.Result {
	t.Helper()
	ctx := tools.WithWorkingDir(WithSessionID(context.Background(), sessionID), work)
	in, _ := json.Marshal(map[string]string{"path": path, "content": content})
	return reg.Call(ctx, "write_file", in, "")
}

func checkpoints(t *testing.T, loop *Loop, sessionID string) []events.Event {
	t.Helper()
	evs, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var out []events.Event
	for _, e := range evs {
		if e.Type == events.TypeCheckpoint {
			out = append(out, e)
		}
	}
	return out
}

// The whole of the file half: what was there is put back, and what the
// turn created is removed.
func TestARewindPutsBackWhatTheTurnChanged(t *testing.T) {
	loop, reg, dir := checkpointLoop(t)
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(work, "existing.txt")
	if err := os.WriteFile(existing, []byte("the original\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const sid = "s1"
	if _, err := loop.Store.CreateSessionIn(sid, "", "general-purpose", work, true); err != nil {
		t.Fatal(err)
	}
	loop.beginTurn(sid)

	if r := writeThrough(t, loop, reg, sid, work, "existing.txt", "changed\n"); r.IsError {
		t.Fatalf("write existing: %s", r.Content)
	}
	if r := writeThrough(t, loop, reg, sid, work, "created.txt", "brand new\n"); r.IsError {
		t.Fatalf("write created: %s", r.Content)
	}
	// A second edit to the same path in the same turn must not move the
	// pre-image forward: the turn should rewind to what was there before
	// its FIRST write.
	if r := writeThrough(t, loop, reg, sid, work, "existing.txt", "changed again\n"); r.IsError {
		t.Fatalf("second write: %s", r.Content)
	}

	if got := checkpoints(t, loop, sid); len(got) != 2 {
		t.Fatalf("recorded %d checkpoints, want one per path: %+v", len(got), got)
	}

	evs, _ := loop.Store.Events(sid, 0)
	restored, removed, skipped := loop.restoreCheckpoints(sid, evs)
	if len(restored) != 1 || len(removed) != 1 || len(skipped) != 0 {
		t.Fatalf("restored=%v removed=%v skipped=%v", restored, removed, skipped)
	}

	if got, _ := os.ReadFile(existing); string(got) != "the original\n" {
		t.Errorf("the edited file is %q, want what was there before the turn's first write", got)
	}
	if _, err := os.Stat(filepath.Join(work, "created.txt")); !os.IsNotExist(err) {
		t.Error("a file the turn created survived the rewind")
	}
}

// The scope, enforced rather than promised. bash can change a file and is
// not tracked, which is Claude Code's documented limit and this project's
// too — what matters is that the boundary is in the code and not in a
// sentence somebody could stop meaning.
func TestOnlyTheFileEditingToolsAreCheckpointed(t *testing.T) {
	loop, reg, dir := checkpointLoop(t)
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(work, "byshell.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const sid = "s1"
	if _, err := loop.Store.CreateSessionIn(sid, "", "general-purpose", work, true); err != nil {
		t.Fatal(err)
	}
	loop.beginTurn(sid)

	ctx := tools.WithWorkingDir(WithSessionID(context.Background(), sid), work)
	in, _ := json.Marshal(map[string]string{"command": "echo after > byshell.txt"})
	if r := reg.Call(ctx, "bash", in, ""); r.IsError {
		t.Fatalf("bash: %s", r.Content)
	}
	if got := checkpoints(t, loop, sid); len(got) != 0 {
		t.Errorf("a bash write was checkpointed, which would make /rewind claim coverage it does not have: %+v", got)
	}
}

// A write that never happened must leave nothing to "restore". A denied
// call that recorded a pre-image would have /rewind overwrite a file the
// turn was refused permission to touch.
func TestARefusedWriteIsNotCheckpointed(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStore(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	// The handler that says no, which is the whole of this test.
	registry := tools.NewRegistry(func(context.Context, tools.Ask) (bool, error) { return false, nil })
	registry.Register(tools.WriteFile{})
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	loop := New(store, registry, map[string]provider.Provider{"local": nil}, cfg)
	registry.BeforeWrite = loop.CheckpointWrite

	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", work, true); err != nil {
		t.Fatal(err)
	}
	loop.beginTurn(sid)

	if r := writeThrough(t, loop, registry, sid, work, "denied.txt", "nope"); !r.IsError {
		t.Fatal("the write was allowed, so this proves nothing")
	}
	if got := checkpoints(t, loop, sid); len(got) != 0 {
		t.Errorf("a refused write left a checkpoint behind: %+v", got)
	}
}

// The pre-images are copies of the user's files. They go when the
// conversation goes, or they are unreachable copies sitting in ~/.localcode
// forever — nothing but that session's own events names them.
func TestDeletingASessionTakesItsCheckpoints(t *testing.T) {
	loop, reg, dir := checkpointLoop(t)
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const sid = "s1"
	if _, err := loop.Store.CreateSessionIn(sid, "", "general-purpose", work, true); err != nil {
		t.Fatal(err)
	}
	loop.beginTurn(sid)
	writeThrough(t, loop, reg, sid, work, "f.txt", "after\n")

	blobs := filepath.Join(dir, "sessions", sid+".checkpoints")
	if _, err := os.Stat(blobs); err != nil {
		t.Fatalf("no pre-image was written, so this proves nothing: %v", err)
	}
	if err := loop.Store.Delete(sid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(blobs); !os.IsNotExist(err) {
		t.Error("deleting the conversation left its copies of the user's files behind")
	}
}
