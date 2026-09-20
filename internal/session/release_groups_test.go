package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestGroupSurvivesRestart verifies that group definitions and session group
// assignments survive a restart through LoadAllFromDisk.
func TestGroupSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)

	if err := s.SetGroups([]string{"work", "personal"}, nil); err != nil {
		t.Fatalf("SetGroups: %v", err)
	}

	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession s1: %v", err)
	}
	if _, err := s.CreateSession("s2", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession s2: %v", err)
	}

	if _, err := s.SetSessionGroup("s1", "work"); err != nil {
		t.Fatalf("SetSessionGroup s1: %v", err)
	}

	// Close store and reopen from disk.
	s.Close()

	s2, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(s2.Close)

	groups := s2.GetGroups()
	if want := []string{"work", "personal"}; !reflect.DeepEqual(groups, want) {
		t.Errorf("GetGroups after restart = %v, want %v", groups, want)
	}

	sess1, err := s2.Get("s1")
	if err != nil {
		t.Fatalf("Get s1: %v", err)
	}
	if sess1.Group != "work" {
		t.Errorf("s1.Group after restart = %q, want %q", sess1.Group, "work")
	}

	sess2, err := s2.Get("s2")
	if err != nil {
		t.Fatalf("Get s2: %v", err)
	}
	if sess2.Group != "" {
		t.Errorf("s2.Group after restart = %q, want empty string", sess2.Group)
	}
}

// TestGroupNameValidationRefusesInvalidAndNamesIt verifies that group name
// validation refuses invalid names and names the offending name in the error.
func TestGroupNameValidationRefusesInvalidAndNamesIt(t *testing.T) {
	s, err := NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)

	tooLong := strings.Repeat("a", 101)

	tests := []struct {
		name string
		// groups is the list submitted; wantNamed is the name the refusal
		// must quote back, and wantSays a word from the reason it gives.
		groups    []string
		wantNamed string
		wantSays  string
	}{
		{
			name:      "empty name",
			groups:    []string{""},
			wantNamed: "",
			wantSays:  "empty",
		},
		{
			name:      "leading whitespace",
			groups:    []string{"  work"},
			wantNamed: "  work",
			wantSays:  "space",
		},
		{
			name:      "trailing whitespace",
			groups:    []string{"work "},
			wantNamed: "work ",
			wantSays:  "space",
		},
		{
			name:      "contains newline",
			groups:    []string{"work\nproject"},
			wantNamed: "work\nproject",
			wantSays:  "control character",
		},
		{
			name:      "contains control character",
			groups:    []string{"work\x00project"},
			wantNamed: "work\x00project",
			wantSays:  "control character",
		},
		{
			name:      "duplicate name",
			groups:    []string{"work", "personal", "work"},
			wantNamed: "work",
			wantSays:  "twice",
		},
		{
			name:      "name too long",
			groups:    []string{tooLong},
			wantNamed: tooLong,
			wantSays:  "longer than",
		},
		{
			// The limit counts characters, not bytes. 101 Korean characters
			// is 303 bytes; a byte limit would have refused this at 34.
			name:      "too long in a language that is not ASCII",
			groups:    []string{strings.Repeat("가", 101)},
			wantNamed: strings.Repeat("가", 101),
			wantSays:  "longer than",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.SetGroups(tt.groups, nil)
			if err == nil {
				t.Fatalf("SetGroups(%v) succeeded, want error", tt.groups)
			}
			// The refusal quotes the name, so a name carrying a newline or a
			// NUL reaches the reader as an escape rather than breaking the
			// line it is printed on. Look for the quoted form, which is what
			// the person actually sees.
			quoted := strconv.Quote(tt.wantNamed)
			if !strings.Contains(err.Error(), quoted) {
				t.Errorf("SetGroups error %q does not name offending name %s", err.Error(), quoted)
			}
			if !strings.Contains(err.Error(), tt.wantSays) {
				t.Errorf("SetGroups error %q does not say why (want it to mention %q)", err.Error(), tt.wantSays)
			}
		})
	}

	// The other side of the limit: a name right at it is accepted, in a
	// language where one character is three bytes. Without both halves the
	// refusals above pass just as well against a limit counting bytes,
	// which would turn a 34-character Korean group name away.
	atLimit := strings.Repeat("가", maxGroupNameRunes)
	if err := s.SetGroups([]string{atLimit}, nil); err != nil {
		t.Errorf("SetGroups with a %d-character name refused: %v", maxGroupNameRunes, err)
	}
}

