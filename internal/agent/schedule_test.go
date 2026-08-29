package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// scheduleLoop is a loop whose model answers once, so a fired task has
// something to have produced.
func scheduleLoop(t *testing.T) *Loop {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	p := &scriptedProvider{turns: [][]provider.StreamEvent{{
		{Type: provider.EventTextDelta, TextDelta: "did the thing"},
		{Type: provider.EventMessageStop, StopReason: "end_turn"},
	}}}
	return New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": p}, cfg)
}

func waitForStatus(t *testing.T, s *Scheduler, id, want string) Scheduled {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := s.Get(id); ok && e.Status == want {
			return e
		}
		time.Sleep(10 * time.Millisecond)
	}
	e, _ := s.Get(id)
	t.Fatalf("scheduled task never reached %q (it is %q: %s)", want, e.Status, e.Error)
	return Scheduled{}
}

// The whole of it: book a prompt, watch it run, read the answer where the
// panel says the answer is.
func TestAScheduledPromptRunsAndLeavesItsAnswerBehind(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	project := t.TempDir()
	if _, err := loop.Store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sched := NewScheduler(context.Background(), loop)

	entry, err := sched.Add(sid, "general-purpose", "run the tests", time.Now().Add(30*time.Millisecond))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if entry.Status != SchedulePending {
		t.Errorf("a booked task starts as %q, want %q", entry.Status, SchedulePending)
	}

	done := waitForStatus(t, sched, entry.ID, ScheduleDone)
	if done.RunSession == "" {
		t.Fatal("a finished task names no session, so there is nothing to click through to")
	}
	// The answer is in the run session, which is what the panel opens.
	if got := lastAssistantText(loop.Store, done.RunSession); !strings.Contains(got, "did the thing") {
		t.Errorf("the run session holds %q, want the model's answer", got)
	}
	// And it ran in the conversation's project, not wherever the daemon
	// happens to have been started.
	if got := loop.SessionDir(done.RunSession); got != project {
		t.Errorf("the scheduled work ran in %q, want the conversation's project %q", got, project)
	}
}

// The row in the panel is built from the conversation's own log, which is
// what makes it survive a reload — the same reason a background task's
// row is built from task.spawned rather than from memory.
func TestBookingAndFinishingAreRecordedOnTheConversation(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sched := NewScheduler(context.Background(), loop)
	entry, err := sched.Add(sid, "general-purpose", "run the tests", time.Now().Add(30*time.Millisecond))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	waitForStatus(t, sched, entry.ID, ScheduleDone)

	seen := map[events.Type]bool{}
	for _, ev := range mustEvents(t, loop.Store, sid) {
		if id, _ := ev.Data["id"].(string); id == entry.ID {
			seen[ev.Type] = true
		}
	}
	for _, want := range []events.Type{events.TypeScheduleCreated, events.TypeScheduleStatus} {
		if !seen[want] {
			t.Errorf("no %s on the conversation's log, so the row would not come back on a reload", want)
		}
	}
}

