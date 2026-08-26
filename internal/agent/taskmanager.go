package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

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
	return tm.spawn(parentSessionID, agentName, prompt, "")
}

// spawn is Spawn with the caller's trace id, which a background task
// cannot take from a context: it deliberately outlives the turn that
// launched it, so the id travels as a value instead. Empty means "start a
// new trace", which is what a client-initiated task is.
func (tm *TaskManager) spawn(parentSessionID, agentName, prompt, traceID string) (string, error) {
	// Checked before anything is created. The child session used to be
	// created first, so spawning against a parent that does not exist
	// failed at the append and left the child behind — invisible (tasks
	// are not listed) and permanent, once per attempt.
	if _, err := tm.loop.Store.Get(parentSessionID); err != nil {
		return "", fmt.Errorf("spawn task: %w", err)
	}

	if blocked, reason := tm.loop.delegateBlocked(tm.rootCtx, parentSessionID, agentName, prompt); blocked {
		return "", fmt.Errorf("delegation to %q was refused: %s", agentName, reason)
	}

	taskID := tm.nextTaskID()
	if _, err := tm.loop.Store.CreateSession(taskID, parentSessionID, agentName, false); err != nil {
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
	tm.loop.traceSpan(traceID, parentSessionID, trace.SpanDelegate, trace.Record{
		Agent: agentName, Detail: "background -> " + taskID,
	})

	ctx, cancel := context.WithCancel(trace.WithID(tm.rootCtx, traceID))
	tm.mu.Lock()
	tm.cancels[taskID] = cancel
	tm.waiters[taskID] = make(chan struct{})
	tm.mu.Unlock()

	go tm.run(ctx, taskID, parentSessionID, agentName, prompt)

	return taskID, nil
}

func (tm *TaskManager) run(ctx context.Context, taskID, parentSessionID, agentName, prompt string) {
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
	err := tm.loop.SendMessage(ctx, taskID, agentName, prompt)
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
	if blocked, reason := tm.loop.delegateBlocked(ctx, parentSessionID, agentName, prompt); blocked {
		return "", fmt.Errorf("delegation to %q was refused: %s", agentName, reason)
	}

	taskID := tm.nextTaskID()

	if _, err := tm.loop.Store.CreateSession(taskID, parentSessionID, agentName, false); err != nil {
		return "", fmt.Errorf("create task session: %w", err)
	}
	if _, err := tm.loop.Store.Append(parentSessionID, events.TypeTaskSpawned, map[string]any{
		"task_id": taskID,
		"agent":   agentName,
		"prompt":  prompt,
	}); err != nil {
		return "", fmt.Errorf("append task.spawned: %w", err)
	}
	tm.loop.traceSpan(trace.ID(ctx), parentSessionID, trace.SpanDelegate, trace.Record{
		Agent: agentName, Detail: "synchronous -> " + taskID,
	})

	select {
	case tm.sem <- struct{}{}:
		defer func() { <-tm.sem }()
	case <-ctx.Done():
		tm.loop.Store.Append(parentSessionID, events.TypeTaskStatus, map[string]any{"task_id": taskID, "status": "cancelled"})
		return "", ctx.Err()
	}

	tm.loop.Store.Append(parentSessionID, events.TypeTaskStatus, map[string]any{"task_id": taskID, "status": "running"})

	err := tm.loop.SendMessage(ctx, taskID, agentName, prompt)
	if err != nil {
		tm.loop.Store.Append(parentSessionID, events.TypeTaskStatus, map[string]any{
			"task_id": taskID, "status": "failed", "error": err.Error(),
		})
		return "", err
	}

	tm.loop.Store.Append(parentSessionID, events.TypeTaskStatus, map[string]any{"task_id": taskID, "status": "completed"})
	return lastAssistantText(tm.loop.Store, taskID), nil
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