// TestAssignNonexistentGroupRefused verifies that setting a session's group to
// one not in the store's group list is refused and leaves the session unchanged.
func TestAssignNonexistentGroupRefused(t *testing.T) {
	s, err := NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)

	if err := s.SetGroups([]string{"alpha"}, nil); err != nil {
		t.Fatalf("SetGroups: %v", err)
	}

	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	_, err = s.SetSessionGroup("s1", "beta")
	if err == nil {
		t.Fatal("SetSessionGroup to nonexistent group succeeded, want error")
	}
	if !strings.Contains(err.Error(), "beta") {
		t.Errorf("error %q does not name nonexistent group 'beta'", err.Error())
	}

	sess, err := s.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sess.Group != "" {
		t.Errorf("s1.Group = %q, want empty string", sess.Group)
	}
}

// TestDeletingGroupLeavesSessionsUngrouped verifies that removing a group
// leaves sessions in that group ungrouped, both in memory and after restart.
func TestDeletingGroupLeavesSessionsUngrouped(t *testing.T) {
	dir := t.TempDir()

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)

	if err := s.SetGroups([]string{"work", "personal"}, nil); err != nil {
		t.Fatalf("SetGroups: %v", err)
	}

	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession s1: %v", err)
	}
	if _, err := s.CreateSession("s2", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession s2: %v", err)
	}

	if _, err := s.SetSessionGroup("s1", "work"); err != nil {
		t.Fatalf("SetSessionGroup s1: %v", err)
	}
	if _, err := s.SetSessionGroup("s2", "personal"); err != nil {
		t.Fatalf("SetSessionGroup s2: %v", err)
	}

	// Delete "work" group by saving only "personal".
	if err := s.SetGroups([]string{"personal"}, nil); err != nil {
		t.Fatalf("SetGroups: %v", err)
	}

	sess1, err := s.Get("s1")
	if err != nil {
		t.Fatalf("Get s1: %v", err)
	}
	if sess1.Group != "" {
		t.Errorf("in-memory s1.Group = %q, want empty string after deletion", sess1.Group)
	}

	sess2, err := s.Get("s2")
	if err != nil {
		t.Fatalf("Get s2: %v", err)
	}
	if sess2.Group != "personal" {
		t.Errorf("in-memory s2.Group = %q, want personal", sess2.Group)
	}

	s.Close()

	s2, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(s2.Close)

	sess1After, err := s2.Get("s1")
	if err != nil {
		t.Fatalf("Get s1 after restart: %v", err)
	}
	if sess1After.Group != "" {
		t.Errorf("persisted s1.Group = %q, want empty string after deletion", sess1After.Group)
	}
}

// TestRenamingGroupCarriesSessions verifies that renaming a group carries
// sessions assigned to that group to the new group name.
func TestRenamingGroupCarriesSessions(t *testing.T) {
	dir := t.TempDir()

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)

	if err := s.SetGroups([]string{"work", "personal"}, nil); err != nil {
		t.Fatalf("SetGroups: %v", err)
	}

	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession s1: %v", err)
	}
	if _, err := s.CreateSession("s2", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession s2: %v", err)
	}

	if _, err := s.SetSessionGroup("s1", "work"); err != nil {
		t.Fatalf("SetSessionGroup s1: %v", err)
	}
	if _, err := s.SetSessionGroup("s2", "personal"); err != nil {
		t.Fatalf("SetSessionGroup s2: %v", err)
	}

	// Rename "work" to "work-projects".
	if err := s.SetGroups([]string{"work-projects", "personal"}, &GroupRename{From: "work", To: "work-projects"}); err != nil {
		t.Fatalf("SetGroups rename: %v", err)
	}

	sess1, err := s.Get("s1")
	if err != nil {
		t.Fatalf("Get s1: %v", err)
	}
	if sess1.Group != "work-projects" {
		t.Errorf("in-memory s1.Group = %q, want 'work-projects'", sess1.Group)
	}

	sess2, err := s.Get("s2")
	if err != nil {
		t.Fatalf("Get s2: %v", err)
	}
	if sess2.Group != "personal" {
		t.Errorf("in-memory s2.Group = %q, want 'personal'", sess2.Group)
	}

	// Restart and check on disk.
	s.Close()

	s2, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(s2.Close)

	sess1After, err := s2.Get("s1")
	if err != nil {
		t.Fatalf("Get s1 after restart: %v", err)
	}
	if sess1After.Group != "work-projects" {
		t.Errorf("persisted s1.Group = %q, want 'work-projects'", sess1After.Group)
	}
}

