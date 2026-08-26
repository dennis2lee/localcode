package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// R3N1, round 5. The interleavings a race detector cannot find on its own,
// forced with explicit barriers rather than sleeps.
//
// The shape of every test here is the same: hold one side at a known point
// and let the other side run all the way to where it would have gone
// wrong. A test that only starts two goroutines and hopes proves nothing
// about the case it is named for, which is what the round 4 test did.

// Barrier 1: a spawn that has passed the parent check but has not yet
// registered its goroutine. This is the exact window the reviewer
// described: the delete's tree snapshot misses the child, and the child's
// session is then deleted while its goroutine runs.
func TestADeletionWaitsForASpawnAlreadyPastTheParentCheck(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	atBarrier := make(chan struct{})
	releaseSpawn := make(chan struct{})
	spawnBarrier = func() {
		select {
		case <-atBarrier:
		default:
			close(atBarrier)
		}
		<-releaseSpawn
	}
	t.Cleanup(func() { spawnBarrier = nil })

	spawned := make(chan string, 1)
	spawnErr := make(chan error, 1)
	go func() {
		id, err := tasks.Spawn(sid, "explore", "say first-slow")
		spawned <- id
		spawnErr <- err
	}()
	<-atBarrier // the spawn is inside its admission window

	// The delete must not proceed past the claim while that admission is
	// in flight. If it does, it takes a tree snapshot that cannot contain
	// the child.
	claimed := make(chan []string, 1)
	go func() {
		ids, release := loop.StopSessionTree(sid)
		loop.Store.DeleteTree(sid)
		release()
		claimed <- ids
	}()

	select {
	case ids := <-claimed:
		t.Fatalf("the delete claimed %v while a spawn was in flight", ids)
	case <-time.After(200 * time.Millisecond):
		// Correct: blocked on the admission.
	}

	close(releaseSpawn)
	childID := <-spawned
	if err := <-spawnErr; err != nil {
		t.Fatalf("spawn: %v", err)
	}

	select {
	case ids := <-claimed:
		if !contains(ids, childID) {
			t.Fatalf("the delete claimed %v, which does not include the child %s it had to stop", ids, childID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the delete never completed after the spawn finished")
	}

	if _, err := loop.Store.Get(childID); err == nil {
		t.Error("the child session survived the delete")
	}
	assertNothingRetained(t, tasks)
}

// Barrier 2: a spawn that starts after the claim is taken. It has to be
// refused outright, and leave nothing behind.
func TestASpawnDuringADeletionIsRefusedAndLeavesNothing(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	before := len(loop.Store.AllSessions())

	// The claim, held open across the attempts, which is what the real
	// delete does while it stops tasks and removes files.
	_, release := loop.claimSessionTree(sid)

	if _, err := tasks.Spawn(sid, "explore", "say one"); err == nil {
		t.Error("Spawn was admitted into a session being deleted")
	} else if !strings.Contains(err.Error(), "being deleted") {
		t.Errorf("Spawn refused with %v, want it to say the session is being deleted", err)
	}
	if _, err := tasks.SpawnBackground(context.Background(), sid, "explore", "say one", ""); err == nil {
		t.Error("SpawnBackground was admitted into a session being deleted")
	}
	if _, err := tasks.SpawnSync(context.Background(), sid, "explore", "say one"); err == nil {
		t.Error("SpawnSync was admitted into a session being deleted")
	}
	if after := len(loop.Store.AllSessions()); after != before {
		t.Errorf("%d session(s) created by refused admissions, want none", after-before)
	}
	release()

	// And admission reopens once the claim is gone, so the refusal is a
	// boundary rather than a permanent block.
	if _, err := tasks.Spawn(sid, "explore", "say one"); err != nil {
		t.Errorf("Spawn was still refused after the claim was released: %v", err)
	}
}

// Barrier 3: the claim outlives the record removal. Releasing it between
// the last task stopping and the sessions being deleted is the same defect
// one step later, so the window between StopSessionTree and DeleteTree has
// to be closed too.
func TestAdmissionStaysClosedUntilTheRecordsAreGone(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, release := loop.StopSessionTree(sid)
	// The sessions still exist at this point, so the parent check would
	// pass. Only the claim stands between here and a new descendant.
	if _, err := tasks.Spawn(sid, "explore", "say one"); err == nil {
		t.Fatal("a task was admitted between the work stopping and the records being removed")
	}
	if err := loop.Store.DeleteTree(sid); err != nil {
		t.Fatalf("DeleteTree: %v", err)
	}
	release()
	assertNothingRetained(t, tasks)
}

// A descendant deletion running inside an ancestor deletion must not clear
// the ancestor's claim when it finishes. This is why closing is
// refcounted rather than a flag.
func TestOverlappingAncestorAndDescendantDeletionsDoNotClearEachOther(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	for _, s := range []struct{ id, parent string }{{"root", ""}, {"child", "root"}} {
		if _, err := loop.Store.CreateSession(s.id, s.parent, "explore", s.parent == ""); err != nil {
			t.Fatalf("create %s: %v", s.id, err)
		}
	}

	_, releaseOuter := loop.claimSessionTree("root")
	_, releaseInner := loop.claimSessionTree("child")
	releaseInner()

	// The outer claim is still held, so "child" is still closed.
	if _, err := tasks.Spawn("child", "explore", "say one"); err == nil {
		t.Error("the inner release cleared a marker the outer claim still needed")
	}
	releaseOuter()
	if _, err := tasks.Spawn("child", "explore", "say one"); err != nil {
		t.Errorf("admission never reopened after both claims were released: %v", err)
	}
}

// The whole-daemon barrier delete-all needs. It has to be up before
// anything is checked, and it has to refuse the paths that are too long to
// drain: starting a turn and creating a session.
func TestTheGlobalClaimRefusesEverythingWhileItIsHeld(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	release := loop.StopEverything([]string{sid})
	if !loop.SessionsClosing() {
		t.Fatal("the daemon does not report itself as closing while delete-all holds the claim")
	}
	if _, err := tasks.Spawn(sid, "explore", "say one"); err == nil {
		t.Error("a task was admitted during delete-all")
	}
	release()
	if loop.SessionsClosing() {
		t.Error("the daemon still reports itself as closing after the claim was released")
	}
	if _, err := tasks.Spawn(sid, "explore", "say one"); err != nil {
		t.Errorf("admission never reopened after delete-all: %v", err)
	}
}

// The claim must drain admissions rather than only blocking new ones, and
// it must do so no matter how many are in flight.
func TestAClaimWaitsForEveryAdmissionInFlight(t *testing.T) {
	lc := newLifecycle()
	const n = 8
	for range n {
		if !lc.admit("p") {
			t.Fatal("admit was refused with nothing claimed")
		}
	}

	claimed := make(chan struct{})
	go func() {
		lc.claim([]string{"p"})
		close(claimed)
	}()

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			time.Sleep(time.Duration(i) * time.Millisecond)
			select {
			case <-claimed:
				t.Errorf("the claim completed with %d admissions still in flight", n-i)
			default:
			}
			lc.admitted("p")
		}(i)
	}
	wg.Wait()

	select {
	case <-claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("the claim never completed after every admission finished")
	}
	if lc.admit("p") {
		t.Error("an admission was allowed into a claimed tree")
	}
}

