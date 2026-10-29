package agent

import (
	"context"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

func TestCloseStaleQuestionsClosesEachLogOnce(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct{ id, parent string }{{"parent", ""}, {"task", "parent"}} {
		if _, err := store.CreateSession(s.id, s.parent, "general-purpose", s.parent == ""); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []events.Type{events.TypePermissionRequest, events.TypeInputRequest} {
			if _, err := store.Append(s.id, kind, map[string]any{"id": string(kind), "task_session": "task"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	store.Close()
	restored, warnings, err := session.LoadAllFromDisk(dir)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("load: %v, %v", err, warnings)
	}
	t.Cleanup(restored.Close)
	loop, _ := scriptedLoop(t, &scriptedProvider{}, tools.NewRegistry(nil))
	loop.Store = restored
	loop.Input = NewInputBroker(restored)
	for range 2 {
		for _, sid := range []string{"parent", "task"} {
			loop.CloseStaleQuestions(sid)
			assertStaleQuestionsClosed(t, restored, sid)
		}
	}
}

func assertStaleQuestionsClosed(t *testing.T, store *session.Store, sid string) {
	t.Helper()
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []events.Type{events.TypePermissionResolved, events.TypeInputResolved} {
		count := 0
		for _, ev := range evs {
			if ev.Type != kind {
				continue
			}
			count++
			if kind == events.TypePermissionResolved {
				if ev.Data["id"] != string(events.TypePermissionRequest) || ev.Data["allow"] != false || ev.Data["scope"] != ScopeOnce || ev.Data["unanswered"] != true {
					t.Errorf("%s: wrong permission resolution: %v", sid, ev.Data)
				}
			} else if ev.Data["id"] != string(events.TypeInputRequest) || ev.Data["cancelled"] != true {
				t.Errorf("%s: wrong input resolution: %v", sid, ev.Data)
			}
		}
		if count != 1 {
			t.Errorf("%s: got %d %s events, want 1", sid, count, kind)
		}
	}
}

func TestInputIDsDoNotRepeatAcrossBrokers(t *testing.T) {
	_, store, _ := newPermissionTestBroker(t)
	ids := map[string]bool{}
	for range 2 {
		broker := NewInputBroker(store)
		before, _ := store.Events("s1", 0)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); broker.Ask(ctx, "s1", "Which store?", nil) }()
		id := waitForInputQuestion(t, store, "s1", len(before))
		cancel()
		<-done
		if ids[id] {
			t.Errorf("different brokers reused input id %q", id)
		}
		ids[id] = true
	}
}

func waitForInputQuestion(t *testing.T, store *session.Store, sid string, baseline int) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs, err := store.Events(sid, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs[baseline:] {
			if ev.Type == events.TypeInputRequest {
				return dataString(ev.Data, "id")
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no input.request appeared")
	return ""
}

func TestCloseStaleQuestionsKeepsLiveQuestionsAndClosesTheirCopies(t *testing.T) {
	loop, parent := scriptedLoop(t, &scriptedProvider{}, tools.NewRegistry(nil))
	broker := NewPermissionBroker(loop.Store)
	loop.PermissionShownIn = broker.ShownIn
	loop.Input = NewInputBroker(loop.Store)
	for _, s := range []struct{ id, parent string }{{"task", parent}, {"copy", ""}} {
		if _, err := loop.Store.CreateSession(s.id, s.parent, "general-purpose", s.parent == ""); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	permissionDone, inputDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(permissionDone)
		broker.Func()(WithSessionID(ctx, "task"), tools.Ask{Tool: "bash", Subject: "make", Description: "make"})
	}()
	go func() { defer close(inputDone); loop.Input.Ask(ctx, parent, "Which store?", nil) }()
	defer func() { cancel(); <-permissionDone; <-inputDone }()
	permissionID := waitForPermissionID(t, loop.Store, "task", 0)
	inputID := waitForInputQuestion(t, loop.Store, parent, 0)
	// The mirror is written before the task's own request. Both are live.
	if !broker.ShownIn(parent, permissionID) || !loop.Input.ShownIn(parent, inputID) {
		t.Fatal("request logged before its audience was registered")
	}
	evs, err := loop.Store.Events(parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if _, err := loop.Store.Append("copy", ev.Type, ev.Data); err != nil {
			t.Fatal(err)
		}
	}
	// Stale and live questions in the same log must be judged separately.
	for _, kind := range []events.Type{events.TypePermissionRequest, events.TypeInputRequest} {
		if _, err := loop.Store.Append(parent, kind, map[string]any{"id": "stale"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, sid := range []string{parent, "task", "copy"} {
		loop.CloseStaleQuestions(sid)
	}
	for _, sid := range []string{parent, "task", "copy"} {
		evs, err := loop.Store.Events(sid, 0)
		if err != nil {
			t.Fatal(err)
		}
		resolved := map[string]bool{}
		count := 0
		for _, ev := range evs {
			if ev.Type == events.TypePermissionResolved || ev.Type == events.TypeInputResolved {
				resolved[dataString(ev.Data, "id")] = true
				count++
			}
		}
		if sid == "copy" {
			if count != 2 || !resolved[permissionID] || !resolved[inputID] {
				t.Errorf("copy kept live ids from another session: %v", resolved)
			}
		} else {
			if resolved[permissionID] || resolved[inputID] {
				t.Errorf("%s closed a live question: %v", sid, resolved)
			}
			if sid == parent && (count != 2 || !resolved["stale"]) {
				t.Errorf("parent did not close its stale questions: %v", resolved)
			}
		}
	}
	if !broker.Resolve(permissionID, false, ScopeOnce) || !loop.Input.Resolve(inputID, "SQLite") {
		t.Fatal("rehydration made a live question unanswerable")
	}
	<-permissionDone
	<-inputDone
	if broker.ShownIn(parent, permissionID) || loop.Input.ShownIn(parent, inputID) {
		t.Fatal("resolved question still shown")
	}
	loop.Input.mu.Lock()
	_, shown := loop.Input.shownIn[inputID]
	loop.Input.mu.Unlock()
	if shown {
		t.Error("resolved input audience retained")
	}
}

// Rehydration only reads. A successor daemon rehydrates every session
// before it knows which ones the daemon it replaced is still writing, so
// a write from here lands in a log another process owns.
func TestRehydrationWritesNothing(t *testing.T) {
	loop, parent := scriptedLoop(t, &scriptedProvider{}, tools.NewRegistry(nil))
	loop.Input = NewInputBroker(loop.Store)
	for _, kind := range []events.Type{events.TypePermissionRequest, events.TypeInputRequest} {
		if _, err := loop.Store.Append(parent, kind, map[string]any{"id": string(kind)}); err != nil {
			t.Fatal(err)
		}
	}
	loop.RehydrateAll()
	loop.RehydrateSession(parent)
	if evs, _ := loop.Store.Events(parent, 0); len(evs) != 2 {
		t.Errorf("rehydration wrote to the log: %v", evs)
	}
}

func TestCloseStaleQuestionsSkipsAnsweredOrEmptyQuestions(t *testing.T) {
	loop, sid := scriptedLoop(t, &scriptedProvider{}, tools.NewRegistry(nil))
	for _, kind := range []events.Type{events.TypePermissionRequest, events.TypeInputRequest, events.TypePermissionResolved, events.TypeInputResolved} {
		if _, err := loop.Store.Append(sid, kind, map[string]any{"id": "answered"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []events.Type{events.TypePermissionRequest, events.TypeInputRequest} {
		if _, err := loop.Store.Append(sid, kind, map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := loop.Store.Events(sid, 0)
	loop.CloseStaleQuestions(sid)
	after, _ := loop.Store.Events(sid, 0)
	if len(after) != len(before) {
		t.Errorf("added events for answered or empty questions: %v", after[len(before):])
	}
}

// An id an earlier daemon reused in the same log (ids restarted at p1 in
// every daemon before v0.169.3) is closed once, and stale questions are
// closed in the order they were asked.
func TestCloseStaleQuestionsClosesAReusedIdOnceAndInOrder(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSession("s", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []struct {
		typ events.Type
		id  string
	}{
		{events.TypePermissionRequest, "p1"},
		{events.TypePermissionResolved, "p1"},
		{events.TypePermissionRequest, "p2"},
		{events.TypeInputRequest, "q1"},
		{events.TypePermissionRequest, "p1"},
	} {
		if _, err := store.Append("s", ev.typ, map[string]any{"id": ev.id}); err != nil {
			t.Fatal(err)
		}
	}
	loop, _ := scriptedLoop(t, &scriptedProvider{}, tools.NewRegistry(nil))
	loop.Store = store
	loop.Input = NewInputBroker(store)
	loop.CloseStaleQuestions("s")

	evs, _ := store.Events("s", 0)
	var closed []string
	for _, ev := range evs[5:] {
		closed = append(closed, string(ev.Type)+" "+dataString(ev.Data, "id"))
	}
	want := []string{"permission.resolved p2", "input.resolved q1", "permission.resolved p1"}
	if len(closed) != len(want) {
		t.Fatalf("closed %q, want %q", closed, want)
	}
	for i := range want {
		if closed[i] != want[i] {
			t.Fatalf("closed %q, want %q", closed, want)
		}
	}
}
