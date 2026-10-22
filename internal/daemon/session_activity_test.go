package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/events"
	"localcode/internal/tools"
)

// activityFor waits for a session.activity about sessionID that want
// accepts, and fails the test if none comes.
func activityFor(t *testing.T, ch <-chan events.Event, sessionID string, want func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == events.TypeSessionActivity && ev.Data["session"] == sessionID && want(ev.Data) {
				return ev.Data
			}
		case <-deadline:
			t.Fatalf("no session.activity for %s of the expected shape", sessionID)
		}
	}
}

// listedRow is one row of GET /api/sessions, as a client reads it.
type listedRow struct {
	ID     string `json:"id"`
	Busy   bool   `json:"busy"`
	Asking bool   `json:"asking"`
	Tasks  int    `json:"tasks"`
}

func listedSession(t *testing.T, srvURL, id string) listedRow {
	t.Helper()
	resp, err := http.Get(srvURL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rows []listedRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("%s is not in the session list", id)
	return listedRow{}
}

// Reported from real use: a conversation whose turn had ended with a
// background task still working drew as finished, a steady light, because
// the list knew only about turns. A task it started keeps it working, in
// the list and in the live event, until the task has unwound.
func TestABackgroundTaskKeepsItsSessionWorking(t *testing.T) {
	model, _, release := heldModelServer(t)
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	announced, unsub := d.daemonEvents.subscribe()
	defer unsub()

	if _, err := d.Tasks.Spawn("parent", "build", "slow work"); err != nil {
		t.Fatal(err)
	}
	ev := activityFor(t, announced, "parent", func(d map[string]any) bool { return d["tasks"] == 1 })
	if ev["busy"] != false || ev["asking"] != false {
		t.Errorf("activity = %v, want a task running and nothing else", ev)
	}
	if row := listedSession(t, srv.URL, "parent"); row.Tasks != 1 || row.Busy {
		t.Errorf("listed as %+v, want one task running and no turn", row)
	}

	release()
	activityFor(t, announced, "parent", func(d map[string]any) bool { return d["tasks"] == 0 })
	if row := listedSession(t, srv.URL, "parent"); row.Tasks != 0 {
		t.Errorf("listed as %+v after the task finished", row)
	}
}

// The model asking the person a question stops the session for them as a
// permission request does, and the light for it is the same. It was not
// counted at all, so such a session drew as working.
func TestAQuestionForThePersonMarksItsSessionAsking(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	announced, unsub := d.daemonEvents.subscribe()
	defer unsub()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Loop.Input.Ask(ctx, "s1", "which one?", []string{"a", "b"})
	}()
	activityFor(t, announced, "s1", func(d map[string]any) bool { return d["asking"] == true })
	if row := listedSession(t, srv.URL, "s1"); !row.Asking {
		t.Errorf("listed as %+v while a question waits", row)
	}

	cancel()
	<-done
	activityFor(t, announced, "s1", func(d map[string]any) bool { return d["asking"] == false })
	if row := listedSession(t, srv.URL, "s1"); row.Asking {
		t.Errorf("listed as %+v after the question was withdrawn", row)
	}
}

// The daemon this one replaced names the sessions it is still writing,
// which for a task is the task's own. The conversation the task works for
// is working too, here and in the list, until the old daemon lets go.
func TestATaskStillRunningInTheOldDaemonKeepsItsConversationWorking(t *testing.T) {
	d, store, dir := handoffDaemon(t)
	if _, err := store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSessionIn("parent-task", "parent", "general-purpose", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSession("other", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, "parent-task")

	if a := d.activityOf("parent"); !a.Busy {
		t.Errorf("activityOf(parent) = %+v while the old daemon finishes its task", a)
	}
	if a := d.activityOf("other"); a.Busy {
		t.Errorf("activityOf(other) = %+v; nothing of it is in the old daemon", a)
	}
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	if row := listedSession(t, srv.URL, "parent"); !row.Busy {
		t.Errorf("listed as %+v while the old daemon finishes its task", row)
	}
}

// A permission request is a session waiting for a person, announced as
// it is raised and again when it ends however it ends.
func TestAPermissionRequestIsAnnouncedAsWaiting(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	announced, unsub := d.daemonEvents.subscribe()
	defer unsub()

	ctx, cancel := context.WithCancel(agent.WithSessionID(context.Background(), "s1"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = d.Broker.Func()(ctx, tools.Ask{Tool: "bash", Description: "run a command", Subject: "make"})
	}()
	activityFor(t, announced, "s1", func(d map[string]any) bool { return d["asking"] == true })
	cancel()
	<-done
	activityFor(t, announced, "s1", func(d map[string]any) bool { return d["asking"] == false })
}
