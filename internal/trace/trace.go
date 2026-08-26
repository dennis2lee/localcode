// Package trace writes the structured record of what an agent turn
// actually did: which model answered, what it cost, which tools ran and
// for how long, what was delegated to whom, and what had to be recovered
// from.
//
// It exists because a multi-agent turn cannot be debugged from a
// transcript. The transcript shows what the main conversation said; it
// does not show that the answer came from the second model in a fallback
// chain, that four fifths of the input was served from cache, that a
// sub-agent spent ninety seconds in one grep, or that the turn compacted
// twice on the way. Those are the questions asked when a session is slow,
// expensive, or wrong, and every one of them is a fact nobody was writing
// down.
//
// The unit that makes it useful is the trace id. One turn at the top
// produces one id, and every child session it spawns inherits it, so a
// delegation that fanned out to three specialists is one grep away from
// being a single readable story rather than four unrelated logs.
package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Span names. One per thing worth knowing separately about a turn.
const (
	SpanTurnStart = "turn.start"
	SpanTurnEnd   = "turn.end"
	// SpanModel is one provider call: the request that went out and what
	// came back. A turn with three tool rounds has three of these.
	SpanModel = "model"
	// SpanTool is one tool execution, with how long it took. In a
	// multi-agent turn this is where the time usually is.
	SpanTool = "tool"
	// SpanDelegate is a sub-agent being started, naming the child session
	// so the two halves can be joined.
	SpanDelegate = "delegate"
	// SpanFallback is the turn moving down the fallback chain.
	SpanFallback = "fallback"
	// SpanRetry is the turn asking the same endpoint again after a
	// transient failure, before any fallback is considered.
	SpanRetry = "retry"
	// SpanCompact is the history being summarized or trimmed.
	SpanCompact = "compact"
)

