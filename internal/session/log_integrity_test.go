package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"testing"

	"localcode/internal/events"
)

// Nothing truncates tool output, so one `cat` of a large file puts a line
// in the log longer than any fixed cap. Read with a Scanner, that stopped
// the restore dead — and reported nothing, since the end of a file and a
// line too long to hold look identical.
//
// The damage was not the missing tail. nextSeq came back below the highest
// seq already in the file, so appends after the restart handed out numbers
// the file already contained, and two events with one seq breaks `since=`
// replay and Last-Event-ID resume for that session for good.
func TestRestoreSurvivesAnOversizedLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s1.meta.json"), []byte(`{"id":"s1","visible":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 1; i <= 4; i++ {
		text := "small"
		if i == 2 {
			text = strings.Repeat("x", 2<<20)
		}
		line, err := json.Marshal(events.Event{
			Seq: uint64(i), Session: "s1", Type: events.TypeMessagePartDelta,
			Data: map[string]any{"text": text},
		})
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	store, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 4 {
		t.Fatalf("restored %d events, want 4 — the log was truncated at the big line", len(evs))
	}

	ev, err := store.Append("s1", events.TypeUserMessage, map[string]any{"text": "after the restart"})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Seq != 5 {
		t.Errorf("next seq = %d, want 5; %d is already in the file", ev.Seq, ev.Seq)
	}
}

// A partially written final line is what an interrupted write leaves. It
// is right to lose that line and wrong to lose the whole log with it.
func TestRestoreKeepsEverythingBeforeATornLastLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s1.meta.json"), []byte(`{"id":"s1","visible":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	good, err := json.Marshal(events.Event{Seq: 1, Session: "s1", Type: events.TypeUserMessage, Data: map[string]any{"text": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), append(append(good, '\n'), []byte(`{"seq":2,"typ`)...), 0o644); err != nil {
		t.Fatal(err)
	}

	store, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("restored %d events, want the one complete line", len(evs))
	}
}

// Two appends can take seq N and N+1 and then reach the file in the other
// order — a background task writing task.status to its parent while that
// parent's turn streams deltas is enough. Restoring reads file order, so
// the log came back unsorted and every seq-based lookup after it was
// working against an unsorted list.
func TestConcurrentAppendsReachTheFileInSeqOrder(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	const writers = 200
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Append("s1", events.TypeMessagePartDelta, map[string]any{"i": i})
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(dir, "s1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != writers {
		t.Fatalf("wrote %d lines, want %d", len(lines), writers)
	}
	var prev uint64
	for n, line := range lines {
		var ev events.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d does not parse (interleaved write?): %v", n, err)
		}
		if ev.Seq <= prev {
			t.Fatalf("line %d has seq %d after seq %d; the file is out of order", n, ev.Seq, prev)
		}
		prev = ev.Seq
	}
}

// Deleting a session left everyone reading it on a stream that would
// never produce another event and never end — they sat there until the
// HTTP client itself gave up.
func TestDeleteEndsLiveSubscriptions(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSession("s1", "", "a", true); err != nil {
		t.Fatal(err)
	}
	_, lost, _, err := store.Subscribe("s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("s1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lost:
	case <-time.After(2 * time.Second):
		t.Fatal("a reader of the deleted session was never told")
	}
}
