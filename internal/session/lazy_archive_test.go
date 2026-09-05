package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localcode/internal/events"
)

// A shelf that costs what the shelf being empty costs is not a shelf.
//
// Every session's log used to be read and parsed at startup, archived or
// not, and the archive is the part nobody is looking at. Measured on a
// home with five live conversations and a hundred archived ones of 3,000
// events each with two background tasks apiece: 2.9s and 1.8 GB reading
// everything, 0.18s and 109 MB with the shelf left on disk.
//
// The read is deferred, not skipped: everything that touches the log
// asks for it first, so nothing downstream knows the difference.

// writeSession puts a session on disk with n events in its log, archived
// or not, the way a previous run of the daemon would have left it.
func writeSession(t *testing.T, dir, id string, n int, archived bool) {
	t.Helper()
	meta := Session{ID: id, Visible: true, Agent: "general-purpose", Workspace: "/tmp/p", CreatedAt: time.Now().UTC()}
	if archived {
		at := time.Now().UTC()
		meta.ArchivedAt = &at
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".meta.json"), data, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	f, err := os.Create(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	defer f.Close()
	for i := 1; i <= n; i++ {
		line, _ := json.Marshal(events.Event{
			Seq: uint64(i), Session: id, Type: events.TypeUserMessage,
			Timestamp: time.Now().UTC(), Data: map[string]any{"text": "hello"},
		})
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatalf("write log: %v", err)
		}
	}
}

func TestAnArchivedLogIsNotReadUntilSomethingAsksForIt(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "s-live", 3, false)
	writeSession(t, dir, "s-shelf", 5, true)

	store, warnings, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}

	// Reaching into the store on purpose: the whole claim is about what
	// is in memory, and there is no honest way to ask that from outside.
	store.mu.Lock()
	live, shelf := store.sessions["s-live"], store.sessions["s-shelf"]
	liveLoaded, shelfLoaded := live.loaded, shelf.loaded
	liveLen, shelfLen := len(live.log), len(shelf.log)
	store.mu.Unlock()

	if !liveLoaded || liveLen != 3 {
		t.Errorf("the live conversation was not read: loaded=%v events=%d", liveLoaded, liveLen)
	}
	if shelfLoaded || shelfLen != 0 {
		t.Errorf("the archived conversation was read at startup: loaded=%v events=%d", shelfLoaded, shelfLen)
	}

	// And asking is all it takes.
	evs, err := store.Events("s-shelf", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 5 {
		t.Errorf("read %d events off the shelf, want 5", len(evs))
	}
}

