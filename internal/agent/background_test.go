package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// backgroundServer answers with whichever text the prompt asked it to say,
// after an optional pause. The pause is what makes "launched in the
// background" mean something: without it every task would be finished
// before the next line of the test ran, and collecting them in launch
// order would pass whether or not the order was preserved.
func backgroundServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		say := "nothing"
		for _, m := range body.Messages {
			if m["role"] != "user" {
				continue
			}
			if s, ok := m["content"].(string); ok && strings.HasPrefix(s, "say ") {
				say = strings.TrimPrefix(s, "say ")
			}
		}
		// The first task launched is the slowest to answer, so collecting
		// in launch order is a different answer from collecting in
		// completion order.
		if strings.HasSuffix(say, "-slow") {
			time.Sleep(120 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", say)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
}

func newBackgroundLoop(t *testing.T, modelURL string) (*Loop, *TaskManager) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	registry := tools.NewRegistry(nil)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelURL},
		},
		Profiles:       map[string]config.Profile{"only": {Provider: "local", Model: "m"}},
		DefaultProfile: "only",
	}
	loop := New(store, registry, map[string]provider.Provider{"local": provider.NewOpenAICompat(modelURL, "")}, cfg)
	loop.SetSmartAgentEnabled(true)
	tasks := NewTaskManager(context.Background(), loop, 5)
	registry.Register(NewTaskBackgroundTool(tasks, loop.DelegatableAgents))
	registry.Register(NewTaskCollectTool(tasks))
	return loop, tasks
}

func launch(t *testing.T, reg *tools.Registry, ctx context.Context, agentName, prompt string) tools.Result {
	t.Helper()
	input, _ := json.Marshal(map[string]string{"agent": agentName, "prompt": prompt})
	return reg.Call(ctx, "TaskBackground", input, "")
}

// The point of the pair of tools: three questions asked at once take as
// long as the slowest, not as long as all three together, and the answers
// come back matched to the order they were asked in.
func TestBackgroundTasksRunAtOnceAndCollectInLaunchOrder(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, _ := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)

	for _, prompt := range []string{"say first-slow", "say second", "say third"} {
		if res := launch(t, loop.Tools, ctx, "explore", prompt); res.IsError {
			t.Fatalf("launch %q: %s", prompt, res.Content)
		}
	}

	started := time.Now()
	res := loop.Tools.Call(ctx, "TaskCollect", json.RawMessage(`{}`), "")
	if res.IsError {
		t.Fatalf("collect: %s", res.Content)
	}
	// Three sequential 120ms answers would be 360ms; only the first task
	// is slow, so anything near that means they ran one after another.
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Errorf("collecting took %v, which is long enough that the tasks did not run at once", elapsed)
	}

	firstAt := strings.Index(res.Content, "first-slow")
	secondAt := strings.Index(res.Content, "second")
	thirdAt := strings.Index(res.Content, "third")
	if firstAt < 0 || secondAt < 0 || thirdAt < 0 {
		t.Fatalf("not every answer came back:\n%s", res.Content)
	}
	if !(firstAt < secondAt && secondAt < thirdAt) {
		t.Errorf("answers came back out of launch order:\n%s", res.Content)
	}
}

func TestCollectingOneTaskLeavesTheOthersOutstanding(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, _ := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)

	first := launch(t, loop.Tools, ctx, "explore", "say alpha")
	if first.IsError {
		t.Fatalf("launch: %s", first.Content)
	}
	if res := launch(t, loop.Tools, ctx, "explore", "say beta"); res.IsError {
		t.Fatalf("launch: %s", res.Content)
	}

	id := taskIDFrom(t, first.Content)
	input, _ := json.Marshal(map[string]string{"task_id": id})
	res := loop.Tools.Call(ctx, "TaskCollect", input, "")
	if res.IsError {
		t.Fatalf("collect one: %s", res.Content)
	}
	if !strings.Contains(res.Content, "alpha") || strings.Contains(res.Content, "beta") {
		t.Errorf("collecting one task returned the wrong answers:\n%s", res.Content)
	}

	rest := loop.Tools.Call(ctx, "TaskCollect", json.RawMessage(`{}`), "")
	if !strings.Contains(rest.Content, "beta") {
		t.Errorf("the other task was not still outstanding:\n%s", rest.Content)
	}
}

