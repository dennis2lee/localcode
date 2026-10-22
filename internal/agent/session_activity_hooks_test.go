package agent

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"localcode/internal/session"
)

// A task spawned by a task counts for the conversation at the root of the
// chain: that is the session someone can see in a list, and the task's
// own parent, another task, is not in any.
func TestATasksWorkCountsForItsTopLevelConversation(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	loop := newTestLoop(store, nil)
	tm := NewTaskManager(context.Background(), loop, 1)
	if _, err := store.CreateSession("top", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSessionIn("child", "top", "general-purpose", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSessionIn("grandchild", "child", "general-purpose", "", false); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"top": "top", "child": "top", "grandchild": "top", "unknown": "unknown"} {
		if got := tm.topLevel(id); got != want {
			t.Errorf("topLevel(%s) = %s, want %s", id, got, want)
		}
	}

	var mu sync.Mutex
	var told []string
	tm.OnTasksChanged(func(id string) {
		mu.Lock()
		told = append(told, id)
		mu.Unlock()
	})
	tm.mu.Lock()
	tm.owner["t1"] = tm.topLevel("grandchild")
	tm.owner["t2"] = tm.topLevel("top")
	tm.owner["t3"] = tm.topLevel("elsewhere")
	tm.mu.Unlock()
	if n := tm.RunningFor("top"); n != 2 {
		t.Errorf("RunningFor(top) = %d, want the nested task and the direct one", n)
	}
	if n := tm.RunningFor("child"); n != 0 {
		t.Errorf("RunningFor(child) = %d; a task is not a conversation anybody lists", n)
	}
	tm.tasksChanged("top")
	tm.tasksChanged("")
	if !reflect.DeepEqual(told, []string{"top"}) {
		t.Errorf("told %v, want the one session and nothing for an empty id", told)
	}
	var nilTM *TaskManager
	if nilTM.RunningFor("top") != 0 {
		t.Error("a loop with no task manager reports tasks running")
	}
}

// Each broker says which sessions started or stopped holding a question,
// once per session however many questions moved, and outside its lock: the
// daemon reads the broker's state back while it is being told.
func TestTheBrokersSayWhoIsWaitingForAnAnswer(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	pb := NewPermissionBroker(store)
	var told []string
	pb.OnAskingChanged(func(id string) {
		told = append(told, id)
		_ = pb.Asking() // takes the lock, so this would deadlock inside it
	})
	pb.mark([]string{"s1", "s1", "s2"})
	if !reflect.DeepEqual(told, []string{"s1", "s2"}) {
		t.Errorf("told %v, want each session once", told)
	}
	if got := pb.Asking(); !got["s1"] || !got["s2"] {
		t.Errorf("Asking() = %v", got)
	}
	told = nil
	pb.unmark([]string{"s1", "s1", "s2"})
	if !reflect.DeepEqual(told, []string{"s1", "s2"}) || len(pb.Asking()) != 0 {
		t.Errorf("told %v, asking %v after every question came down", told, pb.Asking())
	}

	ib := NewInputBroker(store)
	if _, err := store.CreateSession("s3", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	var asked []bool
	ib.OnAskingChanged(func(id string) { asked = append(asked, ib.Asking()[id]) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ib.Ask(ctx, "s3", "which?", nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !ib.Asking()["s3"] {
		if time.Now().After(deadline) {
			t.Fatal("the question never marked its session")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if !reflect.DeepEqual(asked, []bool{true, false}) || len(ib.Asking()) != 0 {
		t.Errorf("told %v and asking %v, want up then down", asked, ib.Asking())
	}
	var nilIB *InputBroker
	if nilIB.Asking() != nil {
		t.Error("a missing input broker reports a question")
	}
}

// A scheduled run is work the booking conversation started, in a session
// of its own that neither the task manager nor the daemon's turn tracker
// ran. It counts for the conversation while it runs, so the session list
// draws it as working, and stops counting when the run ends.
func TestAScheduledRunCountsForTheConversationThatBookedIt(t *testing.T) {
	release := make(chan struct{})
	srv := slowServer(t, release)
	loop, tasks := newBackgroundLoop(t, srv.URL)
	const sid = "booked-in"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatal(err)
	}
	sched := NewScheduler(context.Background(), loop)
	entry, err := sched.Add(sid, "explore", "take your time", time.Now().Add(20*time.Millisecond), RepeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e := waitForStatus(t, sched, sid, entry.ID, ScheduleRunning)
	waitUntil(t, 5*time.Second, func() bool {
		evs, _ := loop.Store.Events(e.RunSession, 0)
		return len(evs) > 1
	}, "the scheduled run never started its turn")
	if n := tasks.RunningFor(sid); n != 1 {
		t.Errorf("RunningFor(%s) = %d while its scheduled run is going, want 1", sid, n)
	}

	close(release)
	waitForStatus(t, sched, sid, entry.ID, ScheduleDone)
	waitUntil(t, 5*time.Second, func() bool { return tasks.RunningFor(sid) == 0 }, "the finished run still counts")
}

// Through Spawn itself: a task spawned by a task counts for the
// conversation at the top, not for the task that spawned it.
func TestASpawnedGrandchildCountsForTheTopConversation(t *testing.T) {
	release := make(chan struct{})
	srv := slowServer(t, release)
	defer close(release)
	loop, tasks := newBackgroundLoop(t, srv.URL)
	if _, err := loop.Store.CreateSession("top", "", "explore", true); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Store.CreateSessionIn("child", "top", "explore", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Spawn("child", "explore", "slow work"); err != nil {
		t.Fatal(err)
	}
	if n := tasks.RunningFor("top"); n != 1 {
		t.Errorf("RunningFor(top) = %d, want the grandchild counted for it", n)
	}
	if n := tasks.RunningFor("child"); n != 0 {
		t.Errorf("RunningFor(child) = %d; the task's parent is not a listed conversation", n)
	}
}