// Archiving a conversation must not destroy which group it was in.
// Archiving is not deleting anywhere else in this store — the title, the
// workspace, the permissions and the list rank all survive it — and the
// group is the one thing a person arranged by hand. An archived session is
// not in the panel, so keeping it costs nothing on screen and gives
// Retrieve the arrangement back for free.
func TestArchivingKeepsTheGroupAndRetrieveBringsItBack(t *testing.T) {
	dir := t.TempDir()

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)

	if err := s.SetGroups([]string{"work"}, nil); err != nil {
		t.Fatalf("SetGroups: %v", err)
	}
	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession s1: %v", err)
	}
	if _, err := s.SetSessionGroup("s1", "work"); err != nil {
		t.Fatalf("SetSessionGroup s1: %v", err)
	}

	archived, err := s.Archive("s1")
	if err != nil {
		t.Fatalf("Archive s1: %v", err)
	}
	if archived.Group != "work" {
		t.Errorf("Archive returned Group = %q, want it kept as %q", archived.Group, "work")
	}

	// It is still out of the panel, so it still refuses to be moved
	// between groups — the arrangement is kept, not editable.
	if _, err := s.SetSessionGroup("s1", "work"); err == nil {
		t.Error("SetSessionGroup on an archived session succeeded, want a refusal")
	}

	// And it survives a restart, so retrieving it tomorrow is the same as
	// retrieving it now.
	s.Close()
	s2, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(s2.Close)

	back, err := s2.Retrieve("s1")
	if err != nil {
		t.Fatalf("Retrieve s1: %v", err)
	}
	if back.Group != "work" {
		t.Errorf("Retrieve returned Group = %q, want %q: the arrangement came back with it", back.Group, "work")
	}
	if got, err := s2.Get("s1"); err != nil {
		t.Fatalf("Get s1: %v", err)
	} else if got.Group != "work" {
		t.Errorf("Get after retrieve = %q, want %q", got.Group, "work")
	}
}

// TestNonexistentGroupReadsAsUngrouped verifies that if session metadata records
// a group that does not exist in the store's group list, read operations report
// the session as ungrouped.
func TestNonexistentGroupReadsAsUngrouped(t *testing.T) {
	dir := t.TempDir()

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)

	if err := s.SetGroups([]string{"alpha"}, nil); err != nil {
		t.Fatalf("SetGroups: %v", err)
	}

	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession s1: %v", err)
	}
	if _, err := s.SetSessionGroup("s1", "alpha"); err != nil {
		t.Fatalf("SetSessionGroup s1: %v", err)
	}

	// Remove groups.json on disk to simulate missing/desynchronized group list.
	groupsFile := filepath.Join(dir, "groups.json")
	if err := os.Remove(groupsFile); err != nil {
		t.Fatalf("Remove groups.json: %v", err)
	}

	s.Close()

	s2, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(s2.Close)

	// Store has no groups loaded.
	if len(s2.GetGroups()) != 0 {
		t.Fatalf("GetGroups = %v, want empty", s2.GetGroups())
	}

	// s1 must read as ungrouped.
	sess1, err := s2.Get("s1")
	if err != nil {
		t.Fatalf("Get s1: %v", err)
	}
	if sess1.Group != "" {
		t.Errorf("s1.Group = %q, want empty string for nonexistent group", sess1.Group)
	}

	visible := s2.ListVisible()
	if len(visible) != 1 || visible[0].Group != "" {
		t.Errorf("ListVisible()[0].Group = %q, want empty string", visible[0].Group)
	}
}