func TestCollectingWithNothingOutstandingSaysSo(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, _ := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)

	res := loop.Tools.Call(ctx, "TaskCollect", json.RawMessage(`{}`), "")
	if res.IsError {
		t.Errorf("collecting nothing is not an error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "no background tasks") {
		t.Errorf("got %q, want it to say there is nothing outstanding", res.Content)
	}
}

// The ceiling exists for the model that launches work it never comes back
// for. Every outstanding task is tokens being spent in a session nobody is
// reading, so hitting the limit has to say what to do rather than queue.
func TestASessionCannotLaunchUnboundedBackgroundWork(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, _ := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)

	for i := 0; i < maxBackgroundTasks; i++ {
		if res := launch(t, loop.Tools, ctx, "explore", "say one"); res.IsError {
			t.Fatalf("launch %d: %s", i, res.Content)
		}
	}
	res := launch(t, loop.Tools, ctx, "explore", "say one more")
	if !res.IsError {
		t.Fatal("a ninth background task was accepted")
	}
	if !strings.Contains(res.Content, "collect them before launching more") {
		t.Errorf("the refusal does not say what to do: %q", res.Content)
	}
}

// Background delegation must not be the way around the depth guard the
// synchronous Task tool applies: a sub-agent that cannot call Task could
// otherwise launch one and never collect it, and nothing would be waiting
// to notice.
func TestBackgroundDelegationObeysTheDepthLimit(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, _ := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := withTaskDepth(WithSessionID(context.Background(), sid), maxTaskDepth)

	res := launch(t, loop.Tools, ctx, "explore", "say deeper")
	if !res.IsError {
		t.Fatal("a task was launched past the delegation depth limit")
	}
	if !strings.Contains(res.Content, "depth") {
		t.Errorf("got %q, want it to say why", res.Content)
	}
}

