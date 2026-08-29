package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/trace"
)

// TaskManager spawns and tracks background agent sessions ("tasks") on
// behalf of a parent session. Each task is itself a session (visible:false)
// running the same Loop concurrently, bounded by a semaphore so a burst of
// spawns can't overrun provider rate limits. Status changes are mirrored
// into the parent session's event log (task.spawned / task.status) so any
// client watching the parent sees background progress without polling.
type TaskManager struct {
	loop    *Loop
	sem     chan struct{}
	rootCtx context.Context

	mu      sync.Mutex
	counter int
	cancels map[string]context.CancelFunc

	// Background bookkeeping — see background.go. A task spawned by the
	// model rather than by a client is one somebody intends to come back
	// for, so its answer has to still be there when they do: the parent's
	// event log records that it finished, not what it said.
	waiters map[string]chan struct{}
	results map[string]taskOutcome
	pending map[string][]string

	// done is closed when a task's goroutine has unwound, for every task
	// rather than only the collectable ones. Deleting a session has to
	// wait for its background work to actually stop before the session's
	// files go away, and "the cancel func is gone" is not that signal:
	// the cancel is removed on the way out, while the turn may still be
	// finishing a tool call and appending to the log.
	done map[string]chan struct{}
}

func NewTaskManager(rootCtx context.Context, loop *Loop, maxConcurrent int) *TaskManager {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	tm := &TaskManager{
		loop:    loop,
		sem:     make(chan struct{}, maxConcurrent),
		rootCtx: rootCtx,
		cancels: map[string]context.CancelFunc{},
		waiters: map[string]chan struct{}{},
		results: map[string]taskOutcome{},
		pending: map[string][]string{},
		done:    map[string]chan struct{}{},
	}
	// Back-reference so the loop can delegate a turn on its own (see
	// Loop.delegatePrompt) rather than only when the model calls the Task
	// tool. Loop.Tasks stays nil for a Loop built without a task manager.
	loop.Tasks = tm
	return tm
}

func (tm *TaskManager) nextTaskID() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.counter++
	return fmt.Sprintf("task-%d-%d", time.Now().UnixNano(), tm.counter)
}

// Spawn creates a child session under parentSessionID and runs agentName's
// profile against prompt in the background. It returns immediately with the
// new session's id; progress is reported via task.status events appended to
// the parent session.
func (tm *TaskManager) Spawn(parentSessionID, agentName, prompt string) (string, error) {
	if !tm.loop.lifecycle.admit(parentSessionID) {
		return "", errSessionClosing(parentSessionID)
	}
	defer tm.loop.lifecycle.admitted(parentSessionID)
	return tm.spawn(context.Background(), parentSessionID, agentName, prompt, "", false)
}

