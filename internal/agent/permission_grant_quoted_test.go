package agent

import (
	"context"
	"testing"

	"localcode/internal/config"
)

// A program written in quotes is generalized like any other: approving
// `"rm" -rf build/` grants `"rm" *`. The same rule in config asks for
// `"rm" -rf ~`, because the unquoted reading, `rm -rf ~`, has to match as
// well, and a session grant asks the same of both readings.
func TestASessionGrantForAQuotedProgramDoesNotCoverWhatConfigAsks(t *testing.T) {
	const approved, next = `"rm" -rf build/`, `"rm" -rf ~`
	if got := config.PermissionRuleFor("bash", approved).Match; got != `"rm" *` {
		t.Fatalf("precondition: the rule proposed for %s is %q", approved, got)
	}
	cfg := &config.Config{Permissions: map[string]config.ToolPermission{
		"bash": {Rules: []config.PermissionRule{{Match: `"rm" *`, Decision: config.DecisionAllow}}},
	}}
	if d := cfg.ResolvePermissionFor(context.Background(), "bash", next, true); d == config.DecisionAllow {
		t.Fatalf("precondition: config rule %q allows %s", `"rm" *`, next)
	}

	broker, _, _ := newPermissionTestBroker(t)
	if !callAndResolve(t, broker, "s1", "bash", approved, true, ScopeSession) {
		t.Fatal("the first call should be allowed")
	}
	if askedOrAllowed(t, broker, "s1", next) {
		t.Errorf("a session grant from %s ran %s without a question; the same rule in config.json asks for it", approved, next)
	}
}