func assertNothingRetained(t *testing.T, tm *TaskManager) {
	t.Helper()
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if n := len(tm.results); n != 0 {
		t.Errorf("%d results retained", n)
	}
	if n := len(tm.waiters); n != 0 {
		t.Errorf("%d waiters retained", n)
	}
	if n := len(tm.pending); n != 0 {
		t.Errorf("%d pending entries retained", n)
	}
	if n := len(tm.cancels); n != 0 {
		t.Errorf("%d cancel entries retained", n)
	}
	if n := len(tm.done); n != 0 {
		t.Errorf("%d done entries retained", n)
	}
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// The round 6 non-blocking observation, acted on. A claim used to wait for
// every admission anywhere on the reasoning that one is only a few
// microseconds long. That reasoning was wrong: an admission window covers
// a delegate hook, which can be a subprocess, and two persistent session
// writes. Deleting one conversation should not be held up behind another
// conversation's hook, and sustained delegation elsewhere should not be
// able to starve it.
func TestATreeClaimDoesNotWaitForDelegationInAnotherTree(t *testing.T) {
	lc := newLifecycle()

	// Something in flight under a different parent, and never finishing.
	if !lc.admit("other") {
		t.Fatal("admit refused with nothing claimed")
	}

	claimed := make(chan struct{})
	go func() {
		lc.claim([]string{"mine"})
		close(claimed)
	}()
	select {
	case <-claimed:
	case <-time.After(3 * time.Second):
		t.Fatal("claiming one tree waited on an admission into an unrelated one")
	}

	// It still refuses admission into the tree it claimed, and still lets
	// the unrelated tree carry on.
	if lc.admit("mine") {
		t.Error("a claimed tree admitted new work")
	}
	if !lc.admit("other") {
		t.Error("an unrelated tree was blocked by a claim it has nothing to do with")
	}

	// And delete-all still waits for everything, because everything is
	// what it is removing.
	all := make(chan struct{})
	go func() {
		lc.claimAll()
		close(all)
	}()
	select {
	case <-all:
		t.Fatal("delete-all did not wait for admissions in flight")
	case <-time.After(150 * time.Millisecond):
	}
	lc.admitted("other")
	lc.admitted("other")
	select {
	case <-all:
	case <-time.After(3 * time.Second):
		t.Fatal("delete-all never completed after every admission finished")
	}
}

// A top-level admission belongs to no tree, so deleting one conversation
// must not wait for a new conversation being created somewhere else. Only
// delete-all drains those.
func TestATreeClaimDoesNotWaitForTopLevelAdmissions(t *testing.T) {
	lc := newLifecycle()
	if !lc.admitTop() {
		t.Fatal("admitTop refused with nothing claimed")
	}

	claimed := make(chan struct{})
	go func() {
		lc.claim([]string{"mine"})
		close(claimed)
	}()
	select {
	case <-claimed:
	case <-time.After(3 * time.Second):
		t.Fatal("a tree claim waited on a top-level admission")
	}
	if lc.admitTop() {
		// Not refused: a tree claim is not a daemon claim.
		lc.admittedTop()
	} else {
		t.Error("a tree claim refused a new conversation, which it has no say over")
	}
	lc.admittedTop()
}