// Record is one line of the log.
//
// Flat and omitempty throughout, because the consumer is `jq` and a nested
// shape makes every query longer for no gain. The fields are the ones a
// multi-agent harness has to correlate: who, on what, at what cost, how
// long, and under which trace.
type Record struct {
	Time    time.Time `json:"time"`
	TraceID string    `json:"trace_id"`
	Span    string    `json:"span"`

	SessionID string `json:"session_id,omitempty"`
	// ParentSessionID is set on a sub-agent's records. With TraceID it is
	// the whole of the multi-agent correlation: one identifies the turn,
	// the other reconstructs the tree inside it.
	ParentSessionID string `json:"parent_session_id,omitempty"`

	Agent    string `json:"agent,omitempty"`
	Profile  string `json:"profile,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`

	InputTokens      int `json:"input_tokens,omitempty"`
	OutputTokens     int `json:"output_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`

	Tool         string `json:"tool,omitempty"`
	DurationMS   int64  `json:"duration_ms,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`

	// Fallbacks, Retries and Compactions are counts for the turn so far,
	// so a turn.end record answers "did this turn have a bad time?"
	// without reading the lines before it.
	Fallbacks   int `json:"fallbacks,omitempty"`
	Retries     int `json:"retries,omitempty"`
	Compactions int `json:"compactions,omitempty"`

	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// recentSize is how many records are kept in memory for reading back over
// the API. Enough for a long turn and its sub-agents; the file is the
// record that lasts.
const recentSize = 500

// Writer appends records to a day-rotated JSONL file and keeps the most
// recent ones in memory.
//
// Every method is safe on a nil receiver, so the loop can hold a nil
// *Writer and call it unconditionally rather than guarding every call
// site. Tracing off is a nil Writer.
type Writer struct {
	dir string

	// Retention. Age is in days and bounds how long a day's file is
	// kept; size is in bytes and bounds the directory as a whole. Zero
	// size means no size cap. See prune.
	maxAgeDays   int
	maxTotalSize int64

	mu     sync.Mutex
	day    string
	file   *os.File
	recent []Record
	next   int
	filled bool
}

// DefaultMaxAgeDays is how long a day's trace file is kept when nothing
// says otherwise. A month covers "why was last week slow" and every
// debugging conversation these files have actually been used in, and it
// bounds a daemon left running for a year at roughly thirty files.
const DefaultMaxAgeDays = 30

// Open prepares a writer over dir, creating it if needed. The file itself
// is opened lazily, on the first record, so turning tracing on in a
// session that then writes nothing leaves nothing behind.
//
// Open deletes nothing. Pruning is destructive and irreversible, so it
// runs only after the effective bounds are known: at SetRetention, which
// the daemon calls with the configured values right after Open, and at
// each day rotation. Pruning here under the default would delete files a
// longer configured retention was about to protect, before that
// configuration had any chance to say so.
func Open(dir string) (*Writer, error) {
	if dir == "" {
		return nil, fmt.Errorf("trace: no directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("trace: create %s: %w", dir, err)
	}
	return &Writer{dir: dir, recent: make([]Record, recentSize), maxAgeDays: DefaultMaxAgeDays}, nil
}

// SetRetention bounds the trace directory: files older than maxAgeDays
// are removed, and when maxTotalMB is nonzero the oldest files go until
// the directory fits under it. maxAgeDays <= 0 restores the default age
// rather than keeping forever, because "forever" is the bug this exists
// to fix; a caller that truly wants that can not call Open.
func (w *Writer) SetRetention(maxAgeDays, maxTotalMB int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if maxAgeDays <= 0 {
		maxAgeDays = DefaultMaxAgeDays
	}
	w.maxAgeDays = maxAgeDays
	w.maxTotalSize = int64(maxTotalMB) * 1024 * 1024
	w.pruneLocked(time.Now())
}

// Write appends one record. Failures are dropped rather than returned: a
// log that cannot be written must not be the reason a turn fails, and the
// caller has nothing useful to do about a full disk mid-answer.
func (w *Writer) Write(r Record) {
	if w == nil {
		return
	}
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	w.recent[w.next] = r
	w.next = (w.next + 1) % len(w.recent)
	if w.next == 0 {
		w.filled = true
	}

	if err := w.rotate(r.Time); err != nil || w.file == nil {
		return
	}
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	w.file.Write(append(line, '\n'))
}

// rotate opens the file for the record's day, closing yesterday's. Called
// under the lock.
func (w *Writer) rotate(at time.Time) error {
	day := at.Format("2006-01-02")
	if w.file != nil && w.day == day {
		return nil
	}
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
	f, err := os.OpenFile(filepath.Join(w.dir, "localcode-"+day+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.file, w.day = f, day
	// The day changed, so yesterday's file just became prunable history.
	// Once per day is the right cadence: retention is measured in days,
	// and checking per record would be all cost and no new answer.
	w.pruneLocked(at)
	return nil
}

// pruneLocked removes trace files past the retention bounds; called
// under the lock. Age goes first, read from the date in the file's own
// name rather than from mtime, because the name is what rotation wrote
// and mtime is whatever the filesystem or a backup tool made of it.
// Then, if a size cap is set, the oldest survivors go until the
// directory fits. Today's file is never removed: it is the one being
// written.
//
// Failures are ignored file by file, for the same reason Write drops
// its own: retention must never be the reason a turn fails, and a file
// that cannot be removed today will be older tomorrow.
func (w *Writer) pruneLocked(now time.Time) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	today := now.Format("2006-01-02")
	cutoff := now.AddDate(0, 0, -w.maxAgeDays).Format("2006-01-02")

	// The files this writer owns, oldest first. The date-named form
	// sorts chronologically as text, so the names are the order.
	type traceFile struct {
		name string
		day  string
		size int64
	}
	var files []traceFile
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "localcode-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, "localcode-"), ".jsonl")
		if _, perr := time.Parse("2006-01-02", day); perr != nil {
			// Not a rotation-named file. Somebody else's; leave it.
			continue
		}
		info, ierr := e.Info()
		var size int64
		if ierr == nil {
			size = info.Size()
		}
		files = append(files, traceFile{name: name, day: day, size: size})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].day < files[j].day })

	var kept []traceFile
	for _, f := range files {
		if f.day < cutoff && f.day != today {
			os.Remove(filepath.Join(w.dir, f.name))
			continue
		}
		kept = append(kept, f)
	}

	if w.maxTotalSize <= 0 {
		return
	}
	var total int64
	for _, f := range kept {
		total += f.size
	}
	for _, f := range kept {
		if total <= w.maxTotalSize || f.day == today {
			break
		}
		os.Remove(filepath.Join(w.dir, f.name))
		total -= f.size
	}
}

// Recent returns up to n of the most recent records, oldest first,
// optionally limited to one session or one trace.
func (w *Writer) Recent(n int, sessionID, traceID string) []Record {
	if w == nil || n <= 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	// Walk the ring oldest to newest so the answer reads in the order
	// things happened, which is the only order a log is useful in.
	var all []Record
	size := len(w.recent)
	start, count := 0, w.next
	if w.filled {
		start, count = w.next, size
	}
	for i := 0; i < count; i++ {
		r := w.recent[(start+i)%size]
		if sessionID != "" && r.SessionID != sessionID {
			continue
		}
		if traceID != "" && r.TraceID != traceID {
			continue
		}
		all = append(all, r)
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// Close releases today's file.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
