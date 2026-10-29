package agent

import (
	"context"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/tools"
)

// askedOrAllowed calls the broker for a bash line and reports whether it
// was allowed without a question. A question is answered no, so the call
// ends.
func askedOrAllowed(t *testing.T, broker *PermissionBroker, sessionID, command string) (allowedSilently bool) {
	t.Helper()
	before, _ := broker.store.Events(sessionID, 0)
	got := make(chan bool, 1)
	go func() {
		allowed, _ := broker.Func()(WithSessionID(context.Background(), sessionID),
			tools.Ask{Tool: "bash", Subject: command, Description: "test call"})
		got <- allowed
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case allowed := <-got:
			return allowed
		default:
		}
		evs, _ := broker.store.Events(sessionID, 0)
		for i := len(evs) - 1; i >= len(before); i-- {
			if evs[i].Type == events.TypePermissionRequest {
				id, _ := evs[i].Data["id"].(string)
				broker.Resolve(id, false, "")
				<-got
				return false
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%q was neither allowed nor asked", command)
	return false
}

// "Allow for this session" covers a bash line only when it covers each
// command of it. The rule a line generalizes to names its first command,
// and matching a whole line against it let `ls -la && rm -rf ~` run on a
// grant for `ls -la`.
func TestASessionGrantCoversEachCommandOfALine(t *testing.T) {
	broker, _, _ := newPermissionTestBroker(t)
	if !callAndResolve(t, broker, "s1", "bash", "ls -la", true, ScopeSession) {
		t.Fatal("the first call should be allowed")
	}
	for _, tc := range []struct {
		command string
		allowed bool
	}{
		{"ls -l", true},
		{"ls -la && ls -l /tmp", true},
		{"ls -la && rm -rf ~", false},
		{"ls -la; rm -rf ~", false},
		{"ls $(rm -rf ~)", false},
		{"ls > listing.txt", false},
	} {
		if got := askedOrAllowed(t, broker, "s1", tc.command); got != tc.allowed {
			t.Errorf("after a session grant for `ls -la`, %q allowed without a question = %v, want %v", tc.command, got, tc.allowed)
		}
	}
}
