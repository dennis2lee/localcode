package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"localcode/internal/events"
)

func planCall(t *testing.T, loop *Loop, sid string, body string) string {
	t.Helper()
	ctx := WithSessionID(t.Context(), sid)
	res := NewUpdatePlanTool(loop).Execute(ctx, json.RawMessage(body))
	if res.IsError {
		return "ERROR: " + res.Content
	}
	return res.Content
}

// The list comes back as a list, so the next turn reads its own plan out
// of the tool result rather than reconstructing what it sent.
func TestUpdatePlanRecordsTheChecklist(t *testing.T) {
	loop, sid := testLoop(t, "")
	got := planCall(t, loop, sid, `{"plan":[
		{"step":"read the loader","status":"completed"},
		{"step":"wire the new root","status":"in_progress"},
		{"step":"cover it with a test","status":"pending"}]}`)

	for _, want := range []string{"[x] read the loader", "[>] wire the new root", "[ ] cover it with a test", "1 of 3 done"} {
		if !strings.Contains(got, want) {
			t.Errorf("result lacks %q:\n%s", want, got)
		}
	}

	// Logged, not transient: a plan is read back beside what happened.
	evs, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Type == events.TypePlanUpdated {
			found = true
			steps, _ := ev.Data["plan"].([]any)
			if len(steps) != 3 {
				t.Errorf("the event carries %d steps, want 3", len(steps))
			}
		}
	}
	if !found {
		t.Error("no plan.updated event was written")
	}
}

// The shapes that make a plan misleading are refused, and the refusal
// says which one it is rather than quietly picking a step.
func TestUpdatePlanRefusesTheShapesThatMislead(t *testing.T) {
	loop, sid := testLoop(t, "")
	for _, tc := range []struct{ name, body, want string }{
		{"two in progress", `{"plan":[{"step":"a","status":"in_progress"},{"step":"b","status":"in_progress"}]}`,
			"exactly one step is in progress"},
		{"single step", `{"plan":[{"step":"do it","status":"pending"}]}`, "one-step plan is not a plan"},
		{"empty", `{"plan":[]}`, "at least one step"},
		{"bad status", `{"plan":[{"step":"a","status":"doing"},{"step":"b","status":"pending"}]}`,
			"must be one of pending, in_progress, completed"},
		{"blank step", `{"plan":[{"step":"  ","status":"pending"},{"step":"b","status":"pending"}]}`,
			"step 1 has no text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := planCall(t, loop, sid, tc.body)
			if !strings.HasPrefix(got, "ERROR:") || !strings.Contains(got, tc.want) {
				t.Errorf("result = %q, want a refusal naming %q", got, tc.want)
			}
		})
	}
}

// All-pending and all-completed are both legitimate: a plan written
// before starting, and one whose last step just finished.
func TestUpdatePlanAllowsNoStepInProgress(t *testing.T) {
	loop, sid := testLoop(t, "")
	for _, body := range []string{
		`{"plan":[{"step":"a","status":"pending"},{"step":"b","status":"pending"}]}`,
		`{"plan":[{"step":"a","status":"completed"},{"step":"b","status":"completed"}]}`,
	} {
		if got := planCall(t, loop, sid, body); strings.HasPrefix(got, "ERROR:") {
			t.Errorf("%s was refused: %s", body, got)
		}
	}
}

// The checklist is part of the Smart Agent bundle, so a turn without it
// is never offered the tool.
func TestUpdatePlanIsHiddenWithSmartAgentOff(t *testing.T) {
	loop, _ := testLoop(t, "")

	loop.SetSmartAgentEnabled(false)
	if !loop.hiddenTools(t.Context())[updatePlanToolName] {
		t.Error("update_plan was offered with Smart Agent off")
	}
	loop.SetSmartAgentEnabled(true)
	if loop.hiddenTools(t.Context())[updatePlanToolName] {
		t.Error("update_plan was hidden with Smart Agent on")
	}
}
