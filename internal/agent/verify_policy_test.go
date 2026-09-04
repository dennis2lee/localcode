package agent

import (
	"strings"
	"testing"
)

// The permission mode decides what the model is told about running
// checks, and both answers are wrong in the other mode.
func TestTheVerifyPolicyFollowsThePermissionMode(t *testing.T) {
	loop, _ := testLoop(t, "")

	loop.Config.SetSkipPermissionsRuntime(true)
	skipped := loop.verifyPolicyFor([]string{"check", "bash"})
	if !strings.Contains(skipped, "without asking") || !strings.Contains(skipped, "the check tool") {
		t.Errorf("with permissions skipped: %q", skipped)
	}

	loop.Config.SetSkipPermissionsRuntime(false)
	asking := loop.verifyPolicyFor([]string{"check", "bash"})
	if !strings.Contains(asking, "waits for the person") {
		t.Errorf("with approvals on: %q", asking)
	}
	if skipped == asking {
		t.Error("the two modes were told the same thing")
	}

	// A turn with no way to run anything is told nothing, rather than
	// being given advice about a tool it does not have.
	if got := loop.verifyPolicyFor([]string{"read_file", "grep"}); got != "" {
		t.Errorf("a turn with no check tool got %q", got)
	}
}

// The meter speaks only from usage the server reported. Every count on
// this side is a four-characters-per-token guess, and a model told a
// made-up number acts on it.
func TestTheContextMeterOnlySpeaksFromMeasuredUsage(t *testing.T) {
	loop, sid := testLoop(t, "")

	if got := loop.contextLeftFor(sid); got != "" {
		t.Errorf("a session with no measured usage got %q", got)
	}

	setTestUsage(loop, sid, sessionUsage{InputTokens: 90_000, OutputTokens: 10_000, MaxContext: 200_000})
	got := loop.contextLeftFor(sid)
	for _, want := range []string{"100,000", "200,000", "50%"} {
		if !strings.Contains(got, want) {
			t.Errorf("meter lacks %q: %s", want, got)
		}
	}

	// Under a quarter it says to read in ranges; under a tenth it says to
	// stop and describe the next step, because what is described survives
	// the compaction that is coming.
	setTestUsage(loop, sid, sessionUsage{InputTokens: 180_000, MaxContext: 200_000})
	if got := loop.contextLeftFor(sid); !strings.Contains(got, "Read in ranges") {
		t.Errorf("at 10%%: %s", got)
	}
	setTestUsage(loop, sid, sessionUsage{InputTokens: 195_000, MaxContext: 200_000})
	if got := loop.contextLeftFor(sid); !strings.Contains(got, "not room for another long file") {
		t.Errorf("at 2.5%%: %s", got)
	}
}

// setTestUsage puts a measured snapshot where getUsage will find it.
// recordUsage takes a whole stream's worth of fields this test does not
// need, and the meter reads only the snapshot.
func setTestUsage(l *Loop, sessionID string, u sessionUsage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.usage == nil {
		l.usage = map[string]sessionUsage{}
	}
	l.usage[sessionID] = u
}
