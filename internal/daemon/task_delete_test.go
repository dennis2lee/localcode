package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"localcode/internal/events"
)

// Removing rows from a conversation's background task panel: one, or all
// of them. See tasks_delete.go.

func deleteRequest(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func rowsOf(t *testing.T, d *Daemon, parentID string) []string {
	t.Helper()
	evs, err := d.Loop.Store.Events(parentID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return taskRows(evs)
}

func eventCount(t *testing.T, d *Daemon, id string) int {
	t.Helper()
	evs, err := d.Loop.Store.Events(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(evs)
}

// finishedTask spawns a background task under parent and waits for it to
// report how it ended.
func finishedTask(t *testing.T, d *Daemon, parent string) string {
	t.Helper()
	id, err := d.Tasks.Spawn(parent, "build", "quick")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		evs, _ := d.Loop.Store.Events(parent, 0)
		for _, ev := range evs {
			if got, _ := ev.Data["task_id"].(string); ev.Type == events.TypeTaskStatus && got == id {
				if s, _ := ev.Data["status"].(string); s == "completed" || s == "failed" {
					return true
				}
			}
		}
		return false
	})
	return id
}

// nothingAfterDeleted fails if anything about taskID reached the parent's
// log after the record that removed its row: a status written behind it
// brings the row back on every client and on every reload.
func nothingAfterDeleted(t *testing.T, d *Daemon, parent, taskID string) {
	t.Helper()
	evs, err := d.Loop.Store.Events(parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	deleted := false
	for _, ev := range evs {
		if got, _ := ev.Data["task_id"].(string); got != taskID {
			continue
		}
		status, _ := ev.Data["status"].(string)
		if deleted && ev.Type == events.TypeTaskStatus {
			t.Errorf("%s: status %q written after the row was removed", taskID, status)
		}
		if status == "deleted" {
			deleted = true
		}
	}
	if !deleted {
		t.Errorf("%s: no record that its row was removed", taskID)
	}
}

func TestDeletingARunningTaskStopsItAndTakesItsRowAway(t *testing.T) {
	model, calls, release := heldModelServer(t)
	defer model.Close()
	defer release()
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	child, err := d.Tasks.Spawn("parent", "build", "work on this")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return calls.Load() > 0 })

	if code, body := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks/"+child); code != http.StatusNoContent {
		t.Fatalf("status %d (%s), want 204", code, body)
	}
	// Stopped and waited for before the answer, not left to finish into
	// a log that is gone.
	if running := d.Tasks.RunningIn([]string{child}); len(running) != 0 {
		t.Error("the task was still running when the delete answered")
	}
	if _, err := d.Loop.Store.Get(child); err == nil {
		t.Error("the task's conversation is still there")
	}
	if _, err := d.Loop.Store.Get("parent"); err != nil {
		t.Error("deleting a task deleted the conversation that started it")
	}
	if rows := rowsOf(t, d, "parent"); len(rows) != 0 {
		t.Errorf("rows %v, want none", rows)
	}
	nothingAfterDeleted(t, d, "parent", child)

	before := calls.Load()
	release()
	time.Sleep(100 * time.Millisecond)
	if after := calls.Load(); after != before {
		t.Errorf("the deleted task made %d more model calls", after-before)
	}
	// Nor, with the model released, does it say how it ended behind the
	// record that removed its row.
	nothingAfterDeleted(t, d, "parent", child)
}