// taskIDFrom pulls the id out of what TaskBackground reports.
func taskIDFrom(t *testing.T, content string) string {
	t.Helper()
	const marker = "as task "
	i := strings.Index(content, marker)
	if i < 0 {
		t.Fatalf("no task id in %q", content)
	}
	rest := content[i+len(marker):]
	if end := strings.IndexAny(rest, ". \n"); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// waitForTaskStatus blocks until the parent session records a terminal
// status for taskID.
func waitForTaskStatus(t *testing.T, loop *Loop, parentSessionID, taskID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		all, err := loop.Store.Events(parentSessionID, 0)
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		for _, ev := range all {
			if ev.Type != events.TypeTaskStatus {
				continue
			}
			if id, _ := ev.Data["task_id"].(string); id != taskID {
				continue
			}
			switch status, _ := ev.Data["status"].(string); status {
			case "completed", "failed", "cancelled":
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s never reached a terminal status", taskID)
}

// SA2. The depth limit lived only on the launching context, and a
// background child was built from the manager's root context instead, so
// every generation started again at zero. Background delegation was
// therefore the way around the limit rather than subject to it.
func TestBackgroundDelegationCarriesTheDepthLimitIntoTheChild(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Launched one level below the ceiling, the way a sub-agent's own
	// launch arrives.
	ctx := withTaskDepth(WithSessionID(context.Background(), sid), maxTaskDepth-1)
	if res := launch(t, loop.Tools, ctx, "explore", "say one"); res.IsError {
		t.Fatalf("launch at depth %d was refused: %s", maxTaskDepth-1, res.Content)
	}

	child := tasks.childContext(ctx, "")
	if got := taskDepthFromContext(child); got != maxTaskDepth {
		t.Fatalf("the background child runs at depth %d, want %d — the parent's depth was left behind", got, maxTaskDepth)
	}
	// And at that depth it is refused, whichever tool it reaches for.
	grandchild := WithSessionID(child, sid)
	if res := launch(t, loop.Tools, grandchild, "explore", "say two"); !res.IsError {
		t.Error("a background child at the depth limit was allowed to launch another background task")
	}
	input, _ := json.Marshal(map[string]string{"agent": "explore", "prompt": "say two"})
	if res := loop.Tools.Call(grandchild, "Task", input, ""); !res.IsError {
		t.Error("a background child at the depth limit was allowed to delegate synchronously")
	}
}

// The mixed path, which is the one most likely to regress: a synchronous
// delegation and then a background one must add up rather than each
// starting over.
func TestSynchronousThenBackgroundDelegationAddsUp(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	// One synchronous hop already taken.
	ctx := withTaskDepth(WithSessionID(context.Background(), sid), 1)
	if res := launch(t, loop.Tools, ctx, "explore", "say one"); res.IsError {
		t.Fatalf("launch: %s", res.Content)
	}
	child := tasks.childContext(ctx, "")
	if got := taskDepthFromContext(child); got != 2 {
		t.Fatalf("depth after one synchronous and one background hop = %d, want 2", got)
	}
}

// SA5. A task the model can come back for keeps its answer in memory
// until it is collected. A task a client started does not: that answer is
// already in the child session's own durable log, and a second copy with
// no reader and no deletion is how a long-running daemon grows.
func TestAClientSpawnedTaskLeavesNoCollectableResultBehind(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	taskID, err := tasks.Spawn(sid, "explore", "say one")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitForTaskStatus(t, loop, sid, taskID)

	tasks.mu.Lock()
	results, waiters := len(tasks.results), len(tasks.waiters)
	tasks.mu.Unlock()
	if results != 0 || waiters != 0 {
		t.Fatalf("after a client-spawned task finished: %d results and %d waiters retained, want none", results, waiters)
	}
}

// SA4. Cancelling a collection must not lose the answer. The task keeps
// running — it belongs to the session, not to the turn that walked away —
// so the id has to stay outstanding, or the work is done and permanently
// unreachable.
func TestACancelledCollectionCanBeCollectedAgain(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)
	if res := launch(t, loop.Tools, ctx, "explore", "say first-slow"); res.IsError {
		t.Fatalf("launch: %s", res.Content)
	}

	// A turn that gives up before the task is done.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if res := loop.Tools.Call(cancelled, "TaskCollect", nil, ""); res.IsError {
		t.Fatalf("collect after cancellation reported an error: %s", res.Content)
	}

	// The task is still going and still outstanding.
	if got := len(tasks.peekPending(sid, "")); got != 1 {
		t.Fatalf("%d tasks outstanding after a cancelled collection, want the one that never finished", got)
	}
	res := loop.Tools.Call(ctx, "TaskCollect", nil, "")
	if res.IsError {
		t.Fatalf("second collect: %s", res.Content)
	}
	if !strings.Contains(res.Content, "first-slow") {
		t.Errorf("the answer was not recoverable after a cancelled collection: %q", res.Content)
	}
	if got := len(tasks.peekPending(sid, "")); got != 0 {
		t.Errorf("%d tasks still outstanding after a successful collection, want none", got)
	}
}

// The same, asked for by id rather than as "collect everything" — the
// path a retrying model takes when it kept the handle.
func TestACancelledCollectionByIDStaysOutstanding(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)
	res := launch(t, loop.Tools, ctx, "explore", "say first-slow")
	if res.IsError {
		t.Fatalf("launch: %s", res.Content)
	}
	taskID := taskIDFrom(t, res.Content)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	input, _ := json.Marshal(map[string]string{"task_id": taskID})
	loop.Tools.Call(cancelled, "TaskCollect", input, "")

	if got := len(tasks.peekPending(sid, "")); got != 1 {
		t.Fatalf("%d outstanding, want the task to still be collectable by id", got)
	}
	again := loop.Tools.Call(ctx, "TaskCollect", input, "")
	if again.IsError || !strings.Contains(again.Content, "first-slow") {
		t.Errorf("collecting by id after a cancellation gave %q", again.Content)
	}
}

