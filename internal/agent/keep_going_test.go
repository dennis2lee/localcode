package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// The report, with the transcript: a build fails, the model reads the
// error, writes "global_init.cpp also has to be updated" — and ends its
// turn. The person types 진행. It does one more step and stops again.
//
// v0.46.0 fixed the mechanical half of this (a reply that asked for tools
// and was ended anyway, because the server called the stop reason
// "stop"). What is left is a model that really does stop, with nothing in
// the reply to run, and nothing in the reply to tell it apart from a
// model that has finished — which is why this is opt-in per profile.

// textStream is a reply with no tool calls in it: the shape of both "I am
// done" and "here is what still needs doing".
func textStream(text string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: text},
		{Type: provider.EventMessageStop, StopReason: "end_turn"},
	}
}

// keepGoingLoop is scriptedLoop with keep_going set on the profile.
func keepGoingLoop(t *testing.T, p provider.Provider, reg *tools.Registry, n int) (*Loop, string) {
	t.Helper()
	loop, sessionID := scriptedLoop(t, p, reg)
	loop.Config.Profiles["balanced"] = config.Profile{Provider: "local", Model: "m", KeepGoing: n}
	return loop, sessionID
}

func TestAStalledTurnCarriesOnWhenTheProfileAsksForIt(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		toolCallStream("tool_calls"),
		textStream("global_init.cpp also has to be updated."),
		toolCallStream("tool_calls"),
		textStream("done — the build passes."),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 1)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if runs != 2 {
		t.Errorf("the tool ran %d times, want 2 — the turn stopped after the model described the next step", runs)
	}
	if p.sent != 4 {
		t.Errorf("provider turns = %d, want 4", p.sent)
	}
	// And the budget stops it: with one carry-on allowed, the reply that
	// says the work is done is the end of the turn.

	// And it is visible. A turn that carries on by itself with nothing in
	// the transcript to say why is a model that looks like it never
	// stopped, which is a worse thing to debug than the stall.
	evs, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var said bool
	for _, ev := range evs {
		if ev.Type != events.TypeError {
			continue
		}
		msg, _ := ev.Data["error"].(string)
		if strings.Contains(msg, "keep_going") {
			said = true
			if recovered, _ := ev.Data["recovered"].(bool); !recovered {
				t.Error("the note is recorded as a failure, which ends the turn on both clients")
			}
		}
	}
	if !said {
		t.Error("nothing in the transcript says the turn was told to carry on")
	}
}

// Off by default, and it has to stay that way: a model that stops when it
// is done looks exactly like this, and nudging it spends a turn asking
// "anything else?" after every task.
func TestATurnIsNotCarriedOnByDefault(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		toolCallStream("tool_calls"),
		textStream("global_init.cpp also has to be updated."),
		toolCallStream("tool_calls"),
	}}
	loop, sessionID := scriptedLoop(t, p, reg)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if p.sent != 2 {
		t.Errorf("provider turns = %d, want 2 — an unconfigured profile must carry on nothing", p.sent)
	}
}

// A question and its answer is not a stalled task. Nothing ran, so there
// is nothing to carry on with, and "continue" would be an answer to a
// message the user never sent.
func TestAPlainAnswerIsLeftAlone(t *testing.T) {
	reg := tools.NewRegistry(nil)
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		textStream("Go maps are not safe for concurrent use."),
		textStream("anything else?"),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 3)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "are Go maps concurrent?"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if p.sent != 1 {
		t.Errorf("provider turns = %d, want 1", p.sent)
	}
}

// Bounded, because the one thing worse than a model that stops early is
// a session that prompts itself forever.
func TestCarryingOnIsBounded(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	// Work, stall, work, stall, work, stall: a model that would keep this
	// up for as long as it is asked to.
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		toolCallStream("tool_calls"),
		textStream("one more thing to do."),
		toolCallStream("tool_calls"),
		textStream("one more thing to do."),
		toolCallStream("tool_calls"),
		textStream("one more thing to do."),
		toolCallStream("tool_calls"),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 2)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// Six requests: the first reply, then two carry-ons each followed by
	// the work they produced, then the third stall — which is past the
	// budget and ends the turn.
	if p.sent != 6 {
		t.Errorf("provider turns = %d, want 6", p.sent)
	}
	if runs != 3 {
		t.Errorf("the tool ran %d times, want 3", runs)
	}
}

// A carry-on that produces no work is the model saying it has finished,
// and it is taken at its word. Without this, a model that stops when it
// is done spends the whole keep_going budget on turns that say "anything
// else?" — the cost of the setting would be paid on every completed task
// rather than only on the stalls it is there for.
func TestACarryOnThatProducesNoWorkEndsTheTurn(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		toolCallStream("tool_calls"),
		textStream("that is the whole change."),
		textStream("yes — nothing else to do."),
		textStream("still nothing."),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 3)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if p.sent != 3 {
		t.Errorf("provider turns = %d, want 3 — one carry-on, and the answer to it was taken as final", p.sent)
	}
}

// A model that stops after being told no has stopped for the right
// reason. Telling it to carry on overrides the person who said no.
func TestARefusedToolEndsTheTurnForGood(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})
	reg.Resolver = func(string, string, bool) tools.Decision { return tools.DecisionDeny }

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		toolCallStream("tool_calls"),
		textStream("understood, I will leave that alone."),
		toolCallStream("tool_calls"),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 2)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if runs != 0 {
		t.Fatalf("the denied tool ran %d times", runs)
	}
	if p.sent != 2 {
		t.Errorf("provider turns = %d, want 2 — the model was told to carry on after a refusal", p.sent)
	}
}