// TestGroupPersistenceRollback verifies that failures during group or session
// metadata persistence leave in-memory state cleanly rolled back.
func TestGroupPersistenceRollback(t *testing.T) {
	errInjected := errors.New("injected write failure")

	t.Run("SetGroups file write failure", func(t *testing.T) {
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		t.Cleanup(s.Close)

		if err := s.SetGroups([]string{"initial"}, nil); err != nil {
			t.Fatalf("SetGroups: %v", err)
		}

		s.writeGroups = func(string, []string) error {
			return errInjected
		}

		err = s.SetGroups([]string{"initial", "second"}, nil)
		if !errors.Is(err, errInjected) {
			t.Fatalf("SetGroups err = %v, want %v", err, errInjected)
		}

		got := s.GetGroups()
		if want := []string{"initial"}; !reflect.DeepEqual(got, want) {
			t.Errorf("GetGroups after rollback = %v, want %v", got, want)
		}
	})

	t.Run("SetGroups session meta write failure rolls back rename", func(t *testing.T) {
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		t.Cleanup(s.Close)

		if err := s.SetGroups([]string{"work"}, nil); err != nil {
			t.Fatalf("SetGroups: %v", err)
		}
		if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if _, err := s.SetSessionGroup("s1", "work"); err != nil {
			t.Fatalf("SetSessionGroup: %v", err)
		}

		// Inject writeMeta error.
		s.writeMeta = func(string, Session) error {
			return errInjected
		}

		err = s.SetGroups([]string{"work-renamed"}, &GroupRename{From: "work", To: "work-renamed"})
		if !errors.Is(err, errInjected) {
			t.Fatalf("SetGroups err = %v, want %v", err, errInjected)
		}

		got := s.GetGroups()
		if want := []string{"work"}; !reflect.DeepEqual(got, want) {
			t.Errorf("GetGroups after rollback = %v, want %v", got, want)
		}

		sess, err := s.Get("s1")
		if err != nil {
			t.Fatalf("Get s1: %v", err)
		}
		if sess.Group != "work" {
			t.Errorf("s1.Group after rollback = %q, want 'work'", sess.Group)
		}
	})

	t.Run("SetSessionGroup meta write failure rolls back", func(t *testing.T) {
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		t.Cleanup(s.Close)

		if err := s.SetGroups([]string{"work"}, nil); err != nil {
			t.Fatalf("SetGroups: %v", err)
		}
		if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		s.writeMeta = func(string, Session) error {
			return errInjected
		}

		_, err = s.SetSessionGroup("s1", "work")
		if !errors.Is(err, errInjected) {
			t.Fatalf("SetSessionGroup err = %v, want %v", err, errInjected)
		}

		sess, err := s.Get("s1")
		if err != nil {
			t.Fatalf("Get s1: %v", err)
		}
		if sess.Group != "" {
			t.Errorf("s1.Group after rollback = %q, want empty string", sess.Group)
		}
	})
}

// TestRenameMustBeStated is the guard on the decision that a rename is
// never inferred from the two lists. Submitting a list where one name
// vanished and another appeared is a deletion and a creation, because
// that is also exactly what a deletion and a creation look like — and a
// second window creating a group while this one renames another produces
// the same two-name difference. Reading it as a rename would empty a
// group silently, which is the one outcome worth ruling out.
func TestRenameMustBeStated(t *testing.T) {
	newStore := func(t *testing.T) *Store {
		t.Helper()
		s, _, err := LoadAllFromDisk(t.TempDir())
		if err != nil {
			t.Fatalf("LoadAllFromDisk: %v", err)
		}
		t.Cleanup(s.Close)
		if err := s.SetGroups([]string{"work"}, nil); err != nil {
			t.Fatalf("SetGroups: %v", err)
		}
		if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if _, err := s.SetSessionGroup("s1", "work"); err != nil {
			t.Fatalf("SetSessionGroup: %v", err)
		}
		return s
	}

	t.Run("unstated is a deletion, not a guess", func(t *testing.T) {
		s := newStore(t)
		if err := s.SetGroups([]string{"job"}, nil); err != nil {
			t.Fatalf("SetGroups: %v", err)
		}
		got, err := s.Get("s1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Group != "" {
			t.Errorf("s1.Group = %q, want it left ungrouped: nothing said work became job", got.Group)
		}
	})

	t.Run("stated carries the sessions", func(t *testing.T) {
		s := newStore(t)
		if err := s.SetGroups([]string{"job"}, &GroupRename{From: "work", To: "job"}); err != nil {
			t.Fatalf("SetGroups: %v", err)
		}
		got, err := s.Get("s1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Group != "job" {
			t.Errorf("s1.Group = %q, want %q", got.Group, "job")
		}
	})

	refusals := []struct {
		name   string
		names  []string
		rename *GroupRename
		says   string
	}{
		{
			name:   "from is not a group",
			names:  []string{"job"},
			rename: &GroupRename{From: "nonesuch", To: "job"},
			says:   "no group by that name",
		},
		{
			name:   "to is not in the submitted list",
			names:  []string{"work"},
			rename: &GroupRename{From: "work", To: "job"},
			says:   "not in the submitted list",
		},
		{
			name:   "from is still in the submitted list",
			names:  []string{"work", "job"},
			rename: &GroupRename{From: "work", To: "job"},
			says:   "still in the submitted list",
		},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t)
			err := s.SetGroups(tt.names, tt.rename)
			if err == nil {
				t.Fatalf("SetGroups(%v, %+v) succeeded, want a refusal", tt.names, tt.rename)
			}
			if !strings.Contains(err.Error(), tt.says) {
				t.Errorf("refusal %q does not say %q", err.Error(), tt.says)
			}
			if got := s.GetGroups(); len(got) != 1 || got[0] != "work" {
				t.Errorf("GetGroups after a refusal = %v, want the list untouched", got)
			}
		})
	}
}

