package session

import (
	"reflect"
	"testing"
)

// groupMembers lists the ids in one group, in the order ListVisible returns
// them. That is the order the panel draws inside the group: there is no
// per-group order, only the one saved panel order read through a group.
func groupMembers(s *Store, group string) []string {
	var ids []string
	for _, sess := range s.ListVisible() {
		if sess.Group == group {
			ids = append(ids, sess.ID)
		}
	}
	return ids
}

// TestOrderInsideAGroupSurvivesRestart pins what dragging a card inside a
// group relies on: the saved panel order and the group of each session are
// two records of one panel, and a reorder that stays inside a group moves
// cards past each other without moving any of them out.
func TestOrderInsideAGroupSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)

	if err := s.SetGroups([]string{"g"}, nil); err != nil {
		t.Fatalf("SetGroups: %v", err)
	}
	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		if _, err := s.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatalf("CreateSession %s: %v", id, err)
		}
	}
	for _, id := range []string{"s2", "s3", "s4"} {
		if _, err := s.SetSessionGroup(id, "g"); err != nil {
			t.Fatalf("SetSessionGroup %s: %v", id, err)
		}
	}

	if err := s.SetOrder([]string{"s1", "s4", "s2", "s3"}); err != nil {
		t.Fatalf("SetOrder: %v", err)
	}
	if got, want := groupMembers(s, "g"), []string{"s4", "s2", "s3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group order after SetOrder = %v, want %v", got, want)
	}

	// Another arrangement of the same members: the group's order changes
	// and nobody changes group.
	if err := s.SetOrder([]string{"s1", "s3", "s4", "s2"}); err != nil {
		t.Fatalf("SetOrder (second): %v", err)
	}
	if got, want := groupMembers(s, "g"), []string{"s3", "s4", "s2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group order after the second SetOrder = %v, want %v", got, want)
	}
	if got := groupMembers(s, ""); !reflect.DeepEqual(got, []string{"s1"}) {
		t.Fatalf("ungrouped after reordering inside g = %v, want [s1]", got)
	}

	s.Close()

	s2, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(s2.Close)

	if got, want := groupMembers(s2, "g"), []string{"s3", "s4", "s2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("group order after restart = %v, want %v", got, want)
	}
	for _, id := range []string{"s2", "s3", "s4"} {
		sess, err := s2.Get(id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if sess.Group != "g" {
			t.Errorf("%s.Group after restart = %q, want g", id, sess.Group)
		}
	}
	if got := groupMembers(s2, ""); !reflect.DeepEqual(got, []string{"s1"}) {
		t.Errorf("ungrouped after restart = %v, want [s1]", got)
	}
}