// A model that asks something is waiting for an answer, not stalling.
// Answering it with "continue" is how a question about which of two
// approaches to take gets decided by nobody.
func TestAQuestionToTheUserIsNotAStall(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		toolCallStream("tool_calls"),
		textStream("두 가지 방법이 있습니다.\n\n어느 쪽으로 할까요?"),
		toolCallStream("tool_calls"),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 2)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if p.sent != 2 {
		t.Errorf("provider turns = %d, want 2 — the model's question was answered by the harness", p.sent)
	}
}

func TestTheQuestionCheckReadsTheLastThingWritten(t *testing.T) {
	for text, want := range map[string]bool{
		"어느 쪽으로 할까요?":                    true,
		"Which one do you want?":         true,
		"**Shall I go ahead?**":          true,
		"Done. Tests pass.":              false,
		"Is it broken? No — it is fine.": false,
		"":                               false,
	} {
		if got := endsWithQuestion(text); got != want {
			t.Errorf("endsWithQuestion(%q) = %v, want %v", text, got, want)
		}
	}
}

// The point of shipping this in a release: the person who reported the
// stall gets a model that finishes by installing the update. The model is
// in the quirk table, so an unconfigured profile carries on out of the
// box — and -1 is the way to say never, whatever the model.
func TestTheReportedModelCarriesOnWithNoConfigAtAll(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		toolCallStream("tool_calls"),
		textStream("global_init.cpp also has to be updated."),
		toolCallStream("tool_calls"),
		textStream("done — the build passes."),
	}}
	loop, sessionID := scriptedLoop(t, p, reg)
	// No keep_going anywhere: only the model name says what this is.
	loop.Config.Profiles["balanced"] = config.Profile{Provider: "local", Model: "muse-glimmer-30b"}

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if runs != 2 {
		t.Errorf("the tool ran %d times, want 2 — the fix did not ship with the release", runs)
	}
}

func TestMinusOneTurnsCarryingOnOffForAStallingModel(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		toolCallStream("tool_calls"),
		textStream("global_init.cpp also has to be updated."),
		toolCallStream("tool_calls"),
	}}
	loop, sessionID := scriptedLoop(t, p, reg)
	loop.Config.Profiles["balanced"] = config.Profile{Provider: "local", Model: "muse-glimmer-30b", KeepGoing: -1}

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if p.sent != 2 {
		t.Errorf("provider turns = %d, want 2 — keep_going -1 did not turn it off", p.sent)
	}
}

// The carry-on is part of the conversation the model had, so it has to
// survive a daemon restart. It used to live only in memory: rehydrating
// the event log rebuilt the history with two assistant messages back to
// back — a shape Bedrock rejects outright — and a "continue" the model
// was never re-told about.
func TestACarriedOnTurnSurvivesARestart(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		toolCallStream("tool_calls"),
		textStream("global_init.cpp also has to be updated."),
		toolCallStream("tool_calls"),
		textStream("done — the build passes."),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 1)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	live := loop.history(sessionID)

	// What a restart does: throw the memory away and rebuild from the log.
	loop.setHistory(sessionID, nil)
	loop.RehydrateSession(sessionID)
	got := loop.history(sessionID)

	if len(got) != len(live) {
		t.Fatalf("rehydrated %d messages, live had %d", len(got), len(live))
	}
	for i := range got {
		if got[i].Role != live[i].Role {
			t.Fatalf("message %d role = %s, live had %s", i, got[i].Role, live[i].Role)
		}
	}
	var nudge bool
	for _, m := range got {
		if m.Role == provider.RoleUser && strings.Contains(replyText(m.Content), "ended your turn") {
			nudge = true
		}
	}
	if !nudge {
		t.Error("the carry-on message is not in the rehydrated history — the model would remember a different conversation")
	}
}

// The model reported in this is one of the ones that stalls, and it is
// told so in words as well: a note costs a paragraph on that model and is
// sent to nothing else.
func TestTheStallingModelIsAskedNotTo(t *testing.T) {
	note := quirkNote("muse-glimmer-30b")
	if note == "" {
		t.Fatal("no note for the model this was reported against")
	}
	if !strings.Contains(note, "same turn") {
		t.Errorf("the note does not say to keep going: %q", note)
	}
	if quirkNote("claude-opus-5") != "" {
		t.Error("a model with no such habit is being told about one")
	}
}

// countingTool records how many times it ran, which is how a carried-on
// turn is told from one that ended.
type countingTool struct{ runs *int }

func (t countingTool) Name() string        { return "echo" }
func (t countingTool) Description() string { return "echo" }
func (t countingTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t countingTool) RequiresPermission(json.RawMessage) bool { return false }
func (t countingTool) Execute(context.Context, json.RawMessage) tools.Result {
	*t.runs++
	return tools.Result{Content: "ok"}
}

// Someone who has already typed the next instruction does not need
// localcode to invent one. Their message reaches the model as soon as
// this turn ends, and "carry on" ahead of it is the harness talking over
// the person it is working for.
func TestAWaitingUserIsNotTalkedOver(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		toolCallStream("tool_calls"),
		textStream("global_init.cpp also has to be updated."),
		toolCallStream("tool_calls"),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 2)
	loop.UserWaiting = func(string) bool { return true }

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if p.sent != 2 {
		t.Errorf("provider turns = %d, want 2 — the turn carried on over a message the user had already sent", p.sent)
	}
}
