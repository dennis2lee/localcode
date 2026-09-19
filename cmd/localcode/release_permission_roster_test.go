package main

import (
	"context"
	"testing"

	"localcode/internal/config"
)

// Every tool the daemon registers has to be a permission key the config
// package accepts.
//
// The two lists are joined by nothing at all. internal/config decides
// whether a permission key names a real tool by consulting a roster it
// keeps itself (config.RegisteredToolNames), and it keeps its own because
// the tools live in internal/tools and internal/agent, both of which
// import internal/config — so reading the registry from there is an import
// cycle. A copy with no guard is a copy that goes stale, and the failure
// it produces is the worst kind: a correct rule for a tool added later is
// refused at startup, and the message says the tool is unknown.
//
// This is the same shape, and for the same reason, as
// TestTheTwoDecisionRostersStillAgree in decision_roster_test.go: the
// check lives in the one package that imports both sides.
//
// One direction only. The reverse — a roster entry that names no tool —
// cannot be checked this way, because two tools register conditionally:
// Skill only when the workspace has skills (setSkillAssets), and check
// only when the config sets verify_command (buildRegistry). A daemon
// built here has neither, so asserting the roster against its registry
// reports both as stale when they are not. A name that is simply wrong
// is caught by the direction this test does check, since the tool it
// misspells is registered and then refused.
func TestEveryRegisteredToolIsAPermissionKey(t *testing.T) {
	f := &fakeModel{}
	smartHome(t, f.server(t).URL, smartOn)
	d, stop, err := buildDaemon(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("build the daemon: %v", err)
	}
	defer stop()

	names := d.Loop.Tools.Names()
	if len(names) < 8 {
		t.Fatalf("only %d tools registered, so this test is checking almost nothing", len(names))
	}
	for _, name := range names {
		if !config.ValidPermissionKey(name) {
			t.Errorf("the daemon registers %q and config.ValidPermissionKey refuses it, so "+
				`{"permission": {%q: "deny"}} is a startup error — add it to config.RegisteredToolNames`,
				name, name)
		}
	}
}
