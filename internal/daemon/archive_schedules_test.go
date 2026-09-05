package daemon

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/events"
)

// Work booked for later, in a conversation that is then put away.
//
// Archiving does not touch the timers. A booking has never run a turn in
// an archived conversation — Scheduler.fire looks at the parent and marks
// the row missed on sight — so an armed timer on the shelf is harmless,
// and it is what writes that row at the moment it was booked for.
func TestArchivingLeavesBookedWorkArmed(t *testing.T) {
	d, srv := archiveDaemon(t)
	d.Loop.Schedules = agent.NewScheduler(context.Background(), d.Loop)

	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Loop.Schedules.Add("s1", "general-purpose", "summarise the week", time.Now().Add(72*time.Hour), agent.RepeatOptions{}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if resp := post(t, srv, "/api/sessions/s1/archive", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("archive: %d", resp.StatusCode)
	}
	if n := len(d.Loop.Schedules.List("s1")); n != 1 {
		t.Errorf("archiving dropped the booking: %d rows, want 1", n)
	}
}

// What a restart does now: startup skips the shelf, so a conversation
// archived before it comes back with nothing armed. Retrieving is what
// rebuilds the rows, and it does it for the whole tree, because a
// background task can book work of its own under its own session id.
func TestRetrievingRebuildsTheBooksForTheWholeTree(t *testing.T) {
	d, srv := archiveDaemon(t)
	d.Loop.Schedules = agent.NewScheduler(context.Background(), d.Loop)

	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Loop.Store.CreateSession("s1-task", "s1", "general-purpose", false); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s1", "s1-task"} {
		if _, err := d.Loop.Schedules.Add(id, "general-purpose", "later, in "+id, time.Now().Add(72*time.Hour), agent.RepeatOptions{}); err != nil {
			t.Fatalf("Add %s: %v", id, err)
		}
	}
	if resp := post(t, srv, "/api/sessions/s1/archive", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("archive: %d", resp.StatusCode)
	}

	// A restart, as far as the scheduler is concerned: the rows are in
	// the logs and nothing is in memory.
	d.Loop.Schedules.ForgetSession("s1")
	d.Loop.Schedules.ForgetSession("s1-task")

	if resp := post(t, srv, "/api/sessions/s1/retrieve", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("retrieve: %d", resp.StatusCode)
	}
	for _, id := range []string{"s1", "s1-task"} {
		rows := d.Loop.Schedules.List(id)
		if len(rows) != 1 {
			t.Errorf("%s came back with %d booking(s), want 1", id, len(rows))
			continue
		}
		if rows[0].Prompt != "later, in "+id {
			t.Errorf("%s came back with the wrong row: %q", id, rows[0].Prompt)
		}
	}
}

// A moment that passed while the conversation was on the shelf is marked
// missed when it comes back — and the reason says so. The startup path's
// sentence, "localcode was not running", is a claim about the machine
// that is false in exactly this case.
func TestABookingMissedOnTheShelfSaysWhy(t *testing.T) {
	d, srv := archiveDaemon(t)
	d.Loop.Schedules = agent.NewScheduler(context.Background(), d.Loop)

	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	// Booked for a moment that has already gone. Add refuses the past, so
	// the row goes into the log the way a restore reads it.
	if _, err := d.Loop.Store.Append("s1", events.TypeScheduleCreated, map[string]any{
		"id": "s1", "at": time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
		"prompt": "summarise the week", "agent": "general-purpose",
	}); err != nil {
		t.Fatal(err)
	}
	if resp := post(t, srv, "/api/sessions/s1/archive", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("archive: %d", resp.StatusCode)
	}
	if resp := post(t, srv, "/api/sessions/s1/retrieve", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("retrieve: %d", resp.StatusCode)
	}

	rows := d.Loop.Schedules.List("s1")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Status != agent.ScheduleMissed {
		t.Fatalf("status = %q, want missed", rows[0].Status)
	}
	if strings.Contains(rows[0].Error, "localcode was not running") {
		t.Errorf("a booking missed on the shelf blames the machine: %q", rows[0].Error)
	}
	if !strings.Contains(rows[0].Error, "archived") {
		t.Errorf("the reason does not say the conversation was archived: %q", rows[0].Error)
	}
}
