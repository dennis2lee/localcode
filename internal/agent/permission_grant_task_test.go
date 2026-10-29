package agent

import (
	"testing"

	"localcode/internal/tools"
)

// A background task's question, answered "allow for this session" from
// the conversation it is mirrored into, grants the line to the task and
// not to the conversation, before and after a restart.
func TestATaskLineGrantLandsInTheTask(t *testing.T) {
	const line = "go test ./... 2>&1 | tail -30"
	first, store, _ := newPermissionTestBroker(t)
	if _, err := store.CreateSessionIn("t1", "s1", "general-purpose", "", false); err != nil {
		t.Fatal(err)
	}
	if !callAndResolve(t, first, "t1", "bash", line, true, ScopeSession) {
		t.Fatal("the answered call was not allowed")
	}
	for name, b := range map[string]*PermissionBroker{"live": first, "restarted": rebuiltBroker(t, first)} {
		if !remembered(t, b, "t1", tools.Ask{Tool: "bash", Subject: line, Description: "again"}) {
			t.Errorf("%s: the task's answered line is asked again in the task", name)
		}
		if remembered(t, b, "s1", tools.Ask{Tool: "bash", Subject: line, Description: "again"}) {
			t.Errorf("%s: the task's answered line is covered in the conversation it was mirrored into", name)
		}
	}
}