// spawn is Spawn for a launch that came from inside a turn.
//
// launchCtx is the launching turn's context. The child does not run under
// it — a background task deliberately outlives the turn that started it,
// and inheriting its cancellation would end the task the moment the turn
// did — so what the child gets is a fresh context off rootCtx carrying the
// two things that must survive the handover:
//
//   - The delegation depth. It used to be left behind, which made
//     background delegation the way around the depth limit: every
//     generation started at zero, so an agent with unrestricted tools
//     could launch a child that launched a child, without bound. The
//     eight-task ceiling is per parent session and does not bound a tree.
//   - The Smart Agent snapshot, taken here at admission rather than when
//     the task is dequeued. A task waiting on the semaphore used to
//     resolve its agent again on the way in, so an "explore" specialist
//     admitted as read-only started with the full tool set if the switch
//     had gone off in between.
//
// traceID travels as a value for the same handover reason. Empty means
// "start a new trace", which is what a client-initiated task is.
// The caller must already hold an admission window for parentSessionID
// (lifecycle.admit). Everything from the parent check to registering the
// goroutine happens inside it, which is what makes a delete either see
// this child or refuse it, and never neither.
func (tm *TaskManager) spawn(launchCtx context.Context, parentSessionID, agentName, prompt, traceID string, collectable bool) (string, error) {
	childCtx := tm.childContext(launchCtx, traceID)

	// A hook for the tests that force the interleaving this admission
	// window exists to close: a spawn that has passed the parent check but
	// has not yet registered its goroutine. Nil in every build that is not
	// running those tests.
	if spawnBarrier != nil {
		spawnBarrier()
	}

	// Checked before anything is created. The child session used to be
	// created first, so spawning against a parent that does not exist
	// failed at the append and left the child behind — invisible (tasks
	// are not listed) and permanent, once per attempt.
	if _, err := tm.loop.Store.Get(parentSessionID); err != nil {
		return "", fmt.Errorf("spawn task: %w", err)
	}

	if blocked, reason := tm.loop.delegateBlocked(childCtx, parentSessionID, agentName, prompt); blocked {
		return "", fmt.Errorf("delegation to %q was refused: %s", agentName, reason)
	}

	taskID := tm.nextTaskID()
	if _, err := tm.loop.Store.CreateSessionIn(taskID, parentSessionID, agentName, tm.childWorkspace(parentSessionID), false); err != nil {
		return "", fmt.Errorf("create task session: %w", err)
	}

	if _, err := tm.loop.Store.Append(parentSessionID, events.TypeTaskSpawned, map[string]any{
		"task_id": taskID,
		"agent":   agentName,
		"prompt":  prompt,
	}); err != nil {
		// Nothing is watching this child and nothing ever will be.
		tm.loop.Store.Delete(taskID)
		return "", fmt.Errorf("append task.spawned: %w", err)
	}
	tm.loop.traceSpan(childCtx, traceID, parentSessionID, trace.SpanDelegate, trace.Record{
		Agent: agentName, Detail: "background -> " + taskID + ", " + describeTask(prompt),
	})

	ctx, cancel := context.WithCancel(childCtx)
	tm.mu.Lock()
	tm.done[taskID] = make(chan struct{})
	tm.cancels[taskID] = cancel
	// Only a task somebody can come back for gets result bookkeeping. A
	// client-spawned task reads its answer out of the child session's own
	// durable log and never calls Wait, so keeping a second copy in memory
	// was a copy with no reader and no deletion: a long-running daemon
	// grew one per task it had ever run.
	if collectable {
		tm.waiters[taskID] = make(chan struct{})
	}
	tm.mu.Unlock()

	go tm.run(ctx, taskID, parentSessionID, agentName, prompt)

	return taskID, nil
}

// childContext is what a background child runs under: rooted in the
// manager's own context rather than in the launching turn's, and carrying
// forward the two things that must survive that handover.
//
// Separate from spawn so the handover can be asserted directly. What it
// carries is easy to state and was easy to lose.
// spawnBarrier is test-only. See spawn.
var spawnBarrier func()

func (tm *TaskManager) childContext(launchCtx context.Context, traceID string) context.Context {
	ctx := trace.WithID(tm.rootCtx, traceID)
	ctx = withTaskDepth(ctx, taskDepthFromContext(launchCtx)+1)
	return config.WithSmartAgent(ctx, tm.loop.Config.SmartAgentFor(launchCtx))
}

// childWorkspace is the directory a task works in: the one its parent is
// working in, resolved at spawn and stamped onto the child session.
//
// The third thing that has to survive the handover, and the one that was
// being dropped. A task session was created with no workspace at all, so
// SessionDir fell through to the daemon's default for every one of them,
// and that default is wherever the daemon was started. The moment a
// conversation was anywhere else — a workspace switched from the header, a
// session reopened in the project it was created in, a second client on
// the same daemon working in a second checkout — the agents it delegated
// to read and wrote in a different project from the one that asked. The
// implement agent is the sharp end of it: told to change a file it
// resolves by relative path, it changed the other project's copy, and
// reported back that it was done.
//
// Resolved now rather than looked up per turn on the parent, because a
// task is given its instructions in a particular directory and should
// finish them there: the parent moving afterwards moves the parent, and
// the child goes on working where it was sent. Stamping is also what
// makes it survive a restart, and what makes nesting need no code of its
// own, since a task that delegates is by then a parent with a workspace
// of its own to pass down.
func (tm *TaskManager) childWorkspace(parentSessionID string) string {
	return tm.loop.SessionDir(parentSessionID)
}

