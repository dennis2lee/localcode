package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"
)

// A session's meta file and the group list are replaced, never written
// into.
//
// They were written with os.WriteFile, which truncates the file and then
// writes it. A crash, a kill or a power loss between the two left a
// zero-length <id>.meta.json, and LoadAllFromDisk drops a session whose
// meta it cannot parse: the conversation's log stays on disk and the
// session is gone from the list.

// atomicWriters is each writer in the package that rewrites a file whole,
// with the file it writes and a way to write a given version of it.
var atomicWriters = []struct {
	name  string
	file  string
	write func(dir, version string) error
}{
	{
		name:  "session meta",
		file:  "s1.meta.json",
		write: func(dir, v string) error { return writeSessionMeta(dir, Session{ID: "s1", Title: v}) },
	},
	{
		name:  "group list",
		file:  "groups.json",
		write: func(dir, v string) error { return writeGroupsFile(dir, []string{v}) },
	},
}

// dirNames is every name in dir, sorted.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// dirFiles is every regular file in dir by name, with its bytes.
func dirFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	files := map[string]string{}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		files[e.Name()] = string(data)
	}
	return files
}

// A write puts a new file in place of the old one. A hard link made to the
// old file before the write still reads the old bytes afterwards, which a
// write into the file would have truncated and overwritten. That is the
// property a crash depends on: at every moment the name is the old file,
// whole, or the new one. The write leaves no temporary file behind, and
// the new file has the mode every file in the directory has.
func TestAWriteReplacesTheFileAndLeavesNoTempFile(t *testing.T) {
	for _, w := range atomicWriters {
		t.Run(w.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, w.file)
			if err := w.write(dir, "version one"); err != nil {
				t.Fatalf("first write: %v", err)
			}
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			previous := filepath.Join(dir, "previous")
			if err := os.Link(path, previous); err != nil {
				t.Fatalf("link the first version: %v", err)
			}
			if runtime.GOOS != "windows" {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			if err := w.write(dir, "version two"); err != nil {
				t.Fatalf("second write: %v", err)
			}

			second, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(second, []byte("version two")) {
				t.Errorf("%s after the second write = %s, want the second version", w.file, second)
			}
			if old, err := os.ReadFile(previous); err != nil {
				t.Fatal(err)
			} else if !bytes.Equal(old, first) {
				t.Errorf("the first version's file was written into: it reads %s, want %s byte for byte", old, first)
			}
			want := []string{w.file, "previous"}
			sort.Strings(want)
			if got := dirNames(t, dir); !reflect.DeepEqual(got, want) {
				t.Errorf("directory after the write = %v, want %v and no temporary file", got, want)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if perm := info.Mode().Perm(); perm != logFileMode {
					t.Errorf("%s is %04o after the write, want %04o", w.file, perm, logFileMode)
				}
			}
		})
	}
}

// A write that fails leaves what was there and no temporary file.
//
// The rename is made to fail by putting a directory with a file in it
// where the file goes: a file cannot be renamed over a directory on any
// platform.
func TestAWriteThatFailsLeavesWhatWasThereAndNoTempFile(t *testing.T) {
	for _, w := range atomicWriters {
		t.Run(w.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, w.file)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			inside := filepath.Join(path, "inside")
			want := []byte("what was there before the write")
			if err := os.WriteFile(inside, want, 0o600); err != nil {
				t.Fatal(err)
			}

			if err := w.write(dir, "version two"); err == nil {
				t.Fatal("the write over a directory succeeded, want it to fail")
			}

			if got, err := os.ReadFile(inside); err != nil {
				t.Fatalf("what was there is gone: %v", err)
			} else if !bytes.Equal(got, want) {
				t.Errorf("what was there reads %q, want %q", got, want)
			}
			if got, want := dirNames(t, dir), []string{w.file}; !reflect.DeepEqual(got, want) {
				t.Errorf("directory after the failed write = %v, want %v and no temporary file", got, want)
			}
		})
	}
}

