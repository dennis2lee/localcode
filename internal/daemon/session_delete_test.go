package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/tools"
)

// R3N1. Deleting a conversation has to stop the work it started.
//
// A background task runs in a session of its own, rooted in the task
// manager's context rather than the launching turn's, so that it survives
// the turn. The cost of that was that it also survived the user deleting
// the conversation: the task kept calling models and running tools for an
// instruction given in a conversation that no longer existed, and its
// session file stayed on disk where nothing lists it.

// slowModelServer answers only when released, and counts how many
// requests it has been asked for. That is what makes "still running" and
// "stopped" observable from outside the agent.
func heldModelServer(t *testing.T) (*httptest.Server, *atomic.Int32, func()) {
	t.Helper()
	release := make(chan struct{})
	var calls atomic.Int32
	var once sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
			// Cancelled: the turn this belonged to was stopped.
			return
		case <-time.After(10 * time.Second):
			t.Error("the model server was never released or cancelled")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	return srv, &calls, func() { once.Do(func() { close(release) }) }
}

func TestDeletingAParentStopsItsRunningBackgroundChild(t *testing.T) {
	model, calls, release := heldModelServer(t)
	defer model.Close()
	defer release()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	childID, err := d.Tasks.Spawn("parent", "build", "work on this")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Wait until the child is genuinely in a model call, so the delete is
	// stopping running work rather than racing its start.
	waitFor(t, func() bool { return calls.Load() > 0 })

	deleteSession(t, httpSrv.URL, "parent")

	// Nothing of the child is left: not the session, not its state.
	if _, err := d.Loop.Store.Get(childID); err == nil {
		t.Error("the child session outlived the conversation that launched it")
	}
	for _, s := range d.Loop.Store.AllSessions() {
		if s.ID == childID || s.ParentID == "parent" {
			t.Errorf("session %s (parent %q) survived the delete", s.ID, s.ParentID)
		}
	}

	// And it is not going to start another model call. Releasing the
	// server would let a still-running turn continue; the count must not
	// move.
	before := calls.Load()
	release()
	time.Sleep(150 * time.Millisecond)
	if after := calls.Load(); after != before {
		t.Errorf("the child made %d more model calls after its parent was deleted", after-before)
	}
}

// A completed but uncollected child, which is the other shape: nothing to
// cancel, but a session and an answer that nothing will ever come for.
func TestDeletingAParentRemovesACompletedChildSession(t *testing.T) {
	model, _, release := heldModelServer(t)
	defer model.Close()
	release()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	childID, err := d.Tasks.Spawn("parent", "build", "quick")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// A terminal status on the parent, not merely "the child has some
	// event": the first event is the user message, which is there long
	// before the child has finished. The completed shape has to be the
	// one being tested.
	waitFor(t, func() bool {
		evs, err := d.Loop.Store.Events("parent", 0)
		if err != nil {
			return false
		}
		for _, ev := range evs {
			if ev.Type != events.TypeTaskStatus {
				continue
			}
			if id, _ := ev.Data["task_id"].(string); id != childID {
				continue
			}
			switch status, _ := ev.Data["status"].(string); status {
			case "completed", "failed", "cancelled":
				return true
			}
		}
		return false
	})

	deleteSession(t, httpSrv.URL, "parent")
	if _, err := d.Loop.Store.Get(childID); err == nil {
		t.Error("a finished child session was left behind")
	}
}

// A nested tree: a child that has a child of its own. Stopping only one
// level would leave the grandchild running and on disk.
func TestDeletingAParentRemovesTheWholeDescendantTree(t *testing.T) {
	model, _, release := heldModelServer(t)
	defer model.Close()
	release()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	for _, s := range []struct{ id, parent string }{
		{"parent", ""}, {"child", "parent"}, {"grandchild", "child"},
	} {
		visible := s.parent == ""
		if _, err := d.Loop.Store.CreateSession(s.id, s.parent, "build", visible); err != nil {
			t.Fatalf("create %s: %v", s.id, err)
		}
	}

	deleteSession(t, httpSrv.URL, "parent")

	for _, id := range []string{"parent", "child", "grandchild"} {
		if _, err := d.Loop.Store.Get(id); err == nil {
			t.Errorf("%s survived the delete", id)
		}
	}
	if n := len(d.Loop.Store.AllSessions()); n != 0 {
		t.Errorf("%d sessions left in the store, want none", n)
	}
}