// A disk that fails partway through SetGroups. There is no way to write
// several files at once, so this call can be partial — the question this
// guards is what the store says about itself afterwards.
//
// It must not lie. Whatever the directory ended up holding, memory must
// say the same thing, so that a restart changes nothing and the next read
// is not a different answer. Rolling everything back in memory would read
// better in a test and be false on disk.
func TestSetGroupsPartialWriteLeavesMemoryMatchingTheFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)

	if err := s.SetGroups([]string{"g1", "g2"}, nil); err != nil {
		t.Fatalf("SetGroups: %v", err)
	}
	for id, group := range map[string]string{"a": "g1", "b": "g2"} {
		if _, err := s.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatalf("CreateSession(%s): %v", id, err)
		}
		if _, err := s.SetSessionGroup(id, group); err != nil {
			t.Fatalf("SetSessionGroup(%s): %v", id, err)
		}
	}

	// The first session's file is written for real; every later one fails.
	injected := errors.New("injected staggered meta failure")
	calls := 0
	s.writeMeta = func(d string, m Session) error {
		calls++
		if calls == 1 {
			return writeSessionMeta(d, m)
		}
		return injected
	}

	err = s.SetGroups([]string{}, nil)
	if !errors.Is(err, injected) {
		t.Fatalf("SetGroups err = %v, want it to wrap %v", err, injected)
	}
	// And it must say which sessions did not move, because that is the
	// only part the caller can do anything about.
	if !strings.Contains(err.Error(), "could not be moved") {
		t.Errorf("error %q does not name what was refused", err.Error())
	}

	s.writeMeta = nil
	for _, id := range []string{"a", "b"} {
		inMemory, err := s.Get(id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		data, err := os.ReadFile(filepath.Join(dir, id+".meta.json"))
		if err != nil {
			t.Fatalf("read %s.meta.json: %v", id, err)
		}
		var onDisk Session
		if err := json.Unmarshal(data, &onDisk); err != nil {
			t.Fatalf("parse %s.meta.json: %v", id, err)
		}
		if inMemory.Group != onDisk.Group {
			t.Errorf("session %s: memory says group %q, the file says %q — the store is claiming something the directory does not hold",
				id, inMemory.Group, onDisk.Group)
		}
	}

	// And a restart settles on the same answer. Whatever the directory
	// ended up holding is what comes back, and coming back does not change
	// it again — a store that healed differently on every start would be
	// the same lie told more slowly.
	s.Close()
	s2, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(s2.Close)
	settled := map[string]string{}
	for _, id := range []string{"a", "b"} {
		got, err := s2.Get(id)
		if err != nil {
			t.Fatalf("Get(%s) after restart: %v", id, err)
		}
		settled[id] = got.Group
		data, err := os.ReadFile(filepath.Join(dir, id+".meta.json"))
		if err != nil {
			t.Fatalf("read %s.meta.json after restart: %v", id, err)
		}
		var onDisk Session
		if err := json.Unmarshal(data, &onDisk); err != nil {
			t.Fatalf("parse %s.meta.json after restart: %v", id, err)
		}
		if got.Group != onDisk.Group {
			t.Errorf("session %s after a restart: memory says %q, the file says %q", id, got.Group, onDisk.Group)
		}
		// And nothing may name a group the list does not have.
		if got.Group != "" {
			found := false
			for _, name := range s2.GetGroups() {
				if name == got.Group {
					found = true
				}
			}
			if !found {
				t.Errorf("session %s names group %q, which is not in %v", id, got.Group, s2.GetGroups())
			}
		}
	}
	s2.Close()
	s3, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("second LoadAllFromDisk: %v", err)
	}
	t.Cleanup(s3.Close)
	for id, want := range settled {
		got, err := s3.Get(id)
		if err != nil {
			t.Fatalf("Get(%s) after second restart: %v", id, err)
		}
		if got.Group != want {
			t.Errorf("session %s: group settled at %q and then became %q on the next start", id, want, got.Group)
		}
	}
}