func TestDeletingOneTaskLeavesTheOthers(t *testing.T) {
	model, _, release := heldModelServer(t)
	defer model.Close()
	release()
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	for _, id := range []string{"parent", "other"} {
		if _, err := d.Loop.Store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	a, b := finishedTask(t, d, "parent"), finishedTask(t, d, "parent")
	elsewhere := finishedTask(t, d, "other")
	announced, unsub := d.daemonEvents.subscribe()
	defer unsub()

	if code, body := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks/"+a); code != http.StatusNoContent {
		t.Fatalf("status %d (%s)", code, body)
	}
	// Every client is told the session is gone, as for any other delete.
	var gone []string
	for len(gone) == 0 {
		select {
		case ev := <-announced:
			if ev.Type == events.TypeSessionDeleted {
				id, _ := ev.Data["session"].(string)
				gone = append(gone, id)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the deletion was never announced")
		}
	}
	if !reflect.DeepEqual(gone, []string{a}) {
		t.Errorf("announced %v as deleted, want %s", gone, a)
	}
	if rows := rowsOf(t, d, "parent"); !reflect.DeepEqual(rows, []string{b}) {
		t.Errorf("rows %v, want only %s", rows, b)
	}
	for _, id := range []string{b, elsewhere} {
		if _, err := d.Loop.Store.Get(id); err != nil {
			t.Errorf("%s was deleted with %s", id, a)
		}
	}
}

// Another conversation's task is not this panel's to remove, and its
// session is not this conversation's to delete.
func TestAnotherConversationsTaskIsNotDeletedThroughThisOne(t *testing.T) {
	model, _, release := heldModelServer(t)
	defer model.Close()
	release()
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	for _, id := range []string{"parent", "other"} {
		if _, err := d.Loop.Store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	theirs := finishedTask(t, d, "other")
	before := eventCount(t, d, "parent")

	for _, id := range []string{theirs, "other", "no-such-task"} {
		if code, _ := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks/"+id); code != http.StatusNotFound {
			t.Errorf("deleting %s through parent: status %d, want 404", id, code)
		}
	}
	for _, id := range []string{theirs, "other"} {
		if _, err := d.Loop.Store.Get(id); err != nil {
			t.Errorf("%s was deleted through a conversation that does not own it", id)
		}
	}
	if after := eventCount(t, d, "parent"); after != before {
		t.Errorf("%d records written to parent for tasks it does not have", after-before)
	}
	if code, _ := deleteRequest(t, srv.URL+"/api/sessions/nobody/tasks/"+theirs); code != http.StatusNotFound {
		t.Errorf("an unknown conversation: status %d, want 404", code)
	}
}

// A row can outlive its session: the panel is built from the parent's
// log. Deleting by session id could never remove such a row.
func TestARowWhoseSessionIsGoneCanStillBeDeleted(t *testing.T) {
	d, srv, done := plainDaemon(t)
	defer done()
	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	d.Loop.Store.Append("parent", events.TypeTaskSpawned, map[string]any{"task_id": "task-ghost", "agent": "build"})
	d.Loop.Store.Append("parent", events.TypeTaskStatus, map[string]any{"task_id": "task-ghost", "status": "completed"})

	if code, body := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks/task-ghost"); code != http.StatusNoContent {
		t.Fatalf("status %d (%s), want 204", code, body)
	}
	if rows := rowsOf(t, d, "parent"); len(rows) != 0 {
		t.Errorf("rows %v, want none", rows)
	}
	// Once gone, it is not a row any more.
	if code, _ := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks/task-ghost"); code != http.StatusNotFound {
		t.Errorf("a second delete: status %d, want 404", code)
	}
}

