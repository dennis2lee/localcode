package session

import (
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

// TestArchivedSessionClearsGroup verifies that archiving a session clears its
// group field, both in memory and on disk.
func TestArchivedSessionClearsGroup(t *testing.T) {
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

	// Archive the session.
	if _, err := s.Archive("s1"); err != nil {
		t.Fatalf("Archive s1: %v", err)
	}

	sess1, err := s.Get("s1")
	if err != nil {
		t.Fatalf("Get s1: %v", err)
	}
	if sess1.Group != "" {
		t.Errorf("archived s1.Group = %q, want empty string", sess1.Group)
	}

	// Setting group on archived session is refused.
	if _, err := s.SetSessionGroup("s1", "work"); err == nil {
		t.Fatal("SetSessionGroup on archived session succeeded, want error")
	}

	// Verify on-disk after restart.
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
		t.Errorf("persisted archived s1.Group = %q, want empty string", sess1After.Group)
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