// A group name that is only spaces. validateGroups refuses a padded name
// everywhere a group is created, so the one path that used to tidy it
// instead — trimming, then finding "" and reading that as "take it out of
// its group" — turned a typo into a silent unfiling.
func TestBlankGroupNameIsRefusedNotTreatedAsUngrouping(t *testing.T) {
	s, _, err := LoadAllFromDisk(t.TempDir())
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.SetGroups([]string{"work"}, nil); err != nil {
		t.Fatalf("SetGroups: %v", err)
	}
	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.SetSessionGroup("s1", "work"); err != nil {
		t.Fatalf("SetSessionGroup: %v", err)
	}

	for _, name := range []string{"   ", "\t", " work"} {
		if _, err := s.SetSessionGroup("s1", name); err == nil {
			t.Errorf("SetSessionGroup(%q) succeeded, want a refusal", name)
		}
		got, err := s.Get("s1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Group != "work" {
			t.Errorf("after a refused %q the session is in %q, want it left in %q", name, got.Group, "work")
		}
	}

	// The empty string still means what it says.
	if _, err := s.SetSessionGroup("s1", ""); err != nil {
		t.Fatalf(`SetSessionGroup("") = %v, want it to ungroup`, err)
	}
	if got, err := s.Get("s1"); err != nil {
		t.Fatalf("Get: %v", err)
	} else if got.Group != "" {
		t.Errorf("after ungrouping, group = %q, want empty", got.Group)
	}
}

// Every way of reading a session must give the same group. The record is
// corrected once at load, against the group list, rather than patched on
// the way out of some accessors and not others — a session that reads one
// way through Get and another through Retrieve is the kind of bug that
// costs a day, and it cannot happen if nothing is patched on the way out.
func TestEveryReaderAgreesAboutTheGroup(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.SetGroups([]string{"work"}, nil); err != nil {
		t.Fatalf("SetGroups: %v", err)
	}
	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.SetSessionGroup("s1", "work"); err != nil {
		t.Fatalf("SetSessionGroup: %v", err)
	}
	s.Close()

	// Take the group list away behind the store's back, the way a file
	// somebody edited or a half-written call would.
	if err := os.Remove(filepath.Join(dir, "groups.json")); err != nil {
		t.Fatalf("remove groups.json: %v", err)
	}

	s2, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(s2.Close)

	readers := map[string]func() (string, error){
		"Get": func() (string, error) {
			got, err := s2.Get("s1")
			if err != nil {
				return "", err
			}
			return got.Group, nil
		},
		"ListVisible": func() (string, error) {
			for _, sess := range s2.ListVisible() {
				if sess.ID == "s1" {
					return sess.Group, nil
				}
			}
			return "", errors.New("s1 not in ListVisible")
		},
		"AllSessions": func() (string, error) {
			for _, sess := range s2.AllSessions() {
				if sess.ID == "s1" {
					return sess.Group, nil
				}
			}
			return "", errors.New("s1 not in AllSessions")
		},
		"SetTitle": func() (string, error) {
			got, err := s2.SetTitle("s1", "renamed")
			if err != nil {
				return "", err
			}
			return got.Group, nil
		},
	}
	for name, read := range readers {
		got, err := read()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != "" {
			t.Errorf("%s reports group %q; the group list does not have it, and every reader must say so", name, got)
		}
	}

	// And the correction was written, so it does not have to be made again.
	data, err := os.ReadFile(filepath.Join(dir, "s1.meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if strings.Contains(string(data), `"group"`) {
		t.Errorf("the meta file still records a group: %s", data)
	}
}
