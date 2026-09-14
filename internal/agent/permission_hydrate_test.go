package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"localcode/internal/config"
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

// taskAsk resolves one background-task question with the given answer,
// asked in the task session and answered from the parent log where the
// mirror appears.
func taskAsk(t *testing.T, broker *PermissionBroker, taskID string, parentBaseline int, ask tools.Ask, allow bool, scope string) {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		ok, err := broker.Func()(WithSessionID(context.Background(), taskID), ask)
		done <- ok && err == nil
	}()
	broker.Resolve(waitForPermissionID(t, broker.store, "s1", parentBaseline), allow, scope)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the task call never returned after Resolve")
	}
}

// A question answered for a background task is shown in the parent
// conversation, not asked of it. After a restart the parent must face
// the same question again, while the task that was actually answered
// keeps its approval — and the parent's transcript still shows both
// the question and the answer.
func TestAMirroredTaskGrantIsNotTheParentsAfterARestart(t *testing.T) {
	first, store, _ := newPermissionTestBroker(t)
	if _, err := store.CreateSession("t1", "s1", "general-purpose", false); err != nil {
		t.Fatalf("create task session: %v", err)
	}
	ask := tools.Ask{Tool: "bash", Subject: "npm test", Description: "test call"}
	taskAsk(t, first, "t1", 0, ask, true, ScopeSession)

	again := rebuiltBroker(t, first)
	if remembered(t, again, "s1", ask) {
		t.Error("the parent was not asked again: a background task's approval became the parent's across a restart")
	}
	if !remembered(t, again, "t1", ask) {
		t.Error("the task lost the approval it was actually given")
	}

	// The mirror is a transcript, not a grant: the question and its
	// answer must still be visible in the parent conversation.
	evs, _ := store.Events("s1", 0)
	var sawRequest, sawResolved bool
	for _, ev := range evs {
		switch ev.Type {
		case events.TypePermissionRequest:
			sawRequest = true
		case events.TypePermissionResolved:
			sawResolved = true
		}
	}
	if !sawRequest || !sawResolved {
		t.Errorf("parent log keeps request=%v resolved=%v, want both: the fix must drop the grant, not the transcript", sawRequest, sawResolved)
	}
}

// The fix must only skip mirrored questions. An ordinary approval in
// the same log, answered for the parent itself, still has to come back.
func TestTheParentsOwnGrantStillSurvivesNextToAMirror(t *testing.T) {
	first, store, _ := newPermissionTestBroker(t)
	if _, err := store.CreateSession("t1", "s1", "general-purpose", false); err != nil {
		t.Fatalf("create task session: %v", err)
	}
	own := tools.Ask{Tool: "bash", Subject: "npm test", Description: "test call"}
	if !callAndResolve(t, first, "s1", "bash", "npm test", true, ScopeSession) {
		t.Fatal("expected the parent call to be allowed")
	}
	before, _ := store.Events("s1", 0)
	taskAsk(t, first, "t1", len(before),
		tools.Ask{Tool: "bash", Subject: "rm -rf build/", Description: "test call"}, true, ScopeSession)

	again := rebuiltBroker(t, first)
	if !remembered(t, again, "s1", own) {
		t.Error("the parent's own approval was lost: skipping mirrors must not skip ordinary grants")
	}
	if remembered(t, again, "s1", tools.Ask{Tool: "bash", Subject: "rm -rf build/", Description: "again"}) {
		t.Error("the task's approval leaked into the parent")
	}
}

// A directory approved for a background task is approved for the task,
// not for the conversation that happened to display the question. The
// parent's own approved directory must still rehydrate beside it.
func TestAMirroredOutsideApprovalIsNotTheParentsAfterARestart(t *testing.T) {
	first, store, _ := newPermissionTestBroker(t)
	if _, err := store.CreateSession("t1", "s1", "general-purpose", false); err != nil {
		t.Fatalf("create task session: %v", err)
	}
	own := tools.Ask{
		Tool: "read_file", Subject: "/opt/shared/lib/util.h", Description: "read",
		Outside: tools.OutsideRead, Dir: "/opt/shared", Workspace: "/home/me/project",
	}
	before, _ := store.Events("s1", 0)
	done := make(chan bool, 1)
	go func() {
		ok, err := first.Func()(WithSessionID(context.Background(), "s1"), own)
		done <- ok && err == nil
	}()
	first.Resolve(waitForPermissionID(t, store, "s1", len(before)), true, ScopeOutsideDir)
	<-done

	before, _ = store.Events("s1", 0)
	taskAsk(t, first, "t1", len(before), tools.Ask{
		Tool: "read_file", Subject: "/srv/other/f.h", Description: "read",
		Outside: tools.OutsideRead, Dir: "/srv/other", Workspace: "/home/me/project",
	}, true, ScopeOutsideDir)

	again := rebuiltBroker(t, first)
	sibling := own
	sibling.Subject = "/opt/shared/lib/other.h"
	if !remembered(t, again, "s1", sibling) {
		t.Error("the parent's own approved directory was forgotten: skipping mirrors must not skip ordinary grants")
	}
	taskSibling := tools.Ask{
		Tool: "read_file", Subject: "/srv/other/g.h", Description: "read",
		Outside: tools.OutsideRead, Dir: "/srv/other", Workspace: "/home/me/project",
	}
	if remembered(t, again, "s1", taskSibling) {
		t.Error("a directory approved for a background task became the parent's across a restart")
	}
	if got := again.RememberedOutside("s1", tools.OutsideRead); len(got) != 1 || got[0] != "/opt/shared" {
		t.Errorf("RememberedOutside after restart = %v, want [/opt/shared]", got)
	}
}

// "Always" still writes its global rule when the answer is given for a
// task — the resolver reads that file on its own — but the parent
// conversation must not additionally hold a session grant for it.
func TestAMirroredAlwaysApprovalWritesTheGlobalRuleButGrantsTheParentNothing(t *testing.T) {
	first, store, configPath := newPermissionTestBroker(t)
	if _, err := store.CreateSession("t1", "s1", "general-purpose", false); err != nil {
		t.Fatalf("create task session: %v", err)
	}
	ask := tools.Ask{Tool: "bash", Subject: "npm test", Description: "test call"}
	taskAsk(t, first, "t1", 0, ask, true, ScopeAlways)

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.json was not written: %v", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("written config.json does not parse: %v", err)
	}
	if got := cfg.ResolvePermission("bash", "npm run build", true); got != config.DecisionAllow {
		t.Errorf("ResolvePermission after a task's always-answer = %q, want allow: the global rule must still be written", got)
	}

	again := rebuiltBroker(t, first)
	if remembered(t, again, "s1", ask) {
		t.Error("the parent holds a session grant for a background task's always-answer")
	}
	if !remembered(t, again, "t1", ask) {
		t.Error("the task lost the approval it was actually given")
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