func (tm *TaskManager) run(ctx context.Context, taskID, parentSessionID, agentName, prompt string) {
	// Registered first so it runs last: everything below has finished,
	// including the terminal outcome, by the time this closes. That is
	// what StopSession waits on before a session's files are removed, so
	// it has to mean the goroutine is done writing, not merely done
	// running. "The cancel func is gone" would not mean that.
	defer func() {
		tm.mu.Lock()
		if ch, ok := tm.done[taskID]; ok {
			close(ch)
			delete(tm.done, taskID)
		}
		tm.mu.Unlock()
	}()

	// Every way out of this function is terminal for the task, so every
	// way out has to wake whoever is waiting for it. finish is
	// first-call-wins (it takes the waiter with it), so the normal path's
	// own call still decides the outcome and this only covers the exits
	// that had none.
	//
	// The exit that had none was cancellation before a semaphore slot came
	// free: the parent's log said "cancelled", the panel showed the task
	// as finished, and TaskCollect went on blocking on a channel nothing
	// would ever close.
	defer tm.finish(taskID, "", nil, context.Canceled)

	defer func() {
		tm.mu.Lock()
		cancel := tm.cancels[taskID]
		delete(tm.cancels, taskID)
		tm.mu.Unlock()
		// Called, not merely dropped: a context with a live cancel func
		// stays attached to its parent, so a long-running daemon
		// accumulated one per task it had ever run.
		if cancel != nil {
			cancel()
		}
	}()

	select {
	case tm.sem <- struct{}{}:
		defer func() { <-tm.sem }()
	case <-ctx.Done():
		tm.loop.Store.Append(parentSessionID, events.TypeTaskStatus, map[string]any{
			"task_id": taskID,
			"status":  "cancelled",
		})
		return
	}

	tm.loop.Store.Append(parentSessionID, events.TypeTaskStatus, map[string]any{
		"task_id": taskID,
		"status":  "running",
	})

	stopMirror := tm.mirrorProgress(taskID, parentSessionID)
	err := tm.loop.SendMessage(withDelegatedTask(ctx, agentName, prompt), taskID, agentName, prompt)
	stopMirror()

	// Recorded before the status event goes out, so anything woken by that
	// event finds the answer already there rather than racing it.
	tm.finish(taskID, lastAssistantText(tm.loop.Store, taskID), err, ctx.Err())

	data := map[string]any{"task_id": taskID, "status": "completed"}
	switch {
	case ctx.Err() != nil:
		// Stopped on purpose. Reporting that as "failed" with a
		// context-cancelled error underneath made a deliberate stop look
		// like something had gone wrong.
		data["status"] = "cancelled"
	case err != nil:
		data["status"] = "failed"
		data["error"] = err.Error()
	}
	tm.loop.Store.Append(parentSessionID, events.TypeTaskStatus, data)
}

// mirrorProgress echoes what a task is doing into the conversation that
// started it, and returns a function that stops doing so.
//
// A background task's own log is a session nothing lists and nothing
// opens, so the parent's only news of it was three words — "spawned",
// "running", "completed". A task that spends twenty minutes in tools
// looked exactly like a task that was wedged, which is how "1 background
// task" that never finishes came to be the whole progress report.
//
// Broadcast, not Append: this is true only at the moment it is sent, and
// writing a line per tool call into the parent's log would bury the
// conversation it is mirrored into. Clients that are not looking simply
// miss it, which is correct for a progress indicator.
func (tm *TaskManager) mirrorProgress(taskID, parentSessionID string) func() {
	ch, _, unsub, err := tm.loop.Store.Subscribe(taskID)
	if err != nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			doing := ""
			switch ev.Type {
			case events.TypeToolStart:
				name, _ := ev.Data["name"].(string)
				doing = name
			case events.TypeMessagePartEnd:
				doing = "thinking"
			default:
				continue
			}
			tm.loop.Store.Broadcast(parentSessionID, events.TypeTaskProgress, map[string]any{
				"task_id": taskID,
				"doing":   doing,
			})
		}
	}()
	return func() {
		// unsub closes the channel, which ends the goroutine above; waiting
		// for it means no progress line can land after the task's final
		// status and leave the panel claiming it is still working.
		unsub()
		<-done
	}
}

// taskDepthKey tracks how many levels deep a chain of synchronous Task
// delegations (agent A delegates to B, which delegates to C, ...) has
// gone, so TaskTool can refuse to go past maxTaskDepth and guard against a
// misconfigured agent delegating to itself forever.
type taskDepthKey struct{}

const maxTaskDepth = 3

func withTaskDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, taskDepthKey{}, depth)
}

func taskDepthFromContext(ctx context.Context) int {
	d, _ := ctx.Value(taskDepthKey{}).(int)
	return d
}

// SpawnSync runs agentName synchronously in a new child session under
// parentSessionID and returns its final answer text once the turn
// completes. Unlike Spawn (fire-and-forget, polled via task.status
// events), this blocks the caller — it's what backs the Task tool, where
// the delegating agent's own turn needs the sub-agent's answer before it
// can continue.
func (tm *TaskManager) SpawnSync(ctx context.Context, parentSessionID, agentName, prompt string) (string, error) {
	_, text, err := tm.spawnSync(ctx, parentSessionID, "", agentName, prompt)
	return text, err
}

