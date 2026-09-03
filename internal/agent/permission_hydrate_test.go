package agent

import (
	"context"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/tools"
)

// A second broker over the same store is what a restart is: the process
// that heard the answer is gone, the log is not. Everything below builds
// one and asks it what the first one was told.
func rebuiltBroker(t *testing.T, from *PermissionBroker) *PermissionBroker {
	t.Helper()
	b := NewPermissionBroker(from.store)
	b.ConfigPath = from.ConfigPath
	return b
}

// remembered reports whether the broker answers the ask without
// prompting. A prompt would block on Resolve, so "did not return" is the
// failure, bounded rather than hung.
func remembered(t *testing.T, b *PermissionBroker, sessionID string, ask tools.Ask) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(WithSessionID(context.Background(), sessionID), 300*time.Millisecond)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		allowed, err := b.Func()(ctx, ask)
		done <- allowed && err == nil
	}()
	select {
	case ok := <-done:
		return ok
	case <-time.After(400 * time.Millisecond):
		return false
	}
}

// "Allow for this session" used to mean "allow until the process ends".
// The words say session, so a restart — an update, a crash, a handoff to
// a newer daemon — must not turn an answered question back into an
// unanswered one.
func TestASessionGrantSurvivesARestart(t *testing.T) {
	first, _, _ := newPermissionTestBroker(t)
	if !callAndResolve(t, first, "s1", "bash", "npm test", true, ScopeSession) {
		t.Fatal("expected the call to be allowed")
	}

	again := rebuiltBroker(t, first)
	if !remembered(t, again, "s1", tools.Ask{Tool: "bash", Subject: "npm test", Description: "again"}) {
		t.Error("a session grant was forgotten by a restart")
	}
}

// And "allow once" means once. A restart must not widen it.
func TestAOnceGrantDoesNotSurviveARestart(t *testing.T) {
	first, _, _ := newPermissionTestBroker(t)
	if !callAndResolve(t, first, "s1", "bash", "npm test", true, ScopeOnce) {
		t.Fatal("expected the call to be allowed")
	}

	again := rebuiltBroker(t, first)
	if remembered(t, again, "s1", tools.Ask{Tool: "bash", Subject: "npm test", Description: "again"}) {
		t.Error("a once-only answer was widened to a session grant by a restart")
	}
}

// A refusal is not a grant, whatever scope the client sent with it.
func TestADenialIsNotRememberedAsAGrant(t *testing.T) {
	first, _, _ := newPermissionTestBroker(t)
	if callAndResolve(t, first, "s1", "bash", "rm -rf build", false, ScopeSession) {
		t.Fatal("expected the call to be refused")
	}

	again := rebuiltBroker(t, first)
	if remembered(t, again, "s1", tools.Ask{Tool: "bash", Subject: "rm -rf build", Description: "again"}) {
		t.Error("a denial came back as a grant after a restart")
	}
}

// The directory a conversation approved leaving the workspace for is the
// case people actually hit: an hour into a session the model reads a
// header under /usr/include again and is asked again, because the answer
// lived in a map.
func TestAnApprovedOutsideDirectorySurvivesARestart(t *testing.T) {
	first, store, _ := newPermissionTestBroker(t)
	ask := tools.Ask{
		Tool: "read_file", Subject: "/opt/shared/lib/util.h", Description: "read",
		Outside: tools.OutsideRead, Dir: "/opt/shared", Workspace: "/home/me/project",
	}
	before, _ := store.Events("s1", 0)
	done := make(chan bool, 1)
	go func() {
		ok, err := first.Func()(WithSessionID(context.Background(), "s1"), ask)
		done <- ok && err == nil
	}()
	id := waitForPermissionID(t, store, "s1", len(before))
	first.Resolve(id, true, ScopeOutsideDir)
	if !<-done {
		t.Fatal("expected the read to be allowed")
	}

	again := rebuiltBroker(t, first)
	sibling := ask
	sibling.Subject = "/opt/shared/lib/other.h"
	if !remembered(t, again, "s1", sibling) {
		t.Error("an approved outside directory was forgotten by a restart")
	}
	if got := again.RememberedOutside("s1", tools.OutsideRead); len(got) != 1 || got[0] != "/opt/shared" {
		t.Errorf("RememberedOutside after restart = %v, want [/opt/shared]", got)
	}
}

// "/read-outside mem-clear" has to be as durable as the remembering it
// undoes. A forget that lived only in memory would come back with the
// directories on the next restart, which is the opposite of what was
// asked.
func TestAForgetSurvivesARestartToo(t *testing.T) {
	first, store, _ := newPermissionTestBroker(t)
	ask := tools.Ask{
		Tool: "read_file", Subject: "/opt/shared/lib/util.h", Description: "read",
		Outside: tools.OutsideRead, Dir: "/opt/shared", Workspace: "/home/me/project",
	}
	before, _ := store.Events("s1", 0)
	done := make(chan bool, 1)
	go func() {
		ok, err := first.Func()(WithSessionID(context.Background(), "s1"), ask)
		done <- ok && err == nil
	}()
	first.Resolve(waitForPermissionID(t, store, "s1", len(before)), true, ScopeOutsideDir)
	<-done

	if n := first.ForgetOutside("s1", tools.OutsideRead); n != 1 {
		t.Fatalf("ForgetOutside dropped %d directories, want 1", n)
	}
	evs, _ := store.Events("s1", 0)
	if evs[len(evs)-1].Type != events.TypePermissionForgotten {
		t.Fatalf("a forget wrote no event; the last one is %s", evs[len(evs)-1].Type)
	}

	again := rebuiltBroker(t, first)
	if remembered(t, again, "s1", ask) {
		t.Error("a forgotten directory came back after a restart")
	}
	if got := again.RememberedOutside("s1", tools.OutsideRead); len(got) != 0 {
		t.Errorf("RememberedOutside after a forget and a restart = %v, want none", got)
	}
}

// Hydration is read-only. The checks it runs under are the ones that
// decide whether to ask at all, and a remembered answer producing an
// event would be a prompt nobody sees.
func TestHydrationWritesNothing(t *testing.T) {
	first, store, _ := newPermissionTestBroker(t)
	if !callAndResolve(t, first, "s1", "bash", "npm test", true, ScopeSession) {
		t.Fatal("expected the call to be allowed")
	}
	before, _ := store.Events("s1", 0)

	again := rebuiltBroker(t, first)
	if !remembered(t, again, "s1", tools.Ask{Tool: "bash", Subject: "npm test", Description: "again"}) {
		t.Fatal("grant not remembered")
	}
	after, _ := store.Events("s1", 0)
	if len(after) != len(before) {
		t.Errorf("a remembered answer wrote %d event(s); it must write none", len(after)-len(before))
	}
}
