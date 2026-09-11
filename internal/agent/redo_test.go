package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"localcode/internal/events"
)

// Putting back a turn that /rewind undid.
//
// The interesting half is the files. Pre-images are kept as a matter of
// course, so undoing always worked; nothing kept the post-images, so
// redoing did not. They are taken during the rewind, in the one moment
// the turn's own result is still on disk.

// applyRewinds stops filtering a turn once a redo marker names its
// rewind. Nothing is edited, the way nothing is edited for a rewind.
func TestARedoneRewindStopsFiltering(t *testing.T) {
	evs := []events.Event{
		{Seq: 1, Type: events.TypeUserMessage, Data: map[string]any{"text": "first"}},
		{Seq: 2, Type: events.TypeUserMessage, Data: map[string]any{"text": "the undone turn"}},
		{Seq: 3, Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "an answer"}},
		{Seq: 4, Type: events.TypeRewound, Data: map[string]any{"from_seq": uint64(2)}},
	}

	// Rewound: the turn is filtered out. The range is half-open — the
	// opening message and the answer go, the marker stays — so what is
	// left is the first turn and the marker.
	if got := len(applyRewinds(evs)); got != 2 {
		t.Errorf("after a rewind %d events survive, want the first turn and the marker", got)
	}

	// Redone: it is back, and both markers are still in the log.
	evs = append(evs, events.Event{Seq: 5, Type: events.TypeRedone, Data: map[string]any{"rewind_seq": uint64(4)}})
	out := applyRewinds(evs)
	if len(out) != 5 {
		t.Fatalf("after a redo %d events survive, want all five", len(out))
	}
	var sawUndone bool
	for _, ev := range out {
		if ev.Seq == 2 {
			sawUndone = true
		}
	}
	if !sawUndone {
		t.Error("the undone turn did not come back")
	}
}

// A redo only applies while the rewind is still the last thing that
// happened. A real turn after it is the conversation moving on, and
// putting the undone turn back underneath would interleave two
// histories.
func TestRedoOnlyAppliesWhileTheRewindIsTheLastThing(t *testing.T) {
	rewind := events.Event{Seq: 4, Type: events.TypeRewound, Data: map[string]any{"from_seq": uint64(2)}}
	base := []events.Event{
		{Seq: 2, Type: events.TypeUserMessage, Data: map[string]any{"text": "the undone turn"}},
		rewind,
	}

	if _, ok := redoableRewind(base); !ok {
		t.Error("a fresh rewind is not redoable")
	}

	// A local command in between is a question, not the conversation
	// moving on.
	withCommand := append(append([]events.Event(nil), base...),
		events.Event{Seq: 5, Type: events.TypeUserMessage, Data: map[string]any{"text": "/usage", "local": true}})
	if _, ok := redoableRewind(withCommand); !ok {
		t.Error("a local command in between made the rewind unredoable")
	}

	// A real turn is.
	withTurn := append(append([]events.Event(nil), base...),
		events.Event{Seq: 5, Type: events.TypeUserMessage, Data: map[string]any{"text": "carry on then"}})
	if _, ok := redoableRewind(withTurn); ok {
		t.Error("a rewind stayed redoable after the conversation moved on")
	}

	// And twice does not put the same turn back twice.
	withRedo := append(append([]events.Event(nil), base...),
		events.Event{Seq: 5, Type: events.TypeRedone, Data: map[string]any{"rewind_seq": uint64(4)}})
	if _, ok := redoableRewind(withRedo); ok {
		t.Error("a rewind that was already redone is still offered")
	}
}

// The files, end to end: a turn changes one file and creates another,
// the rewind takes both back, and the redo puts both where the turn had
// them.
func TestRedoPutsTheFilesBack(t *testing.T) {
	loop, _, dir := checkpointLoop(t)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	changed := filepath.Join(dir, "changed.txt")
	created := filepath.Join(dir, "created.txt")
	if err := os.WriteFile(changed, []byte("before the turn"), 0o644); err != nil {
		t.Fatal(err)
	}

	// What the turn did: a checkpoint for each file, then the writes.
	ctx := WithSessionID(context.Background(), sid)
	loop.beginTurn(sid)
	loop.CheckpointWrite(ctx, "edit", changed)
	loop.CheckpointWrite(ctx, "write_file", created)
	if err := os.WriteFile(changed, []byte("after the turn"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(created, []byte("made by the turn"), 0o644); err != nil {
		t.Fatal(err)
	}

	evs, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	restored, removed, _, redoable := loop.restoreCheckpoints(sid, evs)
	if len(restored) != 1 || len(removed) != 1 {
		t.Fatalf("the rewind restored %v and removed %v, want one of each", restored, removed)
	}
	if len(redoable) != 2 {
		t.Fatalf("the rewind kept %d post-images, want one per file", len(redoable))
	}
	// The rewind actually happened.
	if b, _ := os.ReadFile(changed); string(b) != "before the turn" {
		t.Errorf("the changed file is %q, want what it was before the turn", b)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Error("the created file survived the rewind")
	}

	// And the redo puts both back where the turn had them.
	marker := events.Event{Seq: 99, Data: map[string]any{"redo": redoable}}
	written, skipped := loop.replayPostImages(sid, marker)
	if len(written) != 2 || len(skipped) != 0 {
		t.Fatalf("the redo wrote %v and skipped %v, want both written", written, skipped)
	}
	if b, _ := os.ReadFile(changed); string(b) != "after the turn" {
		t.Errorf("the changed file is %q, want what the turn left", b)
	}
	if b, _ := os.ReadFile(created); string(b) != "made by the turn" {
		t.Errorf("the created file is %q, want what the turn wrote", b)
	}
}

// A post-image whose blob has gone is reported rather than silently
// leaving the file where the rewind put it — the failure this whole
// feature would be judged on, from the other direction.
func TestARedoWithNoSavedCopySaysSo(t *testing.T) {
	loop, _, dir := checkpointLoop(t)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	marker := events.Event{Data: map[string]any{"redo": []map[string]any{
		{"path": filepath.Join(dir, "gone.txt"), "sha256": "0000000000000000000000000000000000000000000000000000000000000000"},
	}}}

	written, skipped := loop.replayPostImages(sid, marker)
	if len(written) != 0 {
		t.Errorf("it wrote %v with no saved copy", written)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want the one path named", skipped)
	}
	if got := skipped[0]; !filepath.IsAbs(got[:len(dir)]) {
		t.Errorf("the skip does not name the path: %q", got)
	}
}
