package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// A manifest that only exists during the call it describes answers the
// wrong question.
//
// The trace carries a manifest ID, the selected asset IDs, and which of
// them were untrusted. That is enough to notice something, and not
// enough to investigate it: the hashes, versions, trust classes,
// placements, activation reasons, exclusions, warnings and adapter
// lowering are what turn "this request was different" into "this request
// was different because the project's rules changed and the fallback
// re-derived the model note". All of it used to be discarded the moment
// the call returned.
//
// So manifests are written down, keyed by the ID the trace already
// records. Redacted by construction rather than by policy: a Manifest
// never held a body in the first place, which is why this can be
// persisted at all.

// Store keeps recent assembly manifests on disk, one file per day like
// the trace it sits beside, and in memory for the ones a diagnostic is
// most likely to want.
//
// Every method is safe on a nil receiver, so a Loop without one simply
// records nothing rather than guarding every call site.
type Store struct {
	dir string

	maxAgeDays   int
	maxTotalSize int64

	mu     sync.Mutex
	recent map[string]Manifest
	order  []string
	day    string
	// written is the set of ids already on the current day's file, so
	// the same assembly recorded on every turn of a long session costs
	// one line rather than one line per turn.
	//
	// Loaded from the file rather than started empty, because a set
	// that only knows what this process wrote is not deduplication, it
	// is deduplication until the next restart. An id is added only
	// after its line is actually on disk, so a write that failed is
	// retried by the next call rather than suppressed by the record of
	// an attempt.
	written map[string]bool
}

// recentManifests is how many are kept in memory. A long turn with a
// fallback and a compaction produces a handful; this covers a session's
// worth of investigation without growing without bound.
const recentManifests = 200

// DefaultMaxAgeDays matches the trace's own default, because the two are
// read together and a manifest whose trace line has been pruned is not
// worth keeping.
const DefaultMaxAgeDays = 30

// OpenStore prepares a store over dir, creating it if needed. Like the
// trace writer, it deletes nothing here: pruning runs when the effective
// retention is installed, so a longer configured retention cannot lose
// files to a default that was applied first.
func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir, maxAgeDays: DefaultMaxAgeDays, recent: map[string]Manifest{}, written: map[string]bool{}}, nil
}

// SetRetention bounds the directory by age in days and, when
// maxTotalMB is nonzero, by total size, taking both from the same
// configuration the trace does.
//
// Both, rather than age alone. Age alone was the first version and it
// was wrong in the way an unbounded log is always wrong: a busy day
// stays inside the age window while it fills the disk, and the trace
// beside it has had a size bound since v0.55.0 for exactly that reason.
// A manifest whose trace line has been pruned is not worth keeping, so
// the two are configured together and prune the same way.
//
// Zero or below restores the default age rather than keeping forever.
// Today's file is never removed by either bound: it is the one being
// written, and deleting it would lose the records this process is still
// producing.
func (s *Store) SetRetention(maxAgeDays, maxTotalMB int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxTotalSize = int64(maxTotalMB) * 1024 * 1024
	if maxAgeDays <= 0 {
		maxAgeDays = DefaultMaxAgeDays
	}
	s.maxAgeDays = maxAgeDays
	s.pruneLocked(time.Now())
}

// Put records a manifest. Failures are dropped for the same reason the
// trace drops its own: a diagnostic that cannot be written must never be
// the reason a turn fails.
func (s *Store) Put(m Manifest) {
	if s == nil || m.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, dup := s.recent[m.ID]; !dup {
		s.recent[m.ID] = m
		s.order = append(s.order, m.ID)
		for len(s.order) > recentManifests {
			delete(s.recent, s.order[0])
			s.order = s.order[1:]
		}
	}

	day := m.At.Format("2006-01-02")
	if m.At.IsZero() {
		day = time.Now().Format("2006-01-02")
	}
	if s.day != day {
		if s.day != "" {
			s.pruneLocked(time.Now())
		}
		// The ids already on this day's file, including the ones a
		// previous process wrote. Read once per day rather than per
		// call, and never at Open, so starting a daemon does not wait
		// on a file it may not write to at all.
		s.written = s.idsOnFile(day)
		s.day = day
	}

	// A manifest is immutable content addressed by its own ID: the same
	// assembly recorded twice is the same record twice. Writing it once
	// is what keeps a session that sends the identical prompt on every
	// turn from paying a full record per turn, and it is what makes the
	// memory and file lookups able to agree, since both then hold the
	// first occurrence. When a call happened is a question for the
	// trace, which records one line per call carrying this ID.
	if s.written[m.ID] {
		return
	}

	line, err := json.Marshal(m)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "manifests-"+day+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	line = append(line, '\n')
	n, werr := f.Write(line)
	if werr == nil && n < len(line) {
		// A fragment with no newline on the end would be glued to the
		// next record appended, costing two records instead of one. The
		// terminator is best effort like everything else here: if it
		// cannot be written, the reader drops one unparseable line,
		// which is the same outcome as before and no worse.
		f.Write([]byte("\n"))
	}
	cerr := f.Close()
	// Marked only once the line is on disk, whole. A short write leaves
	// a truncated record behind, which the reader skips as unparseable
	// JSON, and the next Put appends a complete one; marking before the
	// write meant a full disk suppressed every later attempt for the
	// life of the process.
	if werr == nil && cerr == nil && n == len(line) {
		s.written[m.ID] = true
	}
}