// SA3, the delegation half. A background task is admitted while Smart
// Agent is on, and may sit on the semaphore for as long as the queue
// takes. What it must not do is resolve its agent again on the way in: an
// "explore" specialist admitted with a read-only allowlist would otherwise
// start with the full tool set, including write and shell, because the
// roster it came from no longer exists.
func TestAQueuedSpecialistKeepsTheStateItWasAdmittedUnder(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	launchCtx := WithSessionID(context.Background(), sid)
	child := tasks.childContext(launchCtx, "")

	// Between admission and the task starting, the switch goes off.
	loop.SetSmartAgentEnabled(false)

	cfg := loop.agentConfig(child, "explore")
	if len(cfg.Tools) == 0 {
		t.Fatal("the queued explore task resolved to an unrestricted agent after Smart Agent was turned off")
	}
	allowed := loop.toolsForTurn(child, cfg)
	for _, name := range []string{"write_file", "edit", "bash"} {
		if tools.IsAllowed(allowed, name) {
			t.Errorf("%s became available to a task admitted as read-only", name)
		}
	}
	if !tools.IsAllowed(allowed, "read_file") {
		t.Error("read_file was refused, so the snapshot lost the allowlist rather than pinning it")
	}
	// And a turn admitted after the switch went off gets the new state,
	// which is what "live" is supposed to mean.
	fresh := loop.pinSmart(context.Background())
	if len(loop.agentConfig(fresh, "explore").Tools) != 0 {
		t.Error("a new turn still sees the specialists after Smart Agent was turned off")
	}
}

// SA3A. Automatic delegation and any direct SpawnSync run before
// sendWithModelText and can wait on nothing of their own, so the pin has
// to be taken where the delegation is accepted rather than where the child
// eventually starts. A switch flipped while the child was on its way used
// to reach it.
func TestSynchronousDelegationPinsAtAdmissionNotAtArrival(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, _ := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// An unpinned context, which is what an auto-delegated turn and a
	// direct SpawnSync both arrive with.
	admitted := loop.pinSmart(context.Background())
	loop.SetSmartAgentEnabled(false)

	if !loop.smartOn(admitted) {
		t.Fatal("work admitted while Smart Agent was on lost its state when the switch went off")
	}
	if len(loop.agentConfig(admitted, "explore").Tools) == 0 {
		t.Error("the specialist admitted under the old state resolved to an unrestricted agent")
	}
}

