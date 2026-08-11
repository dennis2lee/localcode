package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"localcode/internal/events"
	"localcode/internal/session"
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
	// Checked before anything is created. The child session used to be
	// created first, so spawning against a parent that does not exist
	// failed at the append and left the child behind — invisible (tasks
	// are not listed) and permanent, once per attempt.
	if _, err := tm.loop.Store.Get(parentSessionID); err != nil {
		return "", fmt.Errorf("spawn task: %w", err)
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

	ctx, cancel := context.WithCancel(tm.rootCtx)
	tm.mu.Lock()
	tm.cancels[taskID] = cancel
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

	err := tm.loop.SendMessage(ctx, taskID, agentName, prompt)

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
