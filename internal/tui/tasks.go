package tui

import (
	"fmt"
	"sort"
	"strings"
)

// taskState is what the TUI knows about one background task, entirely
// from the parent session's events.
type taskState struct {
	agent  string
	status string // spawned | running | completed | failed | cancelled
	prompt string
}

// active reports whether the task is still doing work.
func (t taskState) active() bool { return t.status == "spawned" || t.status == "running" }

// orchestrateStage reports whether a task id is an orchestration stage
// progress report rather than a task. The daemon reports stage progress
// on the task.status channel with task_id "orchestrate:<stage>" (see
// orchestrate_run.go), and that id names a report, not a session: there
// is no conversation behind it to open, inspect, or cancel.
func orchestrateStage(taskID string) (string, bool) {
	if stage, ok := strings.CutPrefix(taskID, "orchestrate:"); ok {
		return stage, true
	}
	return "", false
}

// stageLine renders one orchestration stage report as the transcript
// line it is: a running stage names how many agents it launched, a
// finished one how many answers it kept. Anything else falls back to
// the status word itself, so a future stage state is still legible
// rather than silently dropped.
func stageLine(stage, status string, data map[string]any) string {
	switch status {
	case "running":
		if n := intField(data, "agents"); n == 1 {
			return fmt.Sprintf("[orchestrate: %s running (1 agent)]", stage)
		}
		return fmt.Sprintf("[orchestrate: %s running (%d agents)]", stage, intField(data, "agents"))
	case "completed":
		return fmt.Sprintf("[orchestrate: %s finished (%d kept)]", stage, intField(data, "kept"))
	default:
		if status == "" {
			return fmt.Sprintf("[orchestrate: %s]", stage)
		}
		return fmt.Sprintf("[orchestrate: %s %s]", stage, status)
	}
}

// tasksSummary renders the /tasks listing from the state built out of
// task.spawned/task.status events — no server round trip needed for the
// list itself; /tasks <id> fetches that task's output.
func (m Model) tasksSummary() string {
	if len(m.tasks) == 0 {
		return "No background tasks in this session."
	}
	ids := make([]string, 0, len(m.tasks))
	for id := range m.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("Background tasks (/tasks <id> for its output so far):\n")
	for _, id := range ids {
		t := m.tasks[id]
		fmt.Fprintf(&b, "- %s [%s] %s: %s\n", id, t.status, t.agent, t.prompt)
	}
	return strings.TrimRight(b.String(), "\n")
}
