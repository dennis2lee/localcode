package trace

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecordsLandInADayNamedFile(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	w.Write(Record{TraceID: "abc", Span: SpanModel, SessionID: "s1", Model: "m", InputTokens: 10})
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	name := filepath.Join(dir, "localcode-"+time.Now().Format("2006-01-02")+".jsonl")
	f, err := os.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatal("the file is empty")
	}
	var got Record
	if err := json.Unmarshal(sc.Bytes(), &got); err != nil {
		t.Fatalf("the line is not JSON: %v", err)
	}
	if got.TraceID != "abc" || got.Model != "m" || got.InputTokens != 10 {
		t.Errorf("read back %+v", got)
	}
	if got.Time.IsZero() {
		t.Error("no timestamp was stamped on the record")
	}
}

// Nothing until there is something to write. A daemon that runs all day
// with tracing off should leave no file behind.
func TestNoFileIsCreatedUntilTheFirstRecord(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the directory already has %d files", len(entries))
	}
}

// The log is only useful in the order things happened.
func TestRecentReadsOldestFirst(t *testing.T) {
	w, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	for _, span := range []string{"one", "two", "three"} {
		w.Write(Record{TraceID: "t", Span: span, SessionID: "s1"})
	}
	got := w.Recent(10, "", "")
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3", len(got))
	}
	if got[0].Span != "one" || got[2].Span != "three" {
		t.Errorf("order was %s, %s, %s", got[0].Span, got[1].Span, got[2].Span)
	}
}

// The trace id is the whole reason this is a log and not a pile: one turn
// that fanned out to three sub-agents has to come back as one story.
func TestRecentFiltersBySessionAndTrace(t *testing.T) {
	w, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	w.Write(Record{TraceID: "t1", Span: SpanTurnStart, SessionID: "main"})
	w.Write(Record{TraceID: "t1", Span: SpanDelegate, SessionID: "main"})
	w.Write(Record{TraceID: "t1", Span: SpanModel, SessionID: "task-1", ParentSessionID: "main"})
	w.Write(Record{TraceID: "t2", Span: SpanTurnStart, SessionID: "other"})

	if got := w.Recent(10, "", "t1"); len(got) != 3 {
		t.Errorf("the trace has %d records, want the parent's two and the child's one", len(got))
	}
	if got := w.Recent(10, "task-1", ""); len(got) != 1 {
		t.Errorf("the child session has %d records, want 1", len(got))
	}
	if got := w.Recent(10, "main", "t2"); len(got) != 0 {
		t.Errorf("a session and trace that do not go together returned %d records", len(got))
	}
}

// The ring is bounded, so a long-running daemon does not grow a memory
// leak out of its own log.
func TestRecentIsBounded(t *testing.T) {
	w, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	for i := 0; i < recentSize*2; i++ {
		w.Write(Record{TraceID: "t", Span: SpanTool, SessionID: "s"})
	}
	if got := w.Recent(recentSize * 2, "", ""); len(got) != recentSize {
		t.Errorf("kept %d records in memory, want the ring's %d", len(got), recentSize)
	}
}

// Tracing off is a nil writer, and every call has to survive that: the
// alternative is a nil check at every call site in the agent loop, which
// is where one gets forgotten.
func TestANilWriterIsSafe(t *testing.T) {
	var w *Writer
	w.Write(Record{Span: SpanModel})
	if got := w.Recent(10, "", ""); got != nil {
		t.Errorf("a nil writer returned %v", got)
	}
	if err := w.Close(); err != nil {
		t.Errorf("closing a nil writer: %v", err)
	}
}

// Sub-agents run concurrently and write into the same file.
func TestConcurrentWritersDoNotRace(t *testing.T) {
	w, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				w.Write(Record{TraceID: "t", Span: SpanTool, SessionID: "s"})
			}
		}()
	}
	wg.Wait()
	if got := w.Recent(recentSize, "", ""); len(got) != 400 {
		t.Errorf("kept %d records, want 400", len(got))
	}
}

func TestTheTraceIDTravelsOnTheContext(t *testing.T) {
	ctx := context.Background()
	if ID(ctx) != "" {
		t.Error("a bare context reported a trace id")
	}
	ctx = WithID(ctx, "abc123")
	if ID(ctx) != "abc123" {
		t.Errorf("got %q", ID(ctx))
	}
	// An empty id is a no-op rather than an id of "", so a caller with
	// nothing to say does not have to special-case it.
	if ID(WithID(ctx, "")) != "abc123" {
		t.Error("an empty id overwrote the one already there")
	}
}

func TestNewIDsAreDistinctAndReadable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewID()
		if len(id) != 16 || strings.ContainsAny(id, " \t\n") {
			t.Fatalf("id %q is not something to paste into a grep", id)
		}
		if seen[id] {
			t.Fatalf("id %q came up twice", id)
		}
		seen[id] = true
	}
}