// R2N1. A collectable task cancelled while still queued wrote "cancelled"
// into the parent's log and returned without recording a terminal outcome,
// so its waiter was never closed. The panel showed the task as finished
// and TaskCollect blocked on it until the collecting turn's own context
// expired.
func TestATaskCancelledWhileQueuedStillWakesItsCollector(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)
	// One slot, so the second task cannot start.
	tasks.sem = make(chan struct{}, 1)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)

	// Fill the slot with something slow, then queue behind it.
	if res := launch(t, loop.Tools, ctx, "explore", "say first-slow"); res.IsError {
		t.Fatalf("launch: %s", res.Content)
	}
	queued := launch(t, loop.Tools, ctx, "explore", "say second")
	if queued.IsError {
		t.Fatalf("launch: %s", queued.Content)
	}
	queuedID := taskIDFrom(t, queued.Content)

	if !tasks.Cancel(queuedID) {
		t.Fatal("the queued task could not be cancelled")
	}

	// The collector must come back on its own. A context with a deadline
	// would hide the bug by ending the wait for the wrong reason, so this
	// is a plain background context and the test times out if the waiter
	// is never closed.
	done := make(chan tools.Result, 1)
	go func() { done <- loop.Tools.Call(ctx, "TaskCollect", nil, "") }()
	select {
	case res := <-done:
		if !strings.Contains(res.Content, queuedID) {
			t.Errorf("the cancelled task is missing from the collection: %q", res.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TaskCollect never returned: a task cancelled before it started left its waiter open")
	}

	tasks.mu.Lock()
	waiters := len(tasks.waiters)
	tasks.mu.Unlock()
	if waiters != 0 {
		t.Errorf("%d waiters retained after every task reached a terminal state", waiters)
	}
}

// R2N2. Deleting a parent used to drop the recorded results and the
// pending list but leave the waiters, so a child that finished afterwards
// found its waiter, stored a full answer, and left it there with nothing
// that could collect or delete it. A second unbounded retention path,
// distinct from the client-spawned one.
func TestDeletingAParentLeavesNoOrphanResultBehind(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)

	// Deleted while the child is still running, which is the case that
	// leaked: cleanup runs first, the answer arrives afterwards.
	res := launch(t, loop.Tools, ctx, "explore", "say first-slow")
	if res.IsError {
		t.Fatalf("launch: %s", res.Content)
	}
	taskID := taskIDFrom(t, res.Content)
	loop.ClearSessionState(sid)
	waitForTaskStatus(t, loop, sid, taskID)

	tasks.mu.Lock()
	results, waiters, pending := len(tasks.results), len(tasks.waiters), len(tasks.pending)
	tasks.mu.Unlock()
	if results != 0 || waiters != 0 || pending != 0 {
		t.Errorf("after deleting the parent: %d results, %d waiters, %d pending — want nothing retained",
			results, waiters, pending)
	}
}

// And the same when the child has already finished, so cleanup is not
// merely winning a race.
func TestDeletingAParentAfterItsChildFinishedLeavesNothing(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)
	res := launch(t, loop.Tools, ctx, "explore", "say one")
	if res.IsError {
		t.Fatalf("launch: %s", res.Content)
	}
	waitForTaskStatus(t, loop, sid, taskIDFrom(t, res.Content))
	loop.ClearSessionState(sid)

	tasks.mu.Lock()
	results, waiters, pending := len(tasks.results), len(tasks.waiters), len(tasks.pending)
	tasks.mu.Unlock()
	if results != 0 || waiters != 0 || pending != 0 {
		t.Errorf("after deleting the parent of a finished task: %d results, %d waiters, %d pending",
			results, waiters, pending)
	}
}