// SpawnSyncInto is SpawnSync for a sub-agent session that already exists:
// another turn in the same child, which still has everything it said
// before. childID may be "", which creates one and returns its id.
//
// A debate is what needs it. A reviewer handed a fresh session every
// round reads the work from scratch every round, and cannot say "the
// second thing I raised is still not fixed" — which is the difference
// between a review and a debate. Reusing the session is also what keeps
// the per-round prompt small: its own findings are already in its
// history, so they are not sent again.
func (tm *TaskManager) SpawnSyncInto(ctx context.Context, parentSessionID, childID, agentName, prompt string) (string, string, error) {
	return tm.spawnSync(ctx, parentSessionID, childID, agentName, prompt)
}

func (tm *TaskManager) spawnSync(ctx context.Context, parentSessionID, childID, agentName, prompt string) (string, string, error) {
	// Pinned here rather than relying on the caller, because the callers
	// are not all turns: the Task tool arrives with its parent's pin and
	// keeps it, but a direct SpawnSync from the API or from an embedding
	// program does not have one, and this call can wait on the semaphore
	// for as long as the queue takes before the child reads the setting.
	// Admission is when the delegation was accepted, so admission is what
	// it runs under.
	ctx = tm.loop.pinSmart(ctx)

	if blocked, reason := tm.loop.delegateBlocked(ctx, parentSessionID, agentName, prompt); blocked {
		return "", "", fmt.Errorf("delegation to %q was refused: %s", agentName, reason)
	}

	// Admission, the same window the background path takes, for the same
	// reason and one more. The same: a parent being deleted must not
	// acquire a new child. The one more: this path is reachable directly
	// from the API and from an embedding program, so unlike the Task tool
	// it has no turn holding the session for it.
	if !tm.loop.lifecycle.admit(parentSessionID) {
		return "", "", errSessionClosing(parentSessionID)
	}

	// Checked before anything is created, and rolled back if the parent
	// stops accepting between the two. The background path has done this
	// since the same bug was found there: creating the child first meant a
	// spawn against a parent that does not exist failed at the append and
	// left the child behind, invisible (tasks are not listed) and
	// permanent, once per attempt. The synchronous path had neither half,
	// and "the Task tool always has a valid parent" is not an invariant
	// this function can rely on.
	if _, err := tm.loop.Store.Get(parentSessionID); err != nil {
		tm.loop.lifecycle.admitted(parentSessionID)
		return "", "", fmt.Errorf("spawn task: %w", err)
	}

	taskID := childID
	switch {
	case taskID == "":
		taskID = tm.nextTaskID()
		if _, err := tm.loop.Store.CreateSessionIn(taskID, parentSessionID, agentName, tm.childWorkspace(parentSessionID), false); err != nil {
			tm.loop.lifecycle.admitted(parentSessionID)
			return "", "", fmt.Errorf("create task session: %w", err)
		}
		if _, err := tm.loop.Store.Append(parentSessionID, events.TypeTaskSpawned, map[string]any{
			"task_id": taskID,
			"agent":   agentName,
			"prompt":  prompt,
		}); err != nil {
			// Nothing is watching this child and nothing ever will be.
			tm.loop.Store.Delete(taskID)
			tm.loop.lifecycle.admitted(parentSessionID)
			return "", "", fmt.Errorf("append task.spawned: %w", err)
		}
	default:
		// Resuming a child that already exists: no second task.spawned,
		// because the row a client is already showing is this one, and a
		// second spawn event would give one sub-agent two of them. It is
		// checked rather than assumed — the caller holds an id from an
		// earlier call, and the session behind it can have been deleted
		// since.
		if _, err := tm.loop.Store.Get(taskID); err != nil {
			tm.loop.lifecycle.admitted(parentSessionID)
			return "", "", fmt.Errorf("resume task session: %w", err)
		}
	}
	// The parent's record of what it delegated. On the delegate span
	// rather than in the parent's prompt manifest: the task text is not
	// in the parent's next request, and a manifest entry claiming it was
	// would describe a request nobody sent. The child's own manifest is
	// where the text is named, because the child's request is where the
	// text is.
	tm.loop.traceSpan(ctx, trace.ID(ctx), parentSessionID, trace.SpanDelegate, trace.Record{
		Agent: agentName, Detail: "synchronous -> " + taskID + ", " + describeTask(prompt),
	})

	// Registered like a background task, even though this one runs on the
	// caller's own goroutine, so a deletion can cancel it and wait for it
	// rather than only knowing about the tasks it launched itself. The
	// registration is the last thing inside the admission window: after
	// this the child is visible to a claim.
	ctx, cancel := context.WithCancel(ctx)
	syncDone := make(chan struct{})
	tm.mu.Lock()
	tm.cancels[taskID] = cancel
	tm.done[taskID] = syncDone
	tm.mu.Unlock()
	tm.loop.lifecycle.admitted(parentSessionID)

	defer func() {
		tm.mu.Lock()
		delete(tm.cancels, taskID)
		delete(tm.done, taskID)
		tm.mu.Unlock()
		cancel()
		close(syncDone)
	}()

	// No semaphore here, deliberately.
	//
	// It used to take a slot and hold it for the whole child turn, which
	// deadlocks the moment that child delegates synchronously again: the
	// grandchild waits for a slot its own ancestor is holding while that
	// ancestor waits for the grandchild. At the documented default of one
	// slot this is not a race, it is certain, and the depth limit does not
	// help — it permits three levels and the semaphore stops at two.
	//
	// The right fix is not a bigger default. What the semaphore bounds is
	// a burst of spawns overrunning a provider's rate limit, and a
	// synchronous delegation cannot burst: its caller is blocked at the
	// tool boundary, so one turn has at most one synchronous child at a
	// time and a chain is at most maxTaskDepth deep, with only the
	// deepest link actually calling a model. The concurrency that reaches
	// the provider is therefore the number of concurrent top-level turns,
	// which was never gated by this semaphore either. Background
	// delegation is the one that fans out, and it still queues here.
	if err := ctx.Err(); err != nil {
		tm.loop.Store.Append(parentSessionID, events.TypeTaskStatus, map[string]any{"task_id": taskID, "status": "cancelled"})
		return taskID, "", err
	}

	tm.loop.Store.Append(parentSessionID, events.TypeTaskStatus, map[string]any{"task_id": taskID, "status": "running"})

	// The task travels with the child's own turn, so the request that
	// actually contains it is the one whose manifest names it.
	err := tm.loop.SendMessage(withDelegatedTask(ctx, agentName, prompt), taskID, agentName, prompt)
	if err != nil {
		tm.loop.Store.Append(parentSessionID, events.TypeTaskStatus, map[string]any{
			"task_id": taskID, "status": "failed", "error": err.Error(),
		})
		return taskID, "", err
	}

	tm.loop.Store.Append(parentSessionID, events.TypeTaskStatus, map[string]any{"task_id": taskID, "status": "completed"})
	return taskID, lastAssistantText(tm.loop.Store, taskID), nil
}

