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
