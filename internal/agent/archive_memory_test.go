package agent

import (
	"strings"
	"testing"
	"time"
)

// Archiving frees what the process was holding for a conversation, and
// that is only safe because retrieving replays it from the event log.
//
// Deliberately not ClearSessionState, which the delete path uses: that also
// calls Tasks.forgetSession, dropping the answers of background tasks that
// finished and were never collected. Those exist nowhere else, and
// archiving is reversible, so it must not be the thing that loses them.
func TestArchivingReleasesHistoryAndRetrievingRebuildsIt(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(t.Context(), "s1", "general-purpose", "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	before := len(loop.history("s1"))
	if before == 0 {
		t.Fatal("no history to release, so this test would prove nothing")
	}

	loop.ReleaseSessionMemory("s1")
	if got := len(loop.history("s1")); got != 0 {
		t.Errorf("release left %d messages", got)
	}

	loop.RehydrateSession("s1")
	if got := len(loop.history("s1")); got != before {
		t.Errorf("after rehydrate the history is %d, was %d", got, before)
	}
}

// The uncollected answer of a finished background task is the thing
// ClearSessionState drops and this must not. Archiving is reversible, and
// an answer that exists nowhere else is not something a reversible action
// may lose.
func TestReleasingMemoryKeepsUncollectedTaskAnswers(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	tm := NewTaskManager(t.Context(), loop, 2)
	loop.Store.CreateSession("s1", "", "general-purpose", true)

	id, err := tm.SpawnBackground(t.Context(), "s1", "general-purpose", "do a thing", "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// Finished, and deliberately not collected: Wait consumes the answer,
	// so calling it here would be the test destroying what it is about to
	// check for.
	recorded := func() bool {
		tm.mu.Lock()
		defer tm.mu.Unlock()
		_, ok := tm.results[id]
		return ok
	}
	for range 200 {
		if recorded() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !recorded() {
		t.Fatal("the task never recorded an answer, so this test would prove nothing")
	}

	loop.ReleaseSessionMemory("s1")
	if !recorded() {
		t.Fatal("archiving discarded a finished task's answer that nobody had collected")
	}

	// And the delete path still does drop it, which is why these are two
	// functions and not one.
	loop.ClearSessionState("s1")
	if recorded() {
		t.Error("ClearSessionState no longer forgets the session's tasks")
	}
}

// A prompt booked into a conversation that is archived before it comes
// round is reported missed, not failed and not fired.
//
// Missed is the honest word: the moment passed with the work not done, and
// nothing went wrong. Retrieving does not re-run it, for the reason a
// missed schedule is never fired late.
func TestASchedulePointingAtAnArchivedConversationIsMissed(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	sched := NewScheduler(t.Context(), loop)
	loop.Schedules = sched
	loop.Store.CreateSession("s1", "", "general-purpose", true)

	booked, err := sched.Add("s1", "general-purpose", "run the tests", time.Now().Add(50*time.Millisecond), RepeatOptions{})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if _, err := loop.Store.Archive("s1"); err != nil {
		t.Fatal(err)
	}

	var got string
	for range 200 {
		for _, e := range sched.List("s1") {
			if e.ID == booked.ID {
				got = e.Status
			}
		}
		if got == ScheduleMissed || got == ScheduleFailed || got == ScheduleDone {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got != ScheduleMissed {
		t.Errorf("status = %q, want %q", got, ScheduleMissed)
	}
}

// And booking into one that is already archived is refused at the source,
// so the endpoint and the scheduler cannot disagree.
func TestBookingIntoAnArchivedConversationIsRefused(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	sched := NewScheduler(t.Context(), loop)
	loop.Store.CreateSession("s1", "", "general-purpose", true)
	loop.Store.Archive("s1")

	if _, err := sched.Add("s1", "general-purpose", "run the tests", time.Now().Add(time.Hour), RepeatOptions{}); err == nil {
		t.Error("a prompt was booked into an archived conversation")
	} else if !strings.Contains(err.Error(), "archived") {
		t.Errorf("error = %q, which does not say why", err)
	}
}