// lastAssistantText finds the most recent message.part.end event in a
// session's log and returns its accumulated text — the sub-agent's final
// answer for the turn that just completed.
func lastAssistantText(store *session.Store, sessionID string) string {
	all, err := store.Events(sessionID, 0)
	if err != nil {
		return ""
	}
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Type == events.TypeMessagePartEnd {
			text, _ := all[i].Data["text"].(string)
			return text
		}
	}
	return ""
}

// Cancel stops a running task, if it's still running. Returns false if the
// task id is unknown (already finished or never existed).
func (tm *TaskManager) Cancel(taskID string) bool {
	tm.mu.Lock()
	cancel, ok := tm.cancels[taskID]
	tm.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// List returns the child task sessions spawned under parentSessionID.
func (tm *TaskManager) List(parentSessionID string) []SessionSummary {
	children := tm.loop.Store.Children(parentSessionID)
	out := make([]SessionSummary, 0, len(children))
	for _, c := range children {
		out = append(out, SessionSummary{
			ID:        c.ID,
			Agent:     c.Agent,
			CreatedAt: c.CreatedAt,
		})
	}
	return out
}

// SessionSummary is the daemon/client-facing view of a task's session
// metadata (deliberately narrower than session.Session to keep the API
// surface stable if internal fields change).
type SessionSummary struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	CreatedAt time.Time `json:"created_at"`
}

// describeTask says what a delegation handed over without writing the
// text into a trace file.
//
// Identity and size, the same shape a manifest entry carries and for the
// same reason: a task is written by a model that has been reading tool
// output, and a turn log is not where that belongs. The hash is what
// makes "the same task, twice" answerable, and the size is what makes a
// runaway one visible.
func describeTask(task string) string {
	sum := sha256.Sum256([]byte(task))
	return fmt.Sprintf("task %s, %d chars", hex.EncodeToString(sum[:4]), len([]rune(task)))
}

// childAgent names the agent a task ran as, for a record that has only
// the task id. Falls back to the id itself, because a record naming the
// task is still better than one naming nothing.
func (tm *TaskManager) childAgent(taskID string) string {
	if agent := tm.loop.sessionAgent(taskID); agent != "" {
		return agent
	}
	return taskID
}
