package agent

import (
	"context"
	"encoding/json"
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

func waitForStatus(t *testing.T, s *Scheduler, sessionID, id, want string) Scheduled {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := s.Get(sessionID, id); ok && e.Status == want {
			return e
		}
		time.Sleep(10 * time.Millisecond)
	}
	e, _ := s.Get(sessionID, id)
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

	entry, err := sched.Add(sid, "general-purpose", "run the tests", time.Now().Add(30*time.Millisecond), RepeatOptions{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if entry.Status != SchedulePending {
		t.Errorf("a booked task starts as %q, want %q", entry.Status, SchedulePending)
	}

	done := waitForStatus(t, sched, sid, entry.ID, ScheduleDone)
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
	entry, err := sched.Add(sid, "general-purpose", "run the tests", time.Now().Add(30*time.Millisecond), RepeatOptions{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	waitForStatus(t, sched, sid, entry.ID, ScheduleDone)

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
	entry, err := sched.Add(sid, "general-purpose", "run the tests", time.Now().Add(time.Hour), RepeatOptions{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !sched.Cancel(sid, entry.ID) {
		t.Fatal("Cancel reported there was nothing to cancel")
	}
	if _, ok := sched.Get(sid, entry.ID); ok {
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
	entry, _ := sched.Add(sid, "general-purpose", "run the tests", time.Now().Add(30*time.Millisecond), RepeatOptions{})
	done := waitForStatus(t, sched, sid, entry.ID, ScheduleDone)
	if done.Seen {
		t.Error("a task that has just finished is already marked read")
	}
	if !sched.MarkSeen(sid, entry.ID) {
		t.Fatal("MarkSeen found nothing to mark")
	}
	if e, _ := sched.Get(sid, entry.ID); !e.Seen {
		t.Error("reading the result did not stick")
	}
	if sched.MarkSeen(sid, entry.ID) {
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

	e, ok := sched.Get(sid, "sched-old")
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

	waitForStatus(t, sched, sid, "sched-soon", ScheduleDone)
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
	restore := SetUnattendedWait(50 * time.Millisecond)
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
		// What it wanted, and the fact that is true of every unattended
		// turn rather than of one of the two kinds. The sentence used to
		// call every such turn "scheduled work", which a one-shot run in
		// a pipe is not.
		for _, want := range []string{"not run", "write /tmp/x", "nobody is watching this turn"} {
			if !strings.Contains(refused.Reason, want) {
				t.Errorf("the reason is %q, which does not say %q", refused.Reason, want)
			}
		}
		if strings.Contains(refused.Reason, "scheduled") {
			t.Errorf("the reason calls this scheduled work: %q", refused.Reason)
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
	restore := SetUnattendedWait(20 * time.Millisecond)
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
		if _, err := sched.Add(sid, "general-purpose", "x", time.Now().Add(time.Hour), RepeatOptions{}); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	if _, err := sched.Add(sid, "general-purpose", "x", time.Now().Add(time.Hour), RepeatOptions{}); err == nil {
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

// A booked prompt is a paragraph and a row is one truncated line, so a
// task can be given a name. Cosmetic, like a session's title: nothing
// resolves by it, and the prompt stays visible underneath.
func TestRenamingAScheduledTask(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sched := NewScheduler(context.Background(), loop)
	entry, err := sched.Add(sid, "general-purpose", "run the tests and report the failures", time.Now().Add(time.Hour), RepeatOptions{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if entry.Name != "" {
		t.Errorf("a new task is named %q; it should have none until somebody gives it one", entry.Name)
	}

	got, ok := sched.Rename(sid, entry.ID, "  nightly tests  ")
	if !ok {
		t.Fatal("Rename found nothing to rename")
	}
	if got.Name != "nightly tests" {
		t.Errorf("name = %q, want it trimmed", got.Name)
	}
	if got.Prompt != "run the tests and report the failures" {
		t.Error("naming a task changed what it will run")
	}
	// Recorded where the row comes from, so it survives a reload and
	// reaches a second window.
	renamed := false
	for _, ev := range mustEvents(t, loop.Store, sid) {
		if ev.Type == events.TypeScheduleRenamed {
			renamed = true
		}
	}
	if !renamed {
		t.Error("the rename was not recorded, so it would vanish on a reload")
	}

	// An empty name clears it and the row goes back to the prompt.
	if got, _ := sched.Rename(sid, entry.ID, "   "); got.Name != "" {
		t.Errorf("clearing left %q", got.Name)
	}
	if _, ok := sched.Rename(sid, "no-such-task", "x"); ok {
		t.Error("renaming a task that does not exist reported success")
	}
}

// A name is a row's label, and a row is one line. A prompt pasted into
// the name field is truncated rather than allowed to push the panel
// around.
func TestAnOverlongNameIsCutToWhatARowCanShow(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	loop.Store.CreateSession(sid, "", "general-purpose", true)
	sched := NewScheduler(context.Background(), loop)
	entry, _ := sched.Add(sid, "general-purpose", "x", time.Now().Add(time.Hour), RepeatOptions{})

	got, _ := sched.Rename(sid, entry.ID, strings.Repeat("가", 300))
	if n := len([]rune(got.Name)); n > maxScheduleName {
		t.Errorf("name kept %d characters, want at most %d", n, maxScheduleName)
	}
}

// And it comes back after a restart, along with everything else the row
// is built from.
func TestANameSurvivesARestart(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	loop.Store.CreateSession(sid, "", "general-purpose", true)
	at := time.Now().Add(time.Hour)
	loop.Store.Append(sid, events.TypeScheduleCreated, map[string]any{
		"id": "sched-x", "at": at.Format(time.RFC3339Nano),
		"prompt": "run the tests", "agent": "general-purpose",
	})
	loop.Store.Append(sid, events.TypeScheduleRenamed, map[string]any{"id": "sched-x", "name": "nightly tests"})

	sched := NewScheduler(context.Background(), loop)
	sched.Restore([]string{sid}, time.Now())
	e, ok := sched.Get(sid, "sched-x")
	if !ok {
		t.Fatal("the booking was not restored")
	}
	if e.Name != "nightly tests" {
		t.Errorf("name after a restart = %q, want it back", e.Name)
	}
}

// The one place an id has to be typed is a TUI, and
// "sched-1788019200000000000-1" is not something anybody retypes to
// cancel a task. Ids are short and belong to their conversation.
func TestIdsAreShortAndPerConversation(t *testing.T) {
	loop := scheduleLoop(t)
	for _, id := range []string{"a", "b"} {
		if _, err := loop.Store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	sched := NewScheduler(context.Background(), loop)
	at := time.Now().Add(time.Hour)

	first, _ := sched.Add("a", "general-purpose", "one", at, RepeatOptions{})
	second, _ := sched.Add("a", "general-purpose", "two", at, RepeatOptions{})
	if first.ID != "s1" || second.ID != "s2" {
		t.Errorf("ids = %q, %q; want s1, s2", first.ID, second.ID)
	}
	// Each conversation counts for itself, so the first task in any
	// conversation is s1.
	other, _ := sched.Add("b", "general-purpose", "one", at, RepeatOptions{})
	if other.ID != "s1" {
		t.Errorf("the first task of another conversation is %q, want s1", other.ID)
	}

	// And "s1" means a different task in each of them.
	if e, _ := sched.Get("a", "s1"); e.Prompt != "one" {
		t.Errorf("a/s1 is %q", e.Prompt)
	}
	if !sched.Cancel("b", "s1") {
		t.Fatal("could not cancel b/s1")
	}
	if _, ok := sched.Get("a", "s1"); !ok {
		t.Error("cancelling one conversation's s1 took the other's with it")
	}

	// A number is never handed out twice, even after a deletion: a fresh
	// s1 where a cancelled s1 used to be is the one way a short id can
	// mislead.
	sched.Cancel("a", "s1")
	third, _ := sched.Add("a", "general-purpose", "three", at, RepeatOptions{})
	if third.ID != "s3" {
		t.Errorf("after cancelling s1, the next id is %q; want s3, not a reused number", third.ID)
	}
}

// The counter picks up where the log left off, so a restart cannot hand
// out an id a task in that conversation already used.
func TestIdsDoNotRestartAfterARestart(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	loop.Store.CreateSession(sid, "", "general-purpose", true)
	at := time.Now().Add(time.Hour)
	for _, id := range []string{"s1", "s2", "s3"} {
		loop.Store.Append(sid, events.TypeScheduleCreated, map[string]any{
			"id": id, "at": at.Format(time.RFC3339Nano), "prompt": "x", "agent": "general-purpose",
		})
	}
	loop.Store.Append(sid, events.TypeScheduleRemoved, map[string]any{"id": "s2"})

	sched := NewScheduler(context.Background(), loop)
	sched.Restore([]string{sid}, time.Now())
	next, err := sched.Add(sid, "general-purpose", "new one", at, RepeatOptions{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if next.ID != "s4" {
		t.Errorf("the next id after a restart is %q, want s4", next.ID)
	}
}

// Two conversations firing tasks at once must not collide on the session
// the work runs in, which is the thing a short id cannot be unique for.
func TestTwoConversationsRunTheirOwnS1(t *testing.T) {
	loop := scheduleLoop(t)
	for _, id := range []string{"a", "b"} {
		loop.Store.CreateSession(id, "", "general-purpose", true)
	}
	sched := NewScheduler(context.Background(), loop)
	soon := time.Now().Add(30 * time.Millisecond)
	sched.Add("a", "general-purpose", "one", soon, RepeatOptions{})
	sched.Add("b", "general-purpose", "one", soon, RepeatOptions{})

	ra := waitForStatus(t, sched, "a", "s1", ScheduleDone)
	rb := waitForStatus(t, sched, "b", "s1", ScheduleDone)
	if ra.RunSession == rb.RunSession {
		t.Fatalf("both ran in %q; a session id has to be unique in the store", ra.RunSession)
	}
	if len(loop.Store.Children("a")) != 1 || len(loop.Store.Children("b")) != 1 {
		t.Error("the runs did not land one under each conversation")
	}
}

// The division of labour the tool exists for: the model separates the
// time from the work, and localcode reads the time. Handing a local model
// the clock is how a scheduled task ends up in the wrong year.
func TestTheScheduleToolParsesTheTimeItself(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	project := t.TempDir()
	if _, err := loop.Store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sched := NewScheduler(context.Background(), loop)
	tool := NewScheduleTool(loop)
	ctx := WithSessionID(context.Background(), sid)

	input, _ := json.Marshal(map[string]string{"when": "내일 아침에", "prompt": "테스트 돌려줘"})
	res := tool.Execute(ctx, input)
	if res.IsError {
		t.Fatalf("Execute: %s", res.Content)
	}
	list := sched.List(sid)
	if len(list) != 1 {
		t.Fatalf("booked %d tasks, want 1", len(list))
	}
	if list[0].Prompt != "테스트 돌려줘" {
		t.Errorf("prompt = %q", list[0].Prompt)
	}
	if h, m := list[0].At.Hour(), list[0].At.Minute(); h != 9 || m != 0 {
		t.Errorf("booked for %02d:%02d, want 09:00 — the time is read here, not by the model", h, m)
	}
	// The answer says where it will run and what the promise is not, so
	// the model can tell the user rather than inventing either.
	for _, want := range []string{list[0].ID, project, "only while localcode is running"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("the tool's answer does not mention %q: %s", want, res.Content)
		}
	}
}

// A time the parser will not read is handed back with its own reason, so
// the model can ask for the missing half instead of inventing one.
func TestTheScheduleToolPassesTheRefusalBack(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	loop.Store.CreateSession(sid, "", "general-purpose", true)
	NewScheduler(context.Background(), loop)
	tool := NewScheduleTool(loop)
	ctx := WithSessionID(context.Background(), sid)

	for _, tt := range []struct{ when, want string }{
		{"나중에", "is not a time"},
		// Repeats parse now; these two are the shapes that still have no
		// reading which is not a guess.
		{"매달 1일", "by the month"},
		{"10초마다", "Use minutes or longer"},
		{"소풍 갈 때쯤", "could not read a time"},
	} {
		input, _ := json.Marshal(map[string]string{"when": tt.when, "prompt": "x"})
		res := tool.Execute(ctx, input)
		if !res.IsError {
			t.Errorf("Execute(%q) succeeded", tt.when)
			continue
		}
		if !strings.Contains(res.Content, tt.want) {
			t.Errorf("Execute(%q) said %q, want it to mention %q", tt.when, res.Content, tt.want)
		}
	}
}

// A scheduled turn may not book another. Nobody is watching it, so a task
// that books its own successor is a loop with no one in the room, and the
// ceiling on outstanding tasks is per conversation — which a chain would
// walk past by using a new conversation each time.
func TestAScheduledTurnCannotBookAnother(t *testing.T) {
	loop := scheduleLoop(t)
	const sid = "s1"
	loop.Store.CreateSession(sid, "", "general-purpose", true)
	sched := NewScheduler(context.Background(), loop)
	tool := NewScheduleTool(loop)

	ctx := WithUnattended(WithSessionID(context.Background(), sid))
	input, _ := json.Marshal(map[string]string{"when": "in 1 hour", "prompt": "again"})
	res := tool.Execute(ctx, input)
	if !res.IsError || !res.Refused {
		t.Fatalf("a scheduled turn was allowed to book another: %+v", res)
	}
	if len(sched.List(sid)) != 0 {
		t.Error("it booked one anyway")
	}
}

// The permission prompt names the moment localcode read, not the words
// the model passed: "schedule for 내일 아침" is the model's reading and
// the one worth confirming is what will actually happen.
func TestTheSchedulePromptNamesTheResolvedTime(t *testing.T) {
	tool := NewScheduleTool(scheduleLoop(t))
	input, _ := json.Marshal(map[string]string{"when": "tomorrow 9am", "prompt": "run the tests"})
	got := tool.Describe(input)
	if !strings.Contains(got, "09:00") || !strings.Contains(got, "run the tests") {
		t.Errorf("Describe = %q, want the resolved time and the work", got)
	}
	if strings.Contains(got, "tomorrow 9am") {
		t.Errorf("Describe = %q; it repeats the model's words instead of localcode's reading", got)
	}
	// And an unreadable time says so rather than showing a time it does
	// not have.
	bad, _ := json.Marshal(map[string]string{"when": "나중에", "prompt": "x"})
	if !strings.Contains(tool.Describe(bad), "unreadable") {
		t.Errorf("Describe on a bad time = %q", tool.Describe(bad))
	}
}

// A pipe waits not at all, and "nobody answered within 0s" describes
// somebody having been asked. The two callers differ only in the waiting,
// so the sentence says which of the two happened.
func TestAPipeIsToldThereWasNobodyToAskRatherThanThatNobodyAnswered(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	broker := NewPermissionBroker(store)

	// What cmd/localcode's one-shot sets: no desk, so no wait.
	restore := SetUnattendedWait(0)
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
		if !strings.Contains(refused.Reason, "nobody to ask") {
			t.Errorf("the reason is %q; with no wait, nothing was asked", refused.Reason)
		}
		if strings.Contains(refused.Reason, "0s") {
			t.Errorf("the reason reports a wait that did not happen: %q", refused.Reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a turn with no wait configured waited anyway")
	}
}
