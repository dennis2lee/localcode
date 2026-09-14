package tui

import (
	"strings"
	"testing"

	"localcode/internal/events"
)

// The daemon reports orchestration stage progress on the task.status
// channel with task_id "orchestrate:<stage>". That id is a report, not
// a session, and filing it as a task drew a row out of a zero-value
// task — empty agent, empty prompt — with output to inspect behind a
// session id that does not exist. Stage progress is a transcript line.
func TestOrchestrateStageProgressIsNotATask(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeTaskStatus, Data: map[string]any{
		"task_id": "orchestrate:find", "status": "running", "stage": "find", "agents": 4,
	}})
	m.applyEvent(events.Event{Type: events.TypeTaskStatus, Data: map[string]any{
		"task_id": "orchestrate:find", "status": "completed", "stage": "find", "kept": 2,
	}})

	if _, ok := m.tasks["orchestrate:find"]; ok {
		t.Fatal("stage progress created a task row for a session that does not exist")
	}
	if len(m.tasks) != 0 {
		t.Fatalf("stage progress created task rows: %v", m.tasks)
	}
	var lines []string
	for _, e := range m.transcript {
		lines = append(lines, e.text)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "orchestrate: find running (4 agents)") {
		t.Errorf("no running line for the stage; transcript holds: %q", joined)
	}
	if !strings.Contains(joined, "orchestrate: find finished (2 kept)") {
		t.Errorf("no finished line for the stage; transcript holds: %q", joined)
	}
}

// A spawned event for a stage id must not seed the row either — the
// daemon sends none today, but the row must not depend on that staying
// true.
func TestOrchestrateStageSpawnIsNotATask(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeTaskSpawned, Data: map[string]any{
		"task_id": "orchestrate:find", "agent": "explore", "prompt": "find things",
	}})
	if len(m.tasks) != 0 {
		t.Fatalf("a stage spawn created task rows: %v", m.tasks)
	}
}

// Ordinary tasks are untouched by the stage rule: spawned and status
// still build the row /tasks inspects.
func TestOrdinaryTasksStillBuildRows(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeTaskSpawned, Data: map[string]any{
		"task_id": "task-1", "agent": "explore", "prompt": "find things",
	}})
	m.applyEvent(events.Event{Type: events.TypeTaskStatus, Data: map[string]any{
		"task_id": "task-1", "status": "running",
	}})
	got, ok := m.tasks["task-1"]
	if !ok {
		t.Fatal("an ordinary task.spawned built no row")
	}
	if got.agent != "explore" || got.status != "running" || got.prompt != "find things" {
		t.Errorf("the row lost the task's own fields: %+v", got)
	}
}
