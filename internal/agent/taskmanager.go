package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"localcode/internal/events"
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
	return &TaskManager{
		loop:    loop,
		sem:     make(chan struct{}, maxConcurrent),
		rootCtx: rootCtx,
		cancels: map[string]context.CancelFunc{},
	}
}

// Spawn creates a child session under parentSessionID and runs agentName's
// profile against prompt in the background. It returns immediately with the
// new session's id; progress is reported via task.status events appended to
// the parent session.
func (tm *TaskManager) Spawn(parentSessionID, agentName, prompt string) (string, error) {
	tm.mu.Lock()
	tm.counter++
	taskID := fmt.Sprintf("task-%d-%d", time.Now().UnixNano(), tm.counter)
	tm.mu.Unlock()

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
		delete(tm.cancels, taskID)
		tm.mu.Unlock()
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

	status := "completed"
	data := map[string]any{"task_id": taskID, "status": status}
	if err != nil {
		data["status"] = "failed"
		data["error"] = err.Error()
	}
	tm.loop.Store.Append(parentSessionID, events.TypeTaskStatus, data)
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
