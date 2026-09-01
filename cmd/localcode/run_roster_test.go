package main

import (
	"context"
	"sort"
	"strings"
	"testing"
)

// The one-shot's tool roster against the daemon's.
//
// This is the release gate the v0.80.0 defect needed and did not have.
// `localcode run` assembles its own composition of the same parts, so
// every tool the daemon registers is a decision here too — and the way
// that decision gets made is by nobody making it: a tool is added to
// buildDaemon and buildOneShot is not touched, which is exactly how Smart
// Agent's delegation tools came to be missing from a mode that was being
// sent the prompt telling the model to use them.
//
// So the difference is written down rather than left to be discovered.
// A tool added to the daemon and not to a run fails this test until
// somebody either adds it or says here why a run cannot have it.

// leftOutOfARun is every tool the daemon registers that a one-shot
// deliberately does not, with the reason. Each would otherwise be a tool
// that can only refuse, which is a turn the model spends discovering it.
var leftOutOfARun = map[string]string{
	"Schedule": "books work for a time after this process has exited",
	"session_read": "a run's store holds its own session and nothing else, " +
		"so there is no other conversation to read",
	"Debate": "every turn in a pipe is unattended, and a debate can only be " +
		"started from a conversation somebody is having",
	"Verdict": "a debate reviewer's tool, and a run cannot hold a debate",
}

func TestTheOneShotRosterDiffersFromTheDaemonsOnlyOnPurpose(t *testing.T) {
	f := &fakeModel{}
	smartHome(t, f.server(t).URL, smartOn)

	ctx := context.Background()
	d, stopDaemon, err := buildDaemon(ctx, "", nil)
	if err != nil {
		t.Fatalf("build the daemon: %v", err)
	}
	defer stopDaemon()

	loop, _, stopRun, err := buildOneShot(ctx, runOptions{agent: "general-purpose"})
	if err != nil {
		t.Fatalf("build the one-shot: %v", err)
	}
	defer stopRun()

	inRun := map[string]bool{}
	for _, name := range loop.Tools.Names() {
		inRun[name] = true
	}

	var missing []string
	for _, name := range d.Loop.Tools.Names() {
		if inRun[name] {
			continue
		}
		if _, deliberate := leftOutOfARun[name]; deliberate {
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("the daemon registers %s and a one-shot run does not.\n"+
			"Either register them in buildOneShot, or add each to leftOutOfARun with the reason a run cannot have it.",
			strings.Join(missing, ", "))
	}

	// And the other direction: a name in the exclusion list that the run
	// has anyway is a reason that has stopped being true, which is worse
	// than no reason at all.
	for name, why := range leftOutOfARun {
		if inRun[name] {
			t.Errorf("a run offers %q, but leftOutOfARun still says it cannot: %q", name, why)
		}
	}
}