// Cancelling takes the row with it, and records that where the row comes
// from so it does not reappear.
func TestCancellingAScheduledTaskStopsItAndRemovesTheRow(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sched := NewScheduler(context.Background(), loop)
	entry, err := sched.Add(sid, "general-purpose", "run the tests", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !sched.Cancel(entry.ID) {
		t.Fatal("Cancel reported there was nothing to cancel")
	}
	if _, ok := sched.Get(entry.ID); ok {
		t.Error("a cancelled task is still in the books")
	}
	removed := false
	for _, ev := range mustEvents(t, loop.Store, sid) {
		if ev.Type == events.TypeScheduleRemoved {
			removed = true
		}
	}
	if !removed {
		t.Error("the removal was not recorded, so the row comes back on the next reload")
	}
	// And it does not fire afterwards. Nothing to wait for; the proof is
	// that the entry is gone and its timer was stopped with it.
	if len(loop.Store.Children(sid)) != 0 {
		t.Error("a cancelled task created a run session anyway")
	}
}

// The three states of the row's light: blinking while it waits, solid
// once there is something to read, grey once it has been read.
func TestSeenIsTheThirdState(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sched := NewScheduler(context.Background(), loop)
	entry, _ := sched.Add(sid, "general-purpose", "run the tests", time.Now().Add(30*time.Millisecond))
	done := waitForStatus(t, sched, entry.ID, ScheduleDone)
	if done.Seen {
		t.Error("a task that has just finished is already marked read")
	}
	if !sched.MarkSeen(entry.ID) {
		t.Fatal("MarkSeen found nothing to mark")
	}
	if e, _ := sched.Get(entry.ID); !e.Seen {
		t.Error("reading the result did not stick")
	}
	if sched.MarkSeen(entry.ID) {
		t.Error("marking it read twice reported a second change, so clients would see a redundant event")
	}
}

// A moment that passed while localcode was not running is missed, in
// those words, and is not fired late.
//
// This is the promise the feature makes and the one it must not quietly
// break: running "summarize yesterday's commits" at four in the afternoon
// because the machine was asleep at nine would be doing something nobody
// asked for, at a moment they did not choose.
func TestAMomentThatPassedWhileClosedIsMissedRatherThanRunLate(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	// A conversation whose log already carries a booking, the way it
	// would after a restart.
	past := time.Now().Add(-2 * time.Hour)
	if _, err := loop.Store.Append(sid, events.TypeScheduleCreated, map[string]any{
		"id": "sched-old", "at": past.Format(time.RFC3339),
		"prompt": "summarize yesterday", "agent": "general-purpose",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	sched := NewScheduler(context.Background(), loop)
	sched.Restore([]string{sid}, time.Now())

	e, ok := sched.Get("sched-old")
	if !ok {
		t.Fatal("the booking was not restored at all, so the row would vanish on a restart")
	}
	if e.Status != ScheduleMissed {
		t.Errorf("status = %q, want %q", e.Status, ScheduleMissed)
	}
	if len(loop.Store.Children(sid)) != 0 {
		t.Error("a missed task was run late; the request was for a time, not for eventually")
	}
	if !strings.Contains(e.Error, "not running") {
		t.Errorf("the reason given is %q, which does not say why it did not happen", e.Error)
	}
}

// One still ahead of us is re-armed, because that is simply the daemon
// being alive before the moment comes.
func TestAFutureBookingSurvivesARestart(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	at := time.Now().Add(40 * time.Millisecond)
	loop.Store.Append(sid, events.TypeScheduleCreated, map[string]any{
		"id": "sched-soon", "at": at.Format(time.RFC3339Nano),
		"prompt": "run the tests", "agent": "general-purpose",
	})
	sched := NewScheduler(context.Background(), loop)
	sched.Restore([]string{sid}, time.Now())

	waitForStatus(t, sched, "sched-soon", ScheduleDone)
}

// A permission request has no timeout, so an unattended turn that hits
// one used to block on a channel nothing would ever send to: the session
// stayed busy and the work was found in the morning not done. It now
// waits a bounded time and then stops, saying what it wanted.
func TestAnUnattendedTurnDoesNotWaitForeverForPermission(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	broker := NewPermissionBroker(store)

	// Short enough to test, long enough that the real one gives somebody
	// at the desk a chance: see unattendedPermissionWait.
	restore := unattendedWaitForTest(50 * time.Millisecond)
	defer restore()

	ctx := WithUnattended(WithSessionID(context.Background(), "s1"))
	done := make(chan error, 1)
	go func() {
		_, err := broker.Func()(ctx, tools.Ask{
			Tool: "write_file", Subject: "/tmp/x", Description: "write /tmp/x",
		})
		done <- err
	}()

	select {
	case err := <-done:
		var refused *tools.RefusedError
		if !errorsAs(err, &refused) {
			t.Fatalf("err = %v, want a refusal that carries its reason", err)
		}
		if !strings.Contains(refused.Reason, "nobody answered") {
			t.Errorf("the reason is %q, which does not say why the tool did not run", refused.Reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an unattended turn waited forever for a permission answer")
	}

	// And the question is taken off screen, so a mirrored prompt does not
	// sit in the conversation refusing every message.
	resolved := false
	for _, ev := range mustEvents(t, store, "s1") {
		if ev.Type == events.TypePermissionResolved {
			resolved = true
		}
	}
	if !resolved {
		t.Error("the request was left open, so the modal asking it never closes")
	}
}

// An attended turn is unaffected: it waits, because somebody is there.
func TestAnAttendedTurnStillWaits(t *testing.T) {
	store, _ := session.NewStore("")
	store.CreateSession("s1", "", "general-purpose", true)
	broker := NewPermissionBroker(store)
	restore := unattendedWaitForTest(20 * time.Millisecond)
	defer restore()

	ctx := WithSessionID(context.Background(), "s1")
	done := make(chan bool, 1)
	go func() {
		ok, _ := broker.Func()(ctx, tools.Ask{Tool: "write_file", Subject: "/tmp/x", Description: "write /tmp/x"})
		done <- ok
	}()
	id := waitForPermissionID(t, store, "s1", 0)
	select {
	case <-done:
		t.Fatal("an attended turn gave up on its own; only an unattended one may")
	case <-time.After(100 * time.Millisecond):
	}
	broker.Resolve(id, true, ScopeOnce)
	if !<-done {
		t.Error("the answer did not reach the waiting call")
	}
}

// A booked prompt is not a way around the ceiling on unattended work.
func TestBookingIsBounded(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	loop.Store.CreateSession(sid, "", "general-purpose", true)
	sched := NewScheduler(context.Background(), loop)
	for i := 0; i < maxPendingPerSession; i++ {
		if _, err := sched.Add(sid, "general-purpose", "x", time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	if _, err := sched.Add(sid, "general-purpose", "x", time.Now().Add(time.Hour)); err == nil {
		t.Error("booking past the ceiling was allowed")
	}
}

// errorsAs is errors.As, kept local so this file's imports say what it
// actually uses.
func errorsAs(err error, target **tools.RefusedError) bool {
	for err != nil {
		if r, ok := err.(*tools.RefusedError); ok {
			*target = r
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
