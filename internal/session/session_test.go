package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"localcode/internal/events"
)

func TestCreateSessionAndGet(t *testing.T) {
	s, err := NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sess, err := s.CreateSession("s1", "", "general-purpose", true)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ID != "s1" || sess.Agent != "general-purpose" || !sess.Visible {
		t.Errorf("unexpected session: %+v", sess)
	}

	got, err := s.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "s1" {
		t.Errorf("Get returned %+v", got)
	}
}

func TestCreateSessionDuplicateID(t *testing.T) {
	s, _ := NewStore("")
	if _, err := s.CreateSession("dup", "", "a", true); err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	if _, err := s.CreateSession("dup", "", "a", true); err == nil {
		t.Error("expected an error creating a session with a duplicate id")
	}
}

func TestSetAgentUpdatesSessionAndPersistsAcrossGet(t *testing.T) {
	s, _ := NewStore("")
	if _, err := s.CreateSession("s1", "", "plan", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	updated, err := s.SetAgent("s1", "build")
	if err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	if updated.Agent != "build" {
		t.Errorf("SetAgent returned Agent = %q, want %q", updated.Agent, "build")
	}

	got, err := s.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Agent != "build" {
		t.Errorf("Get after SetAgent returned Agent = %q, want %q", got.Agent, "build")
	}
}

func TestSetAgentUnknownSession(t *testing.T) {
	s, _ := NewStore("")
	if _, err := s.SetAgent("nope", "build"); err == nil {
		t.Error("expected an error switching the agent of an unknown session")
	}
}

func TestGetUnknownSession(t *testing.T) {
	s, _ := NewStore("")
	if _, err := s.Get("nope"); err == nil {
		t.Error("expected an error getting an unknown session")
	}
}

func TestSetTitleUpdatesSessionAndPersistsAcrossGet(t *testing.T) {
	s, _ := NewStore("")
	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	updated, err := s.SetTitle("s1", "My renamed session")
	if err != nil {
		t.Fatalf("SetTitle: %v", err)
	}
	if updated.Title != "My renamed session" {
		t.Errorf("SetTitle returned Title = %q, want %q", updated.Title, "My renamed session")
	}

	got, err := s.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "My renamed session" {
		t.Errorf("Get after SetTitle returned Title = %q, want %q", got.Title, "My renamed session")
	}
}

func TestSetTitleUnknownSession(t *testing.T) {
	s, _ := NewStore("")
	if _, err := s.SetTitle("nope", "x"); err == nil {
		t.Error("expected an error renaming an unknown session")
	}
}

func TestDeleteRemovesSessionFromStore(t *testing.T) {
	s, _ := NewStore("")
	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := s.Delete("s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("s1"); err == nil {
		t.Error("expected Get to fail after Delete")
	}
}

func TestDeleteUnknownSession(t *testing.T) {
	s, _ := NewStore("")
	if err := s.Delete("nope"); err == nil {
		t.Error("expected an error deleting an unknown session")
	}
}

func TestDeleteRemovesPersistedFile(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s.Append("s1", events.TypeUserMessage, map[string]any{"text": "hi"})

	path := filepath.Join(dir, "s1.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected the session log to exist before Delete: %v", err)
	}

	if err := s.Delete("s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected the session log to be removed after Delete, stat err = %v", err)
	}
}

func TestDeleteThenRecreateSameID(t *testing.T) {
	s, _ := NewStore("")
	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.Delete("s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Errorf("expected CreateSession to succeed again with the same ID after Delete, got %v", err)
	}
}

func TestChildrenFiltersToParent(t *testing.T) {
	s, _ := NewStore("")
	s.CreateSession("parent", "", "a", true)
	s.CreateSession("other-parent", "", "a", true)
	s.CreateSession("child1", "parent", "explore", false)
	s.CreateSession("child2", "parent", "explore", false)
	s.CreateSession("child-of-other", "other-parent", "explore", false)

	children := s.Children("parent")
	if len(children) != 2 {
		t.Fatalf("expected 2 children of \"parent\", got %d: %+v", len(children), children)
	}
	ids := map[string]bool{}
	for _, c := range children {
		ids[c.ID] = true
	}
	if !ids["child1"] || !ids["child2"] {
		t.Errorf("expected child1 and child2, got %+v", children)
	}
}

func TestListVisibleExcludesBackgroundTasksNewestFirst(t *testing.T) {
	s, _ := NewStore("")
	s.CreateSession("s1", "", "a", true)
	time.Sleep(2 * time.Millisecond)
	s.CreateSession("s2", "", "a", true)
	s.CreateSession("task1", "s1", "a", false) // background task, not visible

	list := s.ListVisible()
	if len(list) != 2 {
		t.Fatalf("expected 2 visible sessions, got %d: %+v", len(list), list)
	}
	if list[0].ID != "s2" || list[1].ID != "s1" {
		t.Errorf("expected newest-first [s2, s1], got [%s, %s]", list[0].ID, list[1].ID)
	}
}

