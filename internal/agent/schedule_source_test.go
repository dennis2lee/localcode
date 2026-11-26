package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/prompt"
	"localcode/internal/provider"
	"localcode/internal/trace"
)

// Whose task a scheduled run was given.
//
// A prompt the model books is text a model wrote after reading whatever it
// read, and the run that fires it is given it as its whole assignment. That
// is the standing of a sub-agent's task, and the manifest names a
// sub-agent's task as one (child.input.<agent>: instructs the run, claims
// to be nobody's words) so that a reader of the run's request can ask where
// the instruction came from. The run's opening message was not named at
// all, and an unnamed opening message is what a person typed.
//
// A prompt a person booked is theirs and stays unnamed, which is half of
// what these tests are for: a tag on everything would be as wrong as a tag
// on nothing.

// openingSourceOf is the source tag on the first message a session
// recorded from a user, and whether it recorded one.
func openingSourceOf(t *testing.T, loop *Loop, sessionID string) (string, bool) {
	t.Helper()
	for _, ev := range mustEvents(t, loop.Store, sessionID) {
		if ev.Type == events.TypeUserMessage {
			src, _ := ev.Data["source"].(string)
			return src, true
		}
	}
	return "", false
}

// sourceRig is an authorRig whose requests leave a manifest behind, which
// is where the run's opening message has to be named if it is named at all.
// The turn log, which is what records a request's manifest, is Smart Agent's.
func sourceRig(t *testing.T, turns ...[]provider.StreamEvent) (*authorRig, *prompt.Store, *trace.Writer) {
	t.Helper()
	rig := newAuthorRig(t, turns...)
	store, err := prompt.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open the manifest store: %v", err)
	}
	rig.loop.Manifests = store
	rig.loop.SetSmartAgentEnabled(true)
	return rig, store, withTracing(t, rig.loop)
}

func TestAPromptTheModelBookedIsNamedAsTheRunsTask(t *testing.T) {
	rig, manifests, trace := sourceRig(t, answer("did the thing"))
	input, _ := json.Marshal(map[string]string{"when": "in 1 hour", "prompt": "run the tests and report"})
	if res := NewScheduleTool(rig.loop).Execute(WithSessionID(context.Background(), rig.sid), input); res.IsError {
		t.Fatalf("Execute: %s", res.Content)
	}
	run := rig.fire(t, rig.booked(t).ID)

	const want = "child.input.general-purpose"
	if got, ok := openingSourceOf(t, rig.loop, run.RunSession); !ok || got != want {
		t.Errorf("the run's opening message is tagged %q (recorded: %v), want %q", got, ok, want)
	}
	m, ok := lastManifest(t, trace, manifests, run.RunSession)
	if !ok {
		t.Fatal("the run's model call recorded no manifest")
	}
	e, found := entryByID(m, want)
	if !found {
		t.Fatalf("the run's request carries the booked prompt and its manifest does not name it: %v", m.SelectedIDs())
	}
	// It has to instruct: the run exists to carry it out, and a run that
	// treated its own assignment as data would do nothing. And it is not
	// the person's words, which is the distinction a reader needs.
	if e.Provenance != prompt.FromParentAgent || e.Trust != prompt.TrustDelegated || !e.Trust.Instruction() {
		t.Errorf("the booked prompt is recorded as %s/%s, want the parent agent's delegated task", e.Provenance, e.Trust)
	}
	if e.Trust == prompt.TrustUser || e.Provenance == prompt.FromUser {
		t.Error("a prompt the model wrote claims to be the person's")
	}
}

// The other half. A person's booking is their own words, and so is whatever
// they type in the run's conversation afterwards, which travels with
// nothing that says the model wrote it.
func TestAPromptAPersonBookedIsNotNamedAsAnotherAuthorsTask(t *testing.T) {
	rig, manifests, trace := sourceRig(t, answer("did the thing"), answer("the lint is clean too"))
	if _, err := rig.sched.Add(rig.sid, "general-purpose", "run the tests and report", time.Now().Add(time.Hour), RepeatOptions{}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	run := rig.fire(t, rig.booked(t).ID)

	if got, ok := openingSourceOf(t, rig.loop, run.RunSession); !ok || got != "" {
		t.Errorf("a person's booking is tagged %q (recorded: %v), want no tag", got, ok)
	}
	if m, ok := lastManifest(t, trace, manifests, run.RunSession); !ok {
		t.Fatal("the run's model call recorded no manifest")
	} else if _, found := entryByID(m, "child.input.general-purpose"); found {
		t.Error("a person's booking is named as a task another agent handed the run")
	}

	// And the follow-up, typed into the run's conversation by a person.
	if err := rig.loop.SendMessage(context.Background(), run.RunSession, "general-purpose", "and the lint?"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	var tagged []string
	for _, ev := range mustEvents(t, rig.loop.Store, run.RunSession) {
		if src, _ := ev.Data["source"].(string); ev.Type == events.TypeUserMessage && src != "" {
			tagged = append(tagged, src)
		}
	}
	if len(tagged) != 0 {
		t.Errorf("messages in a person's run are tagged %v, want none", tagged)
	}
}

// What a model books and then a person types into the same run: the
// opening message is the model's and the follow-up is the person's, so the
// tag follows the booked text and not the session.
func TestOnlyTheOpeningMessageOfAModelsBookingIsTagged(t *testing.T) {
	rig, _, _ := sourceRig(t, answer("did the thing"), answer("the lint is clean too"))
	input, _ := json.Marshal(map[string]string{"when": "in 1 hour", "prompt": "run the tests and report"})
	if res := NewScheduleTool(rig.loop).Execute(WithSessionID(context.Background(), rig.sid), input); res.IsError {
		t.Fatalf("Execute: %s", res.Content)
	}
	run := rig.fire(t, rig.booked(t).ID)
	if err := rig.loop.SendMessage(context.Background(), run.RunSession, "general-purpose", "and the lint?"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	var sources []string
	for _, ev := range mustEvents(t, rig.loop.Store, run.RunSession) {
		if ev.Type == events.TypeUserMessage {
			src, _ := ev.Data["source"].(string)
			sources = append(sources, src)
		}
	}
	if len(sources) != 2 || sources[0] != "child.input.general-purpose" || sources[1] != "" {
		t.Errorf("the run's user messages are tagged %q, want the booked prompt tagged and the follow-up not", sources)
	}
}
