package memory

import (
	"encoding/json"
	"path/filepath"
	"time"
)

const usageFileName = ".usage.json"

// usage is what the directory's notes have been used for, kept beside
// them rather than in them. A note file changes when what it says
// changes, and nothing else: a counter written into the note would
// rewrite it every time it was read, and the index generated from the
// notes with it.
type usage struct {
	Version int `json:"version"`
	// Sessions counts conversations that loaded this directory's index.
	// It is the clock retirement runs on, next to the calendar: a project
	// nobody opened for six months has not been ignoring its notes.
	Sessions int `json:"sessions"`
	// Counted are the most recent conversations already counted, so a
	// conversation that loads the index again (after a restart, after
	// compaction) is not counted twice.
	Counted []string            `json:"counted,omitempty"`
	Entries map[string]*useMark `json:"entries"`
	// Retired is the most recent retirements, newest last, for /memory to
	// report: a note that left the index on its own should not leave
	// silently.
	Retired []retirement `json:"retired,omitempty"`
}

type useMark struct {
	Uses        int       `json:"uses"`
	LastUsed    time.Time `json:"last_used"`
	LastSession int       `json:"last_session"`
}

type retirement struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

const (
	maxCounted = 200
	maxRetired = 20
)

func usagePath(dir string) string { return filepath.Join(dir, usageFileName) }

// readUsage reads the record, or an empty one. A record that cannot be
// parsed is treated as empty: the cost is that every note starts its
// clock again, which retires nothing early.
func readUsage(dir string) *usage {
	u := &usage{Version: 1, Entries: map[string]*useMark{}}
	data, cut, err := readRegular(usagePath(dir), 1<<20)
	if err != nil || cut {
		return u
	}
	var got usage
	if json.Unmarshal([]byte(data), &got) != nil {
		return u
	}
	if got.Entries == nil {
		got.Entries = map[string]*useMark{}
	}
	got.Version = 1
	return &got
}

func (u *usage) write(dir string) error {
	data, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(usagePath(dir), append(data, '\n'))
}

// mark is file's record, created at the current moment if it has none.
// A note with no record is one written before records existed, or by
// hand: its clock starts now, so an update never retires a note the
// moment it is installed.
func (u *usage) mark(file string, now time.Time) *useMark {
	m := u.Entries[file]
	if m == nil {
		m = &useMark{LastUsed: now, LastSession: u.Sessions}
		u.Entries[file] = m
	}
	return m
}

func (u *usage) use(file string, now time.Time) {
	m := u.mark(file, now)
	m.Uses++
	m.LastUsed = now
	m.LastSession = u.Sessions
}

// countSession counts id once.
func (u *usage) countSession(id string) bool {
	for _, c := range u.Counted {
		if c == id {
			return false
		}
	}
	u.Sessions++
	u.Counted = append(u.Counted, id)
	if len(u.Counted) > maxCounted {
		u.Counted = u.Counted[len(u.Counted)-maxCounted:]
	}
	return true
}

func (u *usage) noteRetired(name string, at time.Time) {
	u.Retired = append(u.Retired, retirement{Name: name, At: at})
	if len(u.Retired) > maxRetired {
		u.Retired = u.Retired[len(u.Retired)-maxRetired:]
	}
}