// A reorder writes only the sessions whose place changes.
//
// Every write is a flush to the disk, and SetOrder holds the store's lock
// while it writes. Rewriting every visible session on each drag cost about
// 4 ms a session on macOS, with every other session's events waiting.
// Moving one card past its neighbour changes two places and writes two
// files. A reorder that changes nothing writes none. The files are still
// the record: a restart reads the order the store has.
func TestAReorderWritesOnlyTheSessionsWhosePlaceChanges(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)
	ids := []string{"s1", "s2", "s3", "s4", "s5"}
	for _, id := range ids {
		if _, err := s.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetOrder(ids); err != nil {
		t.Fatalf("baseline SetOrder: %v", err)
	}

	var wrote []string
	s.writeMeta = func(d string, m Session) error {
		wrote = append(wrote, m.ID)
		return writeSessionMeta(d, m)
	}
	moved := []string{"s1", "s3", "s2", "s4", "s5"}
	if err := s.SetOrder(moved); err != nil {
		t.Fatalf("SetOrder: %v", err)
	}
	sort.Strings(wrote)
	if want := []string{"s2", "s3"}; !reflect.DeepEqual(wrote, want) {
		t.Errorf("moving s3 one place up wrote %v, want %v: the two sessions whose place changed", wrote, want)
	}

	wrote = nil
	if err := s.SetOrder(moved); err != nil {
		t.Fatalf("SetOrder again: %v", err)
	}
	if len(wrote) != 0 {
		t.Errorf("a reorder that changes nothing wrote %v, want no file", wrote)
	}

	restored, warnings, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(restored.Close)
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	var got []string
	for _, sess := range restored.ListVisible() {
		got = append(got, sess.ID)
	}
	if !reflect.DeepEqual(got, moved) {
		t.Errorf("a restart reads the order %v, want %v", got, moved)
	}
}

// A temporary file a stopped write left behind is not a session, and does
// not stop the others from loading. One older than staleTempAge is
// removed at the next load. A younger one is kept, because a daemon being
// replaced may still be writing it. Nothing else in the directory is
// removed, however old it is.
func TestALeftoverTempFileIsNotASession(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Each holds what a stopped write of a session's meta would: a whole,
	// valid record for a session that is not otherwise on disk.
	ghost, err := json.Marshal(Session{ID: "ghost", Visible: true, Agent: "general-purpose"})
	if err != nil {
		t.Fatal(err)
	}
	fresh := tempPrefix + "1111" + tempSuffix
	stale := tempPrefix + "2222" + tempSuffix
	// Names that are not this writer's, each old enough to be removed if
	// it were.
	notOurs := []string{".handoff.json.tmp", "notes" + tempSuffix, "s1.meta.json" + tempSuffix, tempPrefix + "4444.json", tempPrefix + tempSuffix}
	for _, name := range append([]string{fresh, stale}, notOurs...) {
		if err := os.WriteFile(filepath.Join(dir, name), ghost, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	staleDir := tempPrefix + "3333" + tempSuffix
	if err := os.Mkdir(filepath.Join(dir, staleDir), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * staleTempAge)
	for _, name := range append([]string{stale, staleDir}, notOurs...) {
		if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
			t.Fatal(err)
		}
	}

	restored, warnings, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(restored.Close)
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	var ids []string
	for _, sess := range restored.AllSessions() {
		ids = append(ids, sess.ID)
	}
	if want := []string{"s1"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("sessions loaded = %v, want %v", ids, want)
	}

	names := map[string]bool{}
	for _, name := range dirNames(t, dir) {
		names[name] = true
	}
	if names[stale] {
		t.Errorf("%s is older than %v and was not removed", stale, staleTempAge)
	}
	if !names[fresh] {
		t.Errorf("%s was removed, and a write still running may own it", fresh)
	}
	for _, name := range append([]string{staleDir}, notOurs...) {
		if !names[name] {
			t.Errorf("%s was removed, and it is not a temporary file of this writer", name)
		}
	}
}