// R2N3. SpawnSync used to hold a task-manager slot for the whole child
// turn. A child that delegates synchronously again then waits for a slot
// its own ancestor is holding, and at the documented default of one slot
// that is not a race but a certainty: the depth limit permits three levels
// and the semaphore stopped at two.
//
// Smart Agent is off here on purpose. The built-in specialists cannot
// reach this path because they have no delegation tools, but the depth
// guard is documented as general protection for user-defined agents, and
// those are what deadlocked.
func TestNestedSynchronousDelegationCompletesAtTheDefaultConcurrency(t *testing.T) {
	var mu sync.Mutex
	round := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		n := round
		round++
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		// Two levels of delegation, then an answer. The third turn is the
		// one that could never start.
		if n < 2 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"Task","arguments":"{\"agent\":\"deep\",\"prompt\":\"go deeper\"}"}}]}}]}`+"\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"bottom\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	registry := tools.NewRegistry(nil)
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL}},
		Profiles:       map[string]config.Profile{"only": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"top": {Description: "the caller"}, "deep": {Description: "delegates again"}},
		DefaultProfile: "only",
	}
	loop := New(store, registry, map[string]provider.Provider{"local": provider.NewOpenAICompat(srv.URL, "")}, cfg)
	// Zero is the documented default and the manager clamps it to one
	// slot, which is exactly the configuration that deadlocked.
	tasks := NewTaskManager(context.Background(), loop, 0)
	if cap(tasks.sem) != 1 {
		t.Fatalf("the manager has %d slots, want the single-slot default this is about", cap(tasks.sem))
	}
	registry.Register(NewTaskTool(tasks, loop.DelegatableAgents))

	if _, err := loop.Store.CreateSession("s1", "", "top", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- loop.SendMessage(context.Background(), "s1", "top", "start") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("nested synchronous delegation failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("nested synchronous delegation deadlocked: the grandchild is waiting for a slot its ancestor holds")
	}

	// And the slot is not leaked on the way back out.
	if len(tasks.sem) != 0 {
		t.Errorf("%d slots still held after the turn finished", len(tasks.sem))
	}
}

// R3N2. A waiter can be woken by two different things and the caller has
// to be able to tell them apart. Cleanup used to close the channel without
// recording anything, and Wait read the missing map entry as a zero value:
// empty text, no error, collected. A collection could therefore report
// "finished without an answer" about a task that was never asked, which
// reads as a task that had nothing to say.
func TestCleanupWakesAWaiterWithACleanupOutcomeNotAnEmptyAnswer(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)
	res := launch(t, loop.Tools, ctx, "explore", "say first-slow")
	if res.IsError {
		t.Fatalf("launch: %s", res.Content)
	}
	taskID := taskIDFrom(t, res.Content)

	type waitResult struct {
		text      string
		err       error
		collected bool
	}
	got := make(chan waitResult, 1)
	go func() {
		text, err, collected := tasks.Wait(context.Background(), taskID)
		got <- waitResult{text, err, collected}
	}()

	// Give the goroutine time to be blocked on the channel rather than
	// racing past it, then delete the session out from under it.
	time.Sleep(20 * time.Millisecond)
	tasks.forgetSession(sid)

	select {
	case r := <-got:
		if r.err == nil {
			t.Fatalf("cleanup was reported as a successful outcome: text=%q collected=%v", r.text, r.collected)
		}
		if !errors.Is(r.err, errTaskDetached) {
			t.Errorf("cleanup returned %v, want the detached outcome", r.err)
		}
		if r.text != "" {
			t.Errorf("cleanup returned an answer %q", r.text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup never woke the waiter")
	}
}

// The other half: a real completion still reads as a real completion, so
// the fix above cannot be satisfied by calling everything detached.
func TestARealAnswerIsStillReportedAsOne(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := WithSessionID(context.Background(), sid)
	res := launch(t, loop.Tools, ctx, "explore", "say hello")
	if res.IsError {
		t.Fatalf("launch: %s", res.Content)
	}
	text, err, collected := tasks.Wait(context.Background(), taskIDFrom(t, res.Content))
	if err != nil || !collected || text != "hello" {
		t.Errorf("Wait = (%q, %v, %v), want the answer", text, err, collected)
	}
}

// R3N3. The background path validates the parent first and deletes the
// child if the append fails. The synchronous path did neither, and its own
// callers include the API and an embedding program, so "the Task tool
// always has a valid parent" was never an invariant it could rely on.
func TestASynchronousDelegationAgainstAMissingParentLeavesNoChild(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)

	before := len(loop.Store.AllSessions())
	if _, err := tasks.SpawnSync(context.Background(), "nope", "explore", "say one"); err == nil {
		t.Fatal("SpawnSync succeeded against a parent that does not exist")
	}
	if after := len(loop.Store.AllSessions()); after != before {
		t.Errorf("%d session(s) created by a failed delegation, want none", after-before)
	}

	// Repeated attempts must not accumulate, which is the shape the
	// consequence took: one invisible orphan per call.
	for range 5 {
		tasks.SpawnSync(context.Background(), "nope", "explore", "say one")
	}
	if after := len(loop.Store.AllSessions()); after != before {
		t.Errorf("%d session(s) accumulated over six failed delegations", after-before)
	}
}