// The sequence numbers live in the log, so appending to a session whose
// log has not been read would hand out numbers the file already holds —
// and two events with one seq breaks resume for that session for good.
func TestAppendingToAShelvedSessionContinuesItsSequence(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "s-shelf", 6, true)

	store, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	ev, err := store.Append("s-shelf", events.TypeUserMessage, map[string]any{"text": "after"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if ev.Seq != 7 {
		t.Errorf("seq = %d, want 7 — the deferred log was not read before the number was handed out", ev.Seq)
	}
	evs, err := store.Events("s-shelf", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 7 {
		t.Errorf("the session holds %d events, want 7", len(evs))
	}
}

// TailSince is what a client opening a conversation asks first, and it
// reads the log to find a turn boundary.
func TestTailSinceReadsAShelvedLog(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "s-shelf", 10, true)

	store, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	if _, err := store.TailSince("s-shelf", 3); err != nil {
		t.Fatalf("TailSince: %v", err)
	}
	store.mu.Lock()
	loaded := store.sessions["s-shelf"].loaded
	store.mu.Unlock()
	if !loaded {
		t.Error("TailSince answered without reading the log it needs")
	}
}

// Archiving a conversation on a running daemon gives its events back to
// the file they came from, so a daemon up for a week is not still
// holding what was put away on Monday. Retrieving reads them again.
func TestArchivingReturnsTheEventsToDisk(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.CreateSession("s-1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := 0; i < 4; i++ {
		if _, err := store.Append("s-1", events.TypeUserMessage, map[string]any{"text": "hi"}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if _, err := store.Archive("s-1"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	store.mu.Lock()
	held := len(store.sessions["s-1"].log)
	store.mu.Unlock()
	if held != 0 {
		t.Errorf("archiving left %d events in memory", held)
	}
	evs, err := store.Events("s-1", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 4 {
		t.Errorf("retrieved %d events, want 4 — archiving lost what it was supposed to shelve", len(evs))
	}
}

// A store with no directory has nowhere to read events back from, so
// archiving must not drop them there.
func TestArchivingAnUnpersistedSessionKeepsItsEvents(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.CreateSession("s-1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := store.Append("s-1", events.TypeUserMessage, map[string]any{"text": "hi"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := store.Archive("s-1"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	evs, err := store.Events("s-1", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 1 {
		t.Errorf("archiving an in-memory session lost its %d events", 1-len(evs))
	}
}

// A conversation is put away with everything that ran inside it.
//
// A background task's session is invisible and carries no archived flag —
// Archive refuses anything that is not a conversation — so gating the
// deferral on the flag alone left every task log being read at startup.
// On a busy conversation those are the larger half of the events, so the
// saving was only over the part of the shelf that had never run a task.
func TestTheWholeShelvedTreeStaysOnDisk(t *testing.T) {
	dir := t.TempDir()
	writeSessionUnder(t, dir, "s-shelf", "", 4, true)
	writeSessionUnder(t, dir, "s-shelf-task", "s-shelf", 9, false)
	writeSessionUnder(t, dir, "s-shelf-deep", "s-shelf-task", 7, false)
	writeSessionUnder(t, dir, "s-live", "", 3, false)
	writeSessionUnder(t, dir, "s-live-task", "s-live", 5, false)

	store, warnings, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}

	loaded := func(id string) bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.sessions[id].loaded
	}
	for _, id := range []string{"s-shelf", "s-shelf-task", "s-shelf-deep"} {
		if loaded(id) {
			t.Errorf("%s was read at startup — it is under a conversation on the shelf", id)
		}
	}
	for _, id := range []string{"s-live", "s-live-task"} {
		if !loaded(id) {
			t.Errorf("%s was not read at startup", id)
		}
	}

	// And the events are all still there when something asks.
	for id, want := range map[string]int{"s-shelf": 4, "s-shelf-task": 9, "s-shelf-deep": 7} {
		evs, err := store.Events(id, 0)
		if err != nil {
			t.Fatalf("Events %s: %v", id, err)
		}
		if len(evs) != want {
			t.Errorf("%s read back %d events, want %d", id, len(evs), want)
		}
	}
}

// A log that cannot be read is an error, not an empty conversation.
//
// It used to be swallowed: the session was presented as empty with its
// next sequence number at zero, and the next Append handed out seq 1
// into a file that already ended at six. Two events with one sequence
// number breaks `since=` replay and Last-Event-ID resume for that
// session permanently — the corruption parseLog's own comment describes.
func TestAnUnreadableShelvedLogRefusesRatherThanCorrupts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable file is still readable")
	}
	dir := t.TempDir()
	writeSession(t, dir, "s-shelf", 6, true)

	store, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	// Write-only: the append handle restoreOne holds still opens, and
	// the read does not.
	if err := os.Chmod(filepath.Join(dir, "s-shelf.jsonl"), 0o222); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "s-shelf.jsonl"), 0o644) })

	if _, err := store.Events("s-shelf", 0); err == nil {
		t.Error("an unreadable log read back as an empty conversation")
	}
	if _, err := store.Append("s-shelf", events.TypeUserMessage, map[string]any{"text": "after"}); err == nil {
		t.Error("appended to a session whose sequence number could not be read")
	}

	// And it is not latched: the failure was transient as far as the
	// store knows, so the next request tries again.
	if err := os.Chmod(filepath.Join(dir, "s-shelf.jsonl"), 0o644); err != nil {
		t.Fatalf("chmod back: %v", err)
	}
	ev, err := store.Append("s-shelf", events.TypeUserMessage, map[string]any{"text": "after"})
	if err != nil {
		t.Fatalf("Append after the log became readable: %v", err)
	}
	if ev.Seq != 7 {
		t.Errorf("seq = %d, want 7", ev.Seq)
	}
}

// writeSessionUnder is writeSession with a parent, for the tree cases.
func writeSessionUnder(t *testing.T, dir, id, parent string, n int, archived bool) {
	t.Helper()
	writeSession(t, dir, id, n, archived)
	if parent == "" {
		return
	}
	path := filepath.Join(dir, id+".meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta Session
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse meta: %v", err)
	}
	meta.ParentID = parent
	meta.Visible = false
	out, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
}

// Archiving on a running daemon gives the whole tree's events back to
// the files they came from, not only the conversation's own.
func TestArchivingReturnsTheTreeToDisk(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.CreateSession("s-1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := store.CreateSession("s-1-task", "s-1", "general-purpose", false); err != nil {
		t.Fatalf("CreateSession task: %v", err)
	}
	for _, id := range []string{"s-1", "s-1-task"} {
		for i := 0; i < 3; i++ {
			if _, err := store.Append(id, events.TypeUserMessage, map[string]any{"text": "hi"}); err != nil {
				t.Fatalf("Append %s: %v", id, err)
			}
		}
	}
	if _, err := store.Archive("s-1"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	for _, id := range []string{"s-1", "s-1-task"} {
		store.mu.Lock()
		held := len(store.sessions[id].log)
		store.mu.Unlock()
		if held != 0 {
			t.Errorf("archiving left %d events of %s in memory", held, id)
		}
		evs, err := store.Events(id, 0)
		if err != nil {
			t.Fatalf("Events %s: %v", id, err)
		}
		if len(evs) != 3 {
			t.Errorf("%s read back %d events, want 3", id, len(evs))
		}
	}
}
