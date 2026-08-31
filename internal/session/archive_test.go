package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Archiving, at the store.
//
// A shelf and not a bin, which is the whole of what these hold to: nothing
// a session had is lost, the log keeps working, and Retrieve puts it back
// where it was. The one thing that changes is that the conversation leaves
// the list and nothing new starts in it.

func active(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	for _, sess := range s.ListVisible() {
		out = append(out, sess.ID)
	}
	return out
}

func archived(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	for _, sess := range s.ListArchived() {
		out = append(out, sess.ID)
	}
	return out
}

func TestArchivingMovesAConversationBetweenTheTwoLists(t *testing.T) {
	s, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := s.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Archive("b"); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if got := active(t, s); len(got) != 2 || got[0] == "b" || got[1] == "b" {
		t.Errorf("active = %v, want b gone", got)
	}
	if got := archived(t, s); len(got) != 1 || got[0] != "b" {
		t.Errorf("archived = %v, want [b]", got)
	}

	if _, err := s.Retrieve("b"); err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(active(t, s)) != 3 || len(archived(t, s)) != 0 {
		t.Errorf("after retrieve: active %v, archived %v", active(t, s), archived(t, s))
	}
}

// Nothing is lost. This is the difference between archiving and deleting,
// and it is the promise the feature is named for.
func TestArchivingKeepsEverythingTheSessionHad(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSessionIn("a", "", "oracle", "/work/thing", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTitle("a", "the one about parsers"); err != nil {
		t.Fatal(err)
	}
	yes := true
	if _, err := s.SetPermission("a", SwitchSkipTools, &yes); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEffort("a", "high"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append("a", "message.user", map[string]any{"text": "hello"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Archive("a")
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if got.Title != "the one about parsers" || got.Workspace != "/work/thing" ||
		got.Agent != "oracle" || got.Effort != "high" ||
		got.Permissions.SkipTools == nil || !*got.Permissions.SkipTools {
		t.Errorf("archiving changed the session: %+v", got)
	}
	if got.ArchivedAt == nil {
		t.Fatal("archived without a timestamp")
	}

	// The log is still readable, which is the point of keeping it.
	evs, err := s.Events("a", 0)
	if err != nil || len(evs) != 1 {
		t.Errorf("events after archiving: %d, %v", len(evs), err)
	}
	// And still writable: a task that outlives the archive still reports,
	// and a schedule still records that it was missed.
	if _, err := s.Append("a", "task.status", map[string]any{"status": "completed"}); err != nil {
		t.Errorf("append to an archived session: %v", err)
	}
}

func TestArchivingSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err := s.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Archive("a"); err != nil {
		t.Fatal(err)
	}

	reopened, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := archived(t, reopened); len(got) != 1 || got[0] != "a" {
		t.Errorf("after restart, archived = %v", got)
	}
	if got := active(t, reopened); len(got) != 1 || got[0] != "b" {
		t.Errorf("after restart, active = %v", got)
	}
}

// The migration story, such as it is: a meta file written before the field
// existed has no archived_at, which unmarshals to nil, which is active.
func TestAMetaFileWithoutTheFieldLoadsAsActive(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.meta.json"),
		[]byte(`{"id":"old","visible":true,"created_at":"2026-01-01T00:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := active(t, s); len(got) != 1 || got[0] != "old" {
		t.Errorf("a session from before the field is not active: %v", got)
	}
}

// The one check that cannot be raced: nothing new starts under an archived
// conversation, enforced under the same mutex the archive is written with.
// Every way work begins (a Task spawn, a synchronous delegation, a
// scheduled run) arrives here.
func TestNothingIsCreatedUnderAnArchivedConversation(t *testing.T) {
	s, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Archive("parent"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession("task-1", "parent", "explore", false); err == nil {
		t.Error("a task session was created under an archived conversation")
	}

	// And it is allowed again the moment it comes back.
	if _, err := s.Retrieve("parent"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession("task-2", "parent", "explore", false); err != nil {
		t.Errorf("a retrieved conversation still refuses children: %v", err)
	}
}

// A task's session is in no list, so archiving it hides nothing, and its
// parent's turn is waiting on it.
func TestABackgroundTasksSessionCannotBeArchived(t *testing.T) {
	s, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.CreateSession("parent", "", "general-purpose", true)
	s.CreateSession("task-1", "parent", "explore", false)

	if _, err := s.Archive("task-1"); err == nil {
		t.Error("a background task's session was archived")
	} else if got := err.Error(); got != "session task-1 is a background task, not a conversation" {
		t.Errorf("error = %q", got)
	}
}

// Two clients pressing the button is not a conflict worth an error page,
// and the first timestamp is the one that means something.
func TestArchivingTwiceIsANoOpAndKeepsTheFirstTime(t *testing.T) {
	s, _ := NewStore("")
	s.CreateSession("a", "", "general-purpose", true)

	first, err := s.Archive("a")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Archive("a")
	if err != nil {
		t.Fatalf("archiving twice: %v", err)
	}
	if !again.ArchivedAt.Equal(*first.ArchivedAt) {
		t.Error("the second archive moved the timestamp")
	}

	// And retrieving something that is not archived changes nothing.
	s.CreateSession("b", "", "general-purpose", true)
	before := s.ListVisible()
	if _, err := s.Retrieve("b"); err != nil {
		t.Errorf("retrieving an active session: %v", err)
	}
	if len(s.ListVisible()) != len(before) {
		t.Error("retrieving an active session changed the list")
	}
}

// The rank comes back, not the number. A session archived from the middle
// of a hand-arranged list returns to the middle.
func TestRetrieveRestoresThePlaceInAHandArrangedList(t *testing.T) {
	s, _ := NewStore("")
	for _, id := range []string{"a", "b", "c", "d"} {
		s.CreateSession(id, "", "general-purpose", true)
	}
	if err := s.SetOrder([]string{"a", "b", "c", "d"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Archive("c"); err != nil {
		t.Fatal(err)
	}
	if got := active(t, s); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "d" {
		t.Fatalf("after archiving c: %v", got)
	}

	if _, err := s.Retrieve("c"); err != nil {
		t.Fatal(err)
	}
	if got := active(t, s); len(got) != 4 || got[2] != "c" {
		t.Errorf("c did not come back to third place: %v", got)
	}

	// Dense afterwards, so a later drag has no ties to break.
	seen := map[int]bool{}
	for _, sess := range s.ListVisible() {
		if sess.Order == 0 || seen[sess.Order] {
			t.Errorf("order is not dense: %v has %d", sess.ID, sess.Order)
		}
		seen[sess.Order] = true
	}
}

// A reorder that names an archived session is refused with its own
// message, because the fix is its own: retrieve it first.
func TestReorderingRefusesAnArchivedSession(t *testing.T) {
	s, _ := NewStore("")
	s.CreateSession("a", "", "general-purpose", true)
	s.CreateSession("b", "", "general-purpose", true)
	s.Archive("b")

	err := s.SetOrder([]string{"a", "b"})
	if err == nil {
		t.Fatal("a reorder naming an archived session was accepted")
	}
	if got := err.Error(); got != "session b is archived" {
		t.Errorf("error = %q", got)
	}

	// And an ordinary reorder does not renumber the archived one into the
	// active list behind everyone's back.
	if err := s.SetOrder([]string{"a"}); err != nil {
		t.Fatalf("reordering the active list: %v", err)
	}
	if got := archived(t, s); len(got) != 1 {
		t.Errorf("the archived session left the archive: %v", got)
	}
}