// idsOnFile reads back the ids already recorded for a day.
//
// A day's file is bounded by the same size retention the directory is,
// and it is read once when the day changes rather than per call, so the
// cost is one sequential pass over at most that bound. Anything that
// will not parse is skipped: a truncated line from a failed write is
// not an id, and treating it as one would suppress the record that
// replaces it.
func (s *Store) idsOnFile(day string) map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(s.dir, "manifests-"+day+".jsonl"))
	if err != nil {
		return out
	}
	for _, line := range splitLines(data) {
		var m Manifest
		if json.Unmarshal(line, &m) == nil && m.ID != "" {
			out[m.ID] = true
		}
	}
	return out
}

// Get returns the manifest recorded under id: from memory when it is
// recent, and from the files otherwise, which is what makes a trace line
// from last week still answerable.
func (s *Store) Get(id string) (Manifest, bool) {
	if s == nil || id == "" {
		return Manifest{}, false
	}
	s.mu.Lock()
	m, ok := s.recent[id]
	s.mu.Unlock()
	if ok {
		return m, true
	}
	return s.fromFiles(id)
}

func (s *Store) fromFiles(id string) (Manifest, bool) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return Manifest{}, false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	// Oldest first, and the first match wins, which is the same answer
	// the memory ring gives: it keeps the first manifest recorded under
	// an id and Put no longer appends a second. The two lookups used to
	// disagree, memory holding the first record and this holding the
	// last, so "/context <id>" could change its answer across a restart
	// while describing an assembly that had not changed at all.
	sort.Strings(names)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		for _, line := range splitLines(data) {
			var m Manifest
			if json.Unmarshal(line, &m) == nil && m.ID == id {
				return m, true
			}
		}
	}
	return Manifest{}, false
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// pruneLocked removes manifest files past the retention age, read from
// the date in the name rather than from mtime, exactly as the trace does
// and for the same reason: the name is what this wrote, and mtime is
// whatever the filesystem made of it.
func (s *Store) pruneLocked(now time.Time) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := now.AddDate(0, 0, -s.maxAgeDays)
	today := now.Format("2006-01-02")

	// The files this store owns, oldest first. The date-named form
	// sorts chronologically as text, so the names are the order.
	type manifestFile struct {
		name string
		day  string
		size int64
	}
	var kept []manifestFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || len(name) < len("manifests-2006-01-02.jsonl") || name[:len("manifests-")] != "manifests-" {
			continue
		}
		day := name[len("manifests-") : len("manifests-")+len("2006-01-02")]
		at, perr := time.Parse("2006-01-02", day)
		if perr != nil {
			continue
		}
		if day != today && at.Before(cutoff) {
			os.Remove(filepath.Join(s.dir, name))
			continue
		}
		var size int64
		if info, ierr := e.Info(); ierr == nil {
			size = info.Size()
		}
		kept = append(kept, manifestFile{name: name, day: day, size: size})
	}

	if s.maxTotalSize <= 0 {
		return
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].day < kept[j].day })
	var total int64
	for _, f := range kept {
		total += f.size
	}
	// Oldest first until the directory fits, and never today's file,
	// which is the one being written. Same rule as the trace's own size
	// prune, because the two are read together and a bound that removed
	// different days from each would make them unreadable as a pair.
	for _, f := range kept {
		if total <= s.maxTotalSize || f.day == today {
			break
		}
		os.Remove(filepath.Join(s.dir, f.name))
		total -= f.size
	}
}