func TestAppendAssignsIncreasingSeq(t *testing.T) {
	s, _ := NewStore("")
	s.CreateSession("s1", "", "a", true)

	ev1, err := s.Append("s1", events.TypeUserMessage, map[string]any{"text": "hi"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	ev2, err := s.Append("s1", events.TypeMessagePartEnd, nil)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	if ev1.Seq != 1 || ev2.Seq != 2 {
		t.Errorf("expected seq 1 then 2, got %d then %d", ev1.Seq, ev2.Seq)
	}
	if ev1.Session != "s1" {
		t.Errorf("event session = %q, want %q", ev1.Session, "s1")
	}
}

func TestAppendUnknownSession(t *testing.T) {
	s, _ := NewStore("")
	if _, err := s.Append("nope", events.TypeError, nil); err == nil {
		t.Error("expected an error appending to an unknown session")
	}
}

func TestEventsSinceFiltering(t *testing.T) {
	s, _ := NewStore("")
	s.CreateSession("s1", "", "a", true)
	s.Append("s1", events.TypeUserMessage, map[string]any{"n": 1})
	s.Append("s1", events.TypeUserMessage, map[string]any{"n": 2})
	s.Append("s1", events.TypeUserMessage, map[string]any{"n": 3})

	all, err := s.Events("s1", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 events since 0, got %d", len(all))
	}

	since1, err := s.Events("s1", 1)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(since1) != 2 || since1[0].Seq != 2 {
		t.Errorf("expected 2 events starting at seq 2, got %+v", since1)
	}

	sinceAll, err := s.Events("s1", 3)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(sinceAll) != 0 {
		t.Errorf("expected 0 events since the last seq, got %+v", sinceAll)
	}
}

func TestSubscribeReceivesLiveEventsAndClosesOnUnsubscribe(t *testing.T) {
	s, _ := NewStore("")
	s.CreateSession("s1", "", "a", true)

	ch, unsub, err := s.Subscribe("s1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	s.Append("s1", events.TypeUserMessage, map[string]any{"text": "hi"})

	select {
	case ev := <-ch:
		if ev.Type != events.TypeUserMessage {
			t.Errorf("received event type = %q, want %q", ev.Type, events.TypeUserMessage)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the live event")
	}

	unsub()

	// The channel should now be closed: a receive should return the zero
	// value and ok=false promptly rather than blocking.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected the channel to be closed after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel close after unsubscribe")
	}
}

func TestSubscribeUnknownSession(t *testing.T) {
	s, _ := NewStore("")
	if _, _, err := s.Subscribe("nope"); err == nil {
		t.Error("expected an error subscribing to an unknown session")
	}
}

func TestPersistenceAndLoadFromDisk(t *testing.T) {
	dir := t.TempDir()

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s.Append("s1", events.TypeUserMessage, map[string]any{"text": "hello"})
	s.Append("s1", events.TypeMessagePartDelta, map[string]any{"text": "hi there"})

	restored, err := LoadFromDisk(dir, "s1", "", "general-purpose", true)
	if err != nil {
		t.Fatalf("LoadFromDisk: %v", err)
	}

	replayed, err := restored.Events("s1", 0)
	if err != nil {
		t.Fatalf("Events on restored store: %v", err)
	}
	if len(replayed) != 2 {
		t.Fatalf("expected 2 replayed events, got %d: %+v", len(replayed), replayed)
	}
	if replayed[0].Type != events.TypeUserMessage {
		t.Errorf("replayed[0].Type = %q, want %q", replayed[0].Type, events.TypeUserMessage)
	}

	// A subsequent Append on the restored store should continue the seq
	// count rather than restarting at 1, so newly-live events never
	// collide with replayed ones.
	next, err := restored.Append("s1", events.TypeMessagePartEnd, nil)
	if err != nil {
		t.Fatalf("Append after restore: %v", err)
	}
	if next.Seq != 3 {
		t.Errorf("seq after restore = %d, want 3 (continuing from persisted log)", next.Seq)
	}
}

func TestLoadFromDiskNoExistingFile(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadFromDisk(dir, "brand-new", "", "a", true)
	if err != nil {
		t.Fatalf("LoadFromDisk with no prior log file: %v", err)
	}
	list, err := s.Events("brand-new", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected no events for a freshly-created session, got %+v", list)
	}
}
