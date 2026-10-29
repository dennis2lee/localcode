package agent

import (
	"testing"

	"localcode/internal/tools"
)

// "Allow for this session" on a line covers that same line the next time
// it runs, second commands, pipes and redirects included. The rule a line
// generalizes to names only its first command, and coverage asks for every
// command of a line, so the line itself is granted too.
func TestASessionGrantCoversTheLineItWasAnsweredFor(t *testing.T) {
	for _, line := range []string{
		"cd /repo && go test ./...",
		"ls -la | head -20",
		"go test ./... 2>&1",
		"go vet ./... 2>&1 | tail -30",
	} {
		t.Run(line, func(t *testing.T) {
			broker, _, _ := newPermissionTestBroker(t)
			if !callAndResolve(t, broker, "s1", "bash", line, true, ScopeSession) {
				t.Fatal("the answered call was not allowed")
			}
			if !askedOrAllowed(t, broker, "s1", line) {
				t.Errorf("answered %q with allow for this session, and the same line is asked again", line)
			}
		})
	}
}

// The line grant survives a restart, as the rule grant does: the request
// records the line, and the answer's scope rebuilds both.
func TestALineGrantSurvivesARestart(t *testing.T) {
	const line = "cd /repo && go test ./... 2>&1 | tail -30"
	first, _, _ := newPermissionTestBroker(t)
	if !callAndResolve(t, first, "s1", "bash", line, true, ScopeSession) {
		t.Fatal("the answered call was not allowed")
	}
	again := rebuiltBroker(t, first)
	if !remembered(t, again, "s1", tools.Ask{Tool: "bash", Subject: line, Description: "again"}) {
		t.Error("the line answered before a restart is asked again after it")
	}
}