// A row naming a session some other conversation started is this panel's
// to remove, and the session is not this conversation's to delete.
func TestARowNamingAnotherConversationsSessionLeavesTheSession(t *testing.T) {
	d, srv, done := plainDaemon(t)
	defer done()
	for _, id := range []string{"parent", "other"} {
		if _, err := d.Loop.Store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Loop.Store.CreateSessionIn("task-x", "other", "build", "", false); err != nil {
		t.Fatal(err)
	}
	d.Loop.Store.Append("parent", events.TypeTaskSpawned, map[string]any{"task_id": "task-x", "agent": "build"})

	for _, path := range []string{"/api/sessions/parent/tasks/task-x", "/api/sessions/parent/tasks"} {
		d.Loop.Store.Append("parent", events.TypeTaskSpawned, map[string]any{"task_id": "task-x", "agent": "build"})
		if code, body := deleteRequest(t, srv.URL+path); code != http.StatusNoContent && code != http.StatusOK {
			t.Fatalf("%s: status %d (%s)", path, code, body)
		}
		if _, err := d.Loop.Store.Get("task-x"); err != nil {
			t.Fatalf("%s deleted another conversation's session", path)
		}
		if rows := rowsOf(t, d, "parent"); len(rows) != 0 {
			t.Errorf("%s: rows %v, want none", path, rows)
		}
	}
}

func TestDeleteAllEmptiesThePanelAndLeavesTheConversation(t *testing.T) {
	model, calls, release := heldModelServer(t)
	defer model.Close()
	defer release()
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	for _, id := range []string{"parent", "other"} {
		if _, err := d.Loop.Store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	// A running task, a finished one with a task of its own, a row whose
	// session is gone, and a stage of an orchestration, which is a line in
	// the transcript rather than a row.
	running, err := d.Tasks.Spawn("parent", "build", "work")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return calls.Load() > 0 })
	finished := "task-finished"
	if _, err := d.Loop.Store.CreateSessionIn(finished, "parent", "build", "", false); err != nil {
		t.Fatal(err)
	}
	d.Loop.Store.Append("parent", events.TypeTaskSpawned, map[string]any{"task_id": finished, "agent": "build"})
	d.Loop.Store.Append("parent", events.TypeTaskStatus, map[string]any{"task_id": finished, "status": "completed"})
	if _, err := d.Loop.Store.CreateSessionIn("task-grandchild", finished, "explore", "", false); err != nil {
		t.Fatal(err)
	}
	d.Loop.Store.Append("parent", events.TypeTaskSpawned, map[string]any{"task_id": "task-ghost", "agent": "build"})
	d.Loop.Store.Append("parent", events.TypeTaskStatus, map[string]any{"task_id": "orchestrate:plan", "status": "running"})
	theirs, err := d.Loop.Store.CreateSessionIn("task-theirs", "other", "build", "", false)
	if err != nil {
		t.Fatal(err)
	}
	// A scheduled run is a child session of the conversation too, with no
	// row in this panel and a row of its own in the scheduled tasks panel.
	if _, err := d.Loop.Store.CreateSessionIn("run-1", "parent", "general-purpose", "", false); err != nil {
		t.Fatal(err)
	}

	code, body := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks")
	if code != http.StatusOK {
		t.Fatalf("status %d (%s), want 200", code, body)
	}
	var got struct {
		Deleted int `json:"deleted"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Deleted != 3 {
		t.Errorf("answer %s, want 3 deleted", body)
	}
	for _, id := range []string{running, finished, "task-grandchild"} {
		if _, err := d.Loop.Store.Get(id); err == nil {
			t.Errorf("%s survived delete all", id)
		}
	}
	for _, id := range []string{"parent", "other", theirs.ID, "run-1"} {
		if _, err := d.Loop.Store.Get(id); err != nil {
			t.Errorf("%s was deleted, and it is not a row of this panel", id)
		}
	}
	if code, _ := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks/run-1"); code != http.StatusNotFound {
		t.Errorf("a scheduled run deleted as a task: status %d, want 404", code)
	}
	if rows := rowsOf(t, d, "parent"); len(rows) != 0 {
		t.Errorf("rows %v, want none", rows)
	}
	for _, id := range []string{running, finished, "task-ghost"} {
		nothingAfterDeleted(t, d, "parent", id)
	}
}

func TestDeleteAllWithNothingToDelete(t *testing.T) {
	d, srv, done := plainDaemon(t)
	defer done()
	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	before := eventCount(t, d, "parent")
	code, body := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks")
	if code != http.StatusOK || body == "" {
		t.Fatalf("status %d (%s), want 200", code, body)
	}
	if after := eventCount(t, d, "parent"); after != before {
		t.Errorf("%d records written for a panel with no rows", after-before)
	}
	if code, _ := deleteRequest(t, srv.URL+"/api/sessions/nobody/tasks"); code != http.StatusNotFound {
		t.Errorf("an unknown conversation: status %d, want 404", code)
	}
}

// A turn somebody started in a task's own conversation is writing to it,
// and a delete all that removed the other rows and refused this one would
// leave a panel describing neither.
func TestDeleteAllRefusesWhileATaskHasATurnAndDeletesNothing(t *testing.T) {
	d, srv, done := plainDaemon(t)
	defer done()
	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"task-a", "task-b"} {
		if _, err := d.Loop.Store.CreateSessionIn(id, "parent", "build", "", false); err != nil {
			t.Fatal(err)
		}
		d.Loop.Store.Append("parent", events.TypeTaskSpawned, map[string]any{"task_id": id, "agent": "build"})
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !d.turns.begin("task-b", cancel) {
		t.Fatal("could not hold task-b's turn")
	}

	if code, _ := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks"); code != http.StatusConflict {
		t.Errorf("status %d, want 409", code)
	}
	if code, _ := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks/task-b"); code != http.StatusConflict {
		t.Errorf("one task: status %d, want 409", code)
	}
	for _, id := range []string{"task-a", "task-b"} {
		if _, err := d.Loop.Store.Get(id); err != nil {
			t.Errorf("%s was deleted by a refused delete", id)
		}
	}
	if rows := rowsOf(t, d, "parent"); len(rows) != 2 {
		t.Errorf("rows %v, want both", rows)
	}
	// And the slot it took for task-a is given back.
	d.turns.end("task-b")
	if code, _ := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks"); code != http.StatusOK {
		t.Errorf("after the turn: status %d, want 200", code)
	}
}

// The rows, as the clients work them out.
func TestTaskRowsFollowTheLog(t *testing.T) {
	ev := func(typ events.Type, id, status string) events.Event {
		data := map[string]any{"task_id": id}
		if status != "" {
			data["status"] = status
		}
		return events.Event{Type: typ, Data: data}
	}
	got := taskRows([]events.Event{
		ev(events.TypeTaskSpawned, "a", ""),
		ev(events.TypeTaskSpawned, "b", ""),
		ev(events.TypeTaskStatus, "c", "running"), // a status alone names a row
		ev(events.TypeTaskStatus, "orchestrate:plan", "running"),
		ev(events.TypeTaskStatus, "a", "deleted"),
		ev(events.TypeTaskStatus, "b", "deleted"),
		ev(events.TypeTaskStatus, "b", "completed"), // after deleted: back, at the end
		ev(events.TypeUserMessage, "d", ""),
		ev(events.TypeTaskSpawned, "", ""),
	})
	if want := []string{"c", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("rows %v, want %v", got, want)
	}
}
