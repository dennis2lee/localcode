package daemon

import (
	"context"
	"net/http"
	"testing"

	"localcode/internal/agent"
	"localcode/internal/events"
)

// The window's Schedule dialog books a prompt a person filled in, so it
// runs as they wrote it, and the booking says so on the conversation's
// log, which is what a restart reads it back from. See
// agent.Scheduler.AddByModel for the other kind.
func TestTheWindowsBookingIsRecordedAsAPersons(t *testing.T) {
	d, srv := archiveDaemon(t)
	d.Loop.Schedules = agent.NewScheduler(context.Background(), d.Loop)
	if _, err := d.Loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, e := range d.Loop.Schedules.List("s1") {
			d.Loop.Schedules.Cancel("s1", e.ID)
		}
	})

	resp := post(t, srv, "/api/sessions/s1/schedules", `{"when":"in 1 hour","prompt":"!echo from the dialog"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("book: %d", resp.StatusCode)
	}

	evs, err := d.Loop.Store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Type != events.TypeScheduleCreated {
			continue
		}
		found = true
		if by := ev.Data["by"]; by != "person" {
			t.Errorf("the dialog's booking records by=%v, want \"person\"", by)
		}
	}
	if !found {
		t.Fatal("no schedule.created on the conversation's log")
	}
}