// A spawn against a session that has already been deleted is refused by
// the parent check. Renamed from a name that claimed more than it tested:
// this is the sequential case, and the concurrent one it used to be named
// for is in internal/agent/lifecycle_test.go, where the interleaving can
// be forced rather than hoped for.
func TestASpawnAfterItsParentWasDeletedIsRefused(t *testing.T) {
	model, _, release := heldModelServer(t)
	defer model.Close()
	release()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	deleteSession(t, httpSrv.URL, "parent")

	if _, err := d.Tasks.Spawn("parent", "build", "too late"); err == nil {
		t.Error("a task was spawned under a session that had been deleted")
	}
	if n := len(d.Loop.Store.AllSessions()); n != 0 {
		t.Errorf("%d sessions left after a refused spawn, want none", n)
	}
}

func deleteSession(t *testing.T, baseURL, id string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, baseURL+"/api/sessions/"+id, nil)
	if err != nil {
		t.Fatalf("build delete request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE session status = %d, want 204", resp.StatusCode)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

// R5N1. Delete-all used to check that nothing was busy, release the
// tracker lock, spend however long it took to stop background work, and
// only then call DeleteAll. A message arriving in that interval started a
// turn the check had already passed, and DeleteAll closed its log while it
// was still writing to it.
//
// The barrier now goes up first, so the check answers a question that
// stays answered. Held open here, which is what the middle of a real
// delete-all looks like from outside.
// blockingTool holds a turn open at a point cancellation cannot cut
// short.
//
// Cancelling a model request does not give a window: the HTTP client
// returns the moment the context is done, whatever the server is still
// doing, so the child unwinds immediately and the delete is over before
// anything can be observed. A tool that does not watch its context does
// give one, and it is also the honest shape of the risk: a turn in the
// middle of writing a file is exactly what must not have its session log
// removed underneath it.
type blockingTool struct {
	entered chan struct{}
	release chan struct{}
	once    *sync.Once
}

func (b blockingTool) Name() string                            { return "block" }
func (b blockingTool) Description() string                     { return "blocks until released" }
func (b blockingTool) InputSchema() json.RawMessage            { return json.RawMessage(`{"type":"object"}`) }
func (b blockingTool) RequiresPermission(json.RawMessage) bool { return false }
func (b blockingTool) Execute(ctx context.Context, in json.RawMessage) tools.Result {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return tools.Result{Content: "released"}
}

func TestNoTurnOrSessionCanStartWhileDeleteAllIsRunning(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	letGo := func() { releaseOnce.Do(func() { close(release) }) }
	defer letGo()

	var round atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		n := round.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"block","arguments":"{}"}}]}}]}`+"\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	d.Loop.Tools.Register(blockingTool{entered: entered, release: release, once: &enterOnce})
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := d.Tasks.Spawn("parent", "build", "work"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	<-entered // the child is inside a tool call that cancellation cannot cut short

	deleted := make(chan int, 1)
	go func() {
		req, err := http.NewRequest(http.MethodDelete, httpSrv.URL+"/api/sessions", nil)
		if err != nil {
			deleted <- 0
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			deleted <- 0
			return
		}
		resp.Body.Close()
		deleted <- resp.StatusCode
	}()

	// The delete is now past its admission step and waiting for that tool
	// call to finish. This is the interval the old handler left open, and
	// it stays open until the test says otherwise.
	waitFor(t, func() bool { return d.Loop.SessionsClosing() })
	select {
	case code := <-deleted:
		t.Fatalf("delete all finished (%d) before the window could be tested", code)
	default:
	}

	// A turn: refused, not started into a session whose log is about to be
	// removed.
	resp, err := http.Post(httpSrv.URL+"/api/sessions/parent/messages", "application/json",
		strings.NewReader(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("POST message: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("POST message during delete all = %d, want 409", resp.StatusCode)
	}

	// A new session: same, or it is created into a store being emptied.
	resp, err = http.Post(httpSrv.URL+"/api/sessions", "application/json",
		strings.NewReader(`{"agent":"general-purpose"}`))
	if err != nil {
		t.Fatalf("POST session: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("POST session during delete all = %d, want 409", resp.StatusCode)
	}

	letGo()
	select {
	case code := <-deleted:
		if code != http.StatusNoContent {
			t.Fatalf("delete all = %d, want 204", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("delete all never finished after the tool was released")
	}

	if n := len(d.Loop.Store.AllSessions()); n != 0 {
		t.Errorf("%d sessions left after delete all", n)
	}
	// And both paths work again, so this is a boundary rather than a door
	// left shut.
	resp, err = http.Post(httpSrv.URL+"/api/sessions", "application/json",
		strings.NewReader(`{"agent":"general-purpose"}`))
	if err != nil {
		t.Fatalf("POST session: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Errorf("POST session after delete all = %d, want it accepted", resp.StatusCode)
	}
}

// And the whole endpoint still works: delete-all removes every session,
// including the invisible children, and stops their work.
func TestDeleteAllRemovesChildrenAndStopsTheirWork(t *testing.T) {
	model, calls, release := heldModelServer(t)
	defer model.Close()
	defer release()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := d.Tasks.Spawn("parent", "build", "work"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitFor(t, func() bool { return calls.Load() > 0 })

	req, err := http.NewRequest(http.MethodDelete, httpSrv.URL+"/api/sessions", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE all: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE all = %d, want 204", resp.StatusCode)
	}

	if n := len(d.Loop.Store.AllSessions()); n != 0 {
		t.Errorf("%d sessions left after delete all", n)
	}
	before := calls.Load()
	release()
	time.Sleep(150 * time.Millisecond)
	if after := calls.Load(); after != before {
		t.Errorf("the child made %d more model calls after delete all", after-before)
	}
}

// R5N1, the ordering itself. The barrier has to be up at the moment the
// busy check is evaluated, not a moment later: a check that is answered
// while turns can still start is a check that has already expired by the
// time anything acts on it.
//
// Pinned with a hook rather than a race, because the window between the
// two is a few instructions wide and no amount of retrying would land in
// it reliably.
func TestDeleteAllRaisesItsBarrierBeforeCheckingWhatIsBusy(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var sawBarrier bool
	var turnStatus int
	deleteAllProbe = func() {
		sawBarrier = d.Loop.SessionsClosing()
		resp, err := http.Post(httpSrv.URL+"/api/sessions/parent/messages", "application/json",
			strings.NewReader(`{"text":"hello"}`))
		if err != nil {
			t.Errorf("POST message: %v", err)
			return
		}
		resp.Body.Close()
		turnStatus = resp.StatusCode
	}
	t.Cleanup(func() { deleteAllProbe = nil })

	req, err := http.NewRequest(http.MethodDelete, httpSrv.URL+"/api/sessions", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE all: %v", err)
	}
	resp.Body.Close()

	if !sawBarrier {
		t.Error("the busy check ran before the barrier was up, so its answer could expire before anything acted on it")
	}
	if turnStatus != http.StatusConflict {
		t.Errorf("a turn started at the busy check returned %d, want 409", turnStatus)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE all = %d, want 204", resp.StatusCode)
	}
	if n := len(d.Loop.Store.AllSessions()); n != 0 {
		t.Errorf("%d sessions left after delete all", n)
	}
}

// R5N1, round 6. The gap the previous tests did not cover: after a
// top-level handler has decided it may proceed, and before the thing it is
// admitting has committed.
//
// Every test here holds a handler open at exactly that point and then runs
// delete-all against it. The assertion is the acceptance criterion, in two
// halves: delete-all must not pass its busy check or its cleanup snapshot
// while such an operation is unregistered, and whatever the admission
// commits must be visible to the decision that follows.

// holdAtAdmission installs a barrier that fires once, in the window
// between a top-level admission succeeding and the thing it admits
// committing. It returns a channel that closes when a handler arrives, and
// a func that lets it continue.
func holdAtAdmission(t *testing.T) (arrived <-chan struct{}, release func()) {
	t.Helper()
	return holdAtHook(t, &topAdmitBarrier)
}

// holdAtHook installs a once-firing barrier into any of the package's
// test hooks: the first handler to reach it closes arrived and blocks
// until release.
func holdAtHook(t *testing.T, hook *func()) (arrived <-chan struct{}, release func()) {
	t.Helper()
	at := make(chan struct{})
	go1 := make(chan struct{})
	var onArrive, onRelease sync.Once
	*hook = func() {
		onArrive.Do(func() { close(at) })
		<-go1
	}
	t.Cleanup(func() { *hook = nil })
	return at, func() { onRelease.Do(func() { close(go1) }) }
}

// A message that has been admitted but has not yet registered its turn.
// Delete-all has to wait for it, and then see the turn it started.
func TestDeleteAllWaitsForAMessageThatHasNotRegisteredItsTurnYet(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()
	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	arrived, release := holdAtAdmission(t)
	defer release()

	sent := make(chan int, 1)
	go func() {
		resp, err := http.Post(httpSrv.URL+"/api/sessions/parent/messages", "application/json",
			strings.NewReader(`{"text":"hello"}`))
		if err != nil {
			sent <- 0
			return
		}
		resp.Body.Close()
		sent <- resp.StatusCode
	}()
	<-arrived // admitted, turn not registered

	deleted := make(chan int, 1)
	go func() { deleted <- deleteAll(httpSrv.URL) }()

	// This is the whole finding. Delete-all must not answer its busy check
	// while a turn that is going to start is invisible to it.
	select {
	case code := <-deleted:
		t.Fatalf("delete all finished (%d) while an admitted message had not registered its turn", code)
	case <-time.After(250 * time.Millisecond):
	}

	release()
	if code := <-sent; code != http.StatusAccepted {
		t.Fatalf("POST message = %d, want 202", code)
	}
	// The turn committed, so the busy check has to see it and refuse.
	if code := <-deleted; code != http.StatusConflict {
		t.Errorf("delete all = %d, want 409 for the turn that started", code)
	}
	if _, err := d.Loop.Store.Get("parent"); err != nil {
		t.Error("the session was removed underneath a turn that had just started")
	}
}

// A session being created that has been admitted but does not exist yet.
// Delete-all waits, and its cleanup snapshot then includes it.
func TestDeleteAllWaitsForASessionBeingCreated(t *testing.T) {
	d, httpSrv, closeAll := plainDaemon(t)
	defer closeAll()

	arrived, release := holdAtAdmission(t)
	defer release()

	created := make(chan int, 1)
	go func() {
		resp, err := http.Post(httpSrv.URL+"/api/sessions", "application/json",
			strings.NewReader(`{"agent":"general-purpose"}`))
		if err != nil {
			created <- 0
			return
		}
		resp.Body.Close()
		created <- resp.StatusCode
	}()
	<-arrived

	deleted := make(chan int, 1)
	go func() { deleted <- deleteAll(httpSrv.URL) }()
	select {
	case code := <-deleted:
		t.Fatalf("delete all finished (%d) while a session was being created", code)
	case <-time.After(250 * time.Millisecond):
	}

	release()
	if code := <-created; code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("POST session = %d, want it accepted", code)
	}
	if code := <-deleted; code != http.StatusNoContent {
		t.Fatalf("delete all = %d, want 204", code)
	}
	if n := len(d.Loop.Store.AllSessions()); n != 0 {
		t.Errorf("%d sessions left: one created during delete all escaped its cleanup snapshot", n)
	}
}

// Fork is a top-level conversation and had no check at all. Same
// guarantee.
func TestDeleteAllWaitsForAForkBeingCreated(t *testing.T) {
	d, httpSrv, closeAll := plainDaemon(t)
	defer closeAll()

	if _, err := d.Loop.Store.CreateSession("src", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	arrived, release := holdAtAdmission(t)
	defer release()

	forked := make(chan int, 1)
	go func() {
		resp, err := http.Post(httpSrv.URL+"/api/sessions/src/fork", "application/json", nil)
		if err != nil {
			forked <- 0
			return
		}
		resp.Body.Close()
		forked <- resp.StatusCode
	}()
	<-arrived

	deleted := make(chan int, 1)
	go func() { deleted <- deleteAll(httpSrv.URL) }()
	select {
	case code := <-deleted:
		t.Fatalf("delete all finished (%d) while a fork was being created", code)
	case <-time.After(250 * time.Millisecond):
	}

	release()
	if code := <-forked; code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("POST fork = %d, want it accepted", code)
	}
	if code := <-deleted; code != http.StatusNoContent {
		t.Fatalf("delete all = %d, want 204", code)
	}
	if n := len(d.Loop.Store.AllSessions()); n != 0 {
		t.Errorf("%d sessions left: a fork created during delete all escaped its cleanup snapshot", n)
	}
}

// R5N1, round 7. Fork depends on state the other creation paths do not
// have: it reads the source before it creates the destination. The
// admission window has to cover the read, not only the commit —
// otherwise delete-all can run to completion in between, and the fork
// then rebuilds a conversation from a source that no longer exists,
// an outcome no serial ordering of the two operations produces.
//
// This holds a fork after its source snapshot and before its destination
// exists, and runs delete-all against it. Delete-all must wait, and its
// cleanup must then remove both the source and the fork.
func TestDeleteAllWaitsForAForkThatHasReadItsSourceButNotCommitted(t *testing.T) {
	d, httpSrv, closeAll := plainDaemon(t)
	defer closeAll()

	if _, err := d.Loop.Store.CreateSession("src", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	arrived, release := holdAtHook(t, &forkSnapshotBarrier)
	defer release()

	forked := make(chan int, 1)
	go func() {
		resp, err := http.Post(httpSrv.URL+"/api/sessions/src/fork", "application/json", nil)
		if err != nil {
			forked <- 0
			return
		}
		resp.Body.Close()
		forked <- resp.StatusCode
	}()
	<-arrived // source snapshot read, destination not created

	deleted := make(chan int, 1)
	go func() { deleted <- deleteAll(httpSrv.URL) }()

	// The whole finding: the source this fork has already consumed must
	// not be deleted out from under an unregistered destination.
	select {
	case code := <-deleted:
		t.Fatalf("delete all finished (%d) while a fork had read its source but not committed", code)
	case <-time.After(250 * time.Millisecond):
	}

	release()
	if code := <-forked; code != http.StatusCreated {
		t.Fatalf("POST fork = %d, want 201", code)
	}
	// The fork serialized before delete-all, so delete-all removes both.
	if code := <-deleted; code != http.StatusNoContent {
		t.Fatalf("delete all = %d, want 204", code)
	}
	if n := len(d.Loop.Store.AllSessions()); n != 0 {
		t.Errorf("%d sessions left: the fork or its source survived a delete all it serialized before", n)
	}
}

func deleteAll(baseURL string) int {
	req, err := http.NewRequest(http.MethodDelete, baseURL+"/api/sessions", nil)
	if err != nil {
		return 0
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// plainDaemon is a daemon with a model server nothing is expected to call.
func plainDaemon(t *testing.T) (*Daemon, *httptest.Server, func()) {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	return d, httpSrv, func() { httpSrv.Close(); model.Close() }
}
