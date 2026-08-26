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
	if got := w.Recent(recentSize*2, "", ""); len(got) != recentSize {
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

// Item 26. The turn log used to grow without bound: one file per day,
// nothing ever removed, on a daemon that can run for months. Retention is
// by the date in the file's own name, because mtime is whatever the
// filesystem or a backup tool made of it.
func TestOldTraceFilesArePrunedByAge(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "localcode-2020-01-01.jsonl")
	recent := filepath.Join(dir, "localcode-"+time.Now().Format("2006-01-02")+".jsonl")
	foreign := filepath.Join(dir, "notes.jsonl")
	for _, p := range []string{old, recent, foreign} {
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	w, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()
	// Open itself deletes nothing (R10N2); the first prune runs when the
	// effective bounds are installed, which the daemon does right after.
	if _, err := os.Stat(old); err != nil {
		t.Error("open pruned before any retention was configured")
	}
	w.SetRetention(0, 0)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("a file far past the retention age survived open")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("today's file was pruned")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("a file the writer does not own was pruned")
	}
}

// The size cap removes oldest first and never touches today's file, which
// is the one being written.
func TestTraceFilesArePrunedOldestFirstUnderTheSizeCap(t *testing.T) {
	dir := t.TempDir()
	day := func(daysAgo int) string {
		return filepath.Join(dir, "localcode-"+time.Now().AddDate(0, 0, -daysAgo).Format("2006-01-02")+".jsonl")
	}
	big := make([]byte, 2*1024*1024)
	for _, p := range []string{day(2), day(1), day(0)} {
		if err := os.WriteFile(p, big, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	w, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()
	w.SetRetention(30, 4) // three 2MB files against a 4MB cap

	if _, err := os.Stat(day(2)); !os.IsNotExist(err) {
		t.Error("the oldest file survived a size cap it does not fit under")
	}
	if _, err := os.Stat(day(1)); err != nil {
		t.Error("a file that fits under the cap was pruned")
	}
	if _, err := os.Stat(day(0)); err != nil {
		t.Error("today's file was pruned by the size cap")
	}
}

// A nil writer and a zero configuration stay safe: the default age
// applies rather than "keep forever", and nil is a no-op like every
// other method.
func TestRetentionDefaultsAndNilSafety(t *testing.T) {
	var w *Writer
	w.SetRetention(0, 0) // must not panic

	dir := t.TempDir()
	old := filepath.Join(dir, "localcode-2020-01-01.jsonl")
	if err := os.WriteFile(old, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	real, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer real.Close()
	real.SetRetention(0, 0)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("SetRetention(0, 0) kept a file forever; zero must mean the default age, not off")
	}
}

// R10N2. The first prune used to run inside Open under the default 30
// days, before the daemon had any chance to install the configured
// bounds — so trace_max_age_days: 90 could not protect a 40-day file
// from a deletion that had already happened. Open deletes nothing now;
// the configured retention is what prunes.
func TestConfiguredRetentionIsAppliedBeforeAnythingIsDeleted(t *testing.T) {
	dir := t.TempDir()
	day := func(daysAgo int) string {
		return filepath.Join(dir, "localcode-"+time.Now().AddDate(0, 0, -daysAgo).Format("2006-01-02")+".jsonl")
	}
	for _, p := range []string{day(40), day(100)} {
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	w, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()
	w.SetRetention(90, 0)

	if _, err := os.Stat(day(40)); err != nil {
		t.Error("a 40-day trace was deleted under a configured 90-day retention")
	}
	if _, err := os.Stat(day(100)); !os.IsNotExist(err) {
		t.Error("a 100-day trace survived a 90-day retention")
	}

	// And a record written before any SetRetention still prunes at
	// rotation under the default, so a caller that never configures
	// anything is not "keep forever" either.
	w2dir := t.TempDir()
	oldFile := filepath.Join(w2dir, "localcode-2020-01-01.jsonl")
	if err := os.WriteFile(oldFile, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	w2, err := Open(w2dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w2.Close()
	w2.Write(Record{TraceID: "t", Span: SpanTurnStart})
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("an unconfigured writer never pruned: the default stopped applying")
	}
}
