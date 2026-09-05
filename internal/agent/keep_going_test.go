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

// distinctToolCall is toolCallStream with arguments of its own, so a
// script can tell "the model did something else" from "the model did the
// same thing again". keep_going counts only the first as work — see
// newWork.
func distinctToolCall(arg string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: "running the command now"},
		{Type: provider.EventToolUseStart, ToolUseID: "call_" + arg, ToolName: "bash"},
		{Type: provider.EventToolUseEnd, ToolUseID: "call_" + arg, ToolInput: json.RawMessage(`{"step":"` + arg + `"}`)},
		{Type: provider.EventMessageStop, StopReason: "tool_calls"},
	}
}

// keepGoingLoop is scriptedLoop with keep_going set on the profile. The
// model id contains "muse" because the feature only exists for that
// family now; TestKeepGoingIsMuseOnly is the test of that boundary.
func keepGoingLoop(t *testing.T, p provider.Provider, reg *tools.Registry, n int) (*Loop, string) {
	t.Helper()
	loop, sessionID := scriptedLoop(t, p, reg)
	loop.Config.Profiles["balanced"] = config.Profile{Provider: "local", Model: "muse-test-7b", KeepGoing: n}
	return loop, sessionID
}

func TestAStalledTurnCarriesOnWhenTheProfileAsksForIt(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		distinctToolCall("edit-main"),
		textStream("global_init.cpp also has to be updated."),
		distinctToolCall("edit-global-init"),
		textStream("done — the build passes."),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 1)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if runs != 2 {
		t.Errorf("the tool ran %d times, want 2 — the turn stopped after the model described the next step", runs)
	}
	if p.sentCount() != 4 {
		t.Errorf("provider turns = %d, want 4", p.sentCount())
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
	if p.sentCount() != 2 {
		t.Errorf("provider turns = %d, want 2 — an unconfigured profile must carry on nothing", p.sentCount())
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
	if p.sentCount() != 1 {
		t.Errorf("provider turns = %d, want 1", p.sentCount())
	}
}

// Bounded, because the one thing worse than a model that stops early is
// a session that prompts itself forever.
func TestCarryingOnIsBounded(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	// Work, stall, work, stall, work, stall: a model that would keep this
	// up for as long as it is asked to. Each step CHANGES something,
	// which is what makes it progress — a step that only looks somewhere
	// new does not earn another carry-on, and a step that repeats itself
	// does not either. See TestLookingSomewhereNewDoesNotEarnAnother and
	// TestACarryOnThatOnlyRepeatsItselfEndsTheTurn.
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		distinctToolCall("one"),
		textStream("one more thing to do."),
		distinctToolCall("two"),
		textStream("one more thing to do."),
		distinctToolCall("three"),
		textStream("one more thing to do."),
		distinctToolCall("four"),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 2)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// Six requests: the first reply, then two carry-ons each followed by
	// the work they produced, then the third stall — which is past the
	// budget and ends the turn.
	if p.sentCount() != 6 {
		t.Errorf("provider turns = %d, want 6", p.sentCount())
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
	if p.sentCount() != 3 {
		t.Errorf("provider turns = %d, want 3 — one carry-on, and the answer to it was taken as final", p.sentCount())
	}
}

// A model that stops after being told no has stopped for the right
// reason. Telling it to carry on overrides the person who said no.
func TestARefusedToolEndsTheTurnForGood(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})
	reg.Resolver = func(context.Context, tools.Query) tools.Outcome { return tools.Outcome{Decision: tools.DecisionDeny} }

	// distinctToolCall, not the shared toolCallStream: this registry holds
	// one tool and the call has to name it, or the turn ends on "no such
	// tool" and the refusal is never the reason.
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		distinctToolCall("one"),
		textStream("understood, I will leave that alone."),
		distinctToolCall("two"),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 2)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if runs != 0 {
		t.Fatalf("the denied tool ran %d times", runs)
	}
	if p.sentCount() != 2 {
		t.Errorf("provider turns = %d, want 2 — the model was told to carry on after a refusal", p.sentCount())
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
	if p.sentCount() != 2 {
		t.Errorf("provider turns = %d, want 2 — the model's question was answered by the harness", p.sentCount())
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
		distinctToolCall("edit-main"),
		textStream("global_init.cpp also has to be updated."),
		distinctToolCall("edit-global-init"),
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
	if p.sentCount() != 2 {
		t.Errorf("provider turns = %d, want 2 — keep_going -1 did not turn it off", p.sentCount())
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
		distinctToolCall("edit-main"),
		textStream("global_init.cpp also has to be updated."),
		distinctToolCall("edit-global-init"),
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
		// Matched against the prompt itself, not a phrase copied out of
		// it: the wording is tuned from time to time and a test that
		// pins a sentence would fail for that instead of for this.
		if m.Role == provider.RoleUser && strings.Contains(replyText(m.Content), keepGoingPrompt) {
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

// "bash", not "echo": a carry-on is earned by a call that changes
// something, and the test's own premise is a model that does the next
// piece of work each time it is prodded. See changedSomething.
func (t countingTool) Name() string        { return "bash" }
func (t countingTool) Description() string { return "run a command" }
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
	if p.sentCount() != 2 {
		t.Errorf("provider turns = %d, want 2 — the turn carried on over a message the user had already sent", p.sentCount())
	}
}

// The report this fixes: with muse, "the task is already finished and it
// runs it all over again".
//
// A model told "you did not finish" does not argue. It goes and does
// something — re-reads the file it just wrote, re-runs the build it just
// ran — and the old guard cleared on any tool call at all, so every one
// of those bought another carry-on. A completed task was re-executed for
// the whole budget, and from the outside that is a model repeating itself
// until it is told to stop.
//
// The script is the finished case exactly: work, a summary, and then
// nothing but the same call again each time it is prodded.
func TestACarryOnThatOnlyRepeatsItselfEndsTheTurn(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		distinctToolCall("build"),
		textStream("done — the build passes."),
		distinctToolCall("build"), // the same call, to check its own work
		textStream("still done."),
		distinctToolCall("build"), // and again
		textStream("still done."),
		distinctToolCall("build"),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 3)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// One carry-on, the repeat it produced, and the reply that ends the
	// turn: four requests, not the eight a budget of three would allow.
	if p.sentCount() != 4 {
		t.Errorf("provider turns = %d, want 4 — a finished task was carried on %d times", p.sentCount(), p.sentCount()-2)
	}
	// Twice: the real work, and the one repeat the single carry-on bought.
	if runs != 2 {
		t.Errorf("the tool ran %d times, want 2 — finished work was re-executed", runs)
	}
}

// The carry-on prompt must not assert that the work is unfinished.
//
// localcode cannot tell a stall from a finished task — that is the whole
// premise of this file — so a sentence claiming the task is incomplete is
// a claim with no evidence behind it, put to a model that complies with
// the last thing it was told. Naming "it is already done" as an allowed
// answer is what gives a finished model somewhere to go that is not more
// work.
func TestTheCarryOnPromptLetsTheModelSayItIsDone(t *testing.T) {
	for _, claim := range []string{"you ended your turn with the task unfinished", "you described the next step"} {
		if strings.Contains(strings.ToLower(keepGoingPrompt), claim) {
			t.Errorf("the prompt asserts the task is unfinished (%q), which is what makes a finished model redo its work", claim)
		}
	}
	lower := strings.ToLower(keepGoingPrompt)
	if !strings.Contains(lower, "complete") && !strings.Contains(lower, "done") {
		t.Error("the prompt never offers 'it is already complete' as an answer")
	}
	if !strings.Contains(lower, "redo") && !strings.Contains(lower, "re-run") {
		t.Error("the prompt does not tell the model to leave finished work alone")
	}
}

// The feature exists for one family. A profile's own keep_going number
// used to be able to opt any model in, and the daemon-wide switch could
// be read as arming it everywhere; neither is wanted, because a nudge
// sent to a model without the habit is localcode second-guessing a
// finished answer.
func TestKeepGoingIsMuseOnly(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")

	for _, tc := range []struct {
		model string
		extra int
		want  int
	}{
		{"muse-glimmer-30b", 0, 3},   // the family default
		{"MUSE-GLIMMER-30B", 0, 3},   // case does not matter
		{"my-Muse-variant-7b", 0, 3}, // any variant of the family
		{"muse-test", 5, 5},          // profile override, muse
		{"muse-test", -1, -1},        // profile opt-out, muse
		{"gemma-3-27b-it", 0, 0},     // another family: nothing
		{"gemma-3-27b-it", 5, 0},     // even asked for by the profile
		{"claude-sonnet-4-6", 3, 0},  // even asked for by the profile
		{"qwen3-30b-a3b", 0, 0},      // nothing
	} {
		got := loop.effectiveKeepGoing(config.Profile{Model: tc.model, KeepGoing: tc.extra})
		if got != tc.want {
			t.Errorf("effectiveKeepGoing(%q, keep_going=%d) = %d, want %d", tc.model, tc.extra, got, tc.want)
		}
	}
}

// The daemon-wide switch kills it for muse too, which is what
// "/keep-going off" means.
func TestTheKeepGoingSwitchGatesTheFamily(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	profile := config.Profile{Model: "muse-glimmer-30b"}

	if got := loop.effectiveKeepGoing(profile); got != 3 {
		t.Fatalf("budget with the switch on = %d, want 3", got)
	}
	loop.SetKeepGoingEnabled(false)
	if got := loop.effectiveKeepGoing(profile); got != 0 {
		t.Errorf("budget with the switch off = %d, want 0", got)
	}
}

// Only a change clears the carry-on guard; looking again does not.
//
// Measured against the real model this feature exists for, on a task it
// had already finished: told to check whether the work was complete, it
// ran `grep timeout` and then `grep 30`. Neither was a repeat, so under
// the old test both counted as work, both cleared the guard, and both
// bought another nudge — nine requests became thirteen, four of them
// re-confirming a change already made.
func TestOnlyAChangeClearsTheCarryOnGuard(t *testing.T) {
	call := func(name string) provider.Block {
		return provider.Block{Type: provider.BlockToolUse, ToolName: name, ToolInput: []byte(`{}`)}
	}
	for _, c := range []struct {
		name  string
		calls []provider.Block
		want  bool
	}{
		{"an edit is a change", []provider.Block{call("edit")}, true},
		{"a write is a change", []provider.Block{call("write_file")}, true},
		{"a command may be a change", []provider.Block{call("bash")}, true},
		{"reading is looking", []provider.Block{call("read_file")}, false},
		{"grepping is looking", []provider.Block{call("grep")}, false},
		{"globbing is looking", []provider.Block{call("glob")}, false},
		{"re-running the check is looking", []provider.Block{call("check")}, false},
		{"delegating is not a change this turn made", []provider.Block{call("Task")}, false},
		{"the exact pair the model reached for", []provider.Block{call("grep"), call("grep")}, false},
		{"looking and then fixing is a change", []provider.Block{call("grep"), call("edit")}, true},
		{"nothing at all", nil, false},
	} {
		if got := changedSomething(c.calls); got != c.want {
			t.Errorf("%s: changedSomething = %v, want %v", c.name, got, c.want)
		}
	}
}

// A model prodded into looking somewhere new does not earn another prod.
//
// This is the fault the guard was rebuilt for, and it was measured on the
// model the feature exists for. Told to check whether an already-finished
// task was complete, a 30B muse ran `grep timeout`, then `grep 30`.
// Neither was a repeat, so under the old test both counted as work, both
// cleared the guard, and both bought another carry-on: nine requests
// became thirteen, four of them re-confirming a change already made.
func TestLookingSomewhereNewDoesNotEarnAnother(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(lookingTool{runs: &runs})

	// Look, stall, look, stall, look: every step is a different call and
	// not one of them changes anything.
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		lookCall("one"),
		textStream("one more thing to do."),
		lookCall("two"),
		textStream("one more thing to do."),
		lookCall("three"),
		textStream("one more thing to do."),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 3)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// Four requests: the first reply, one carry-on, the look it produced,
	// and the stall after it — which earns nothing, because looking is not
	// progress. Under the old rule this ran to the whole budget.
	if p.sentCount() != 4 {
		t.Errorf("provider turns = %d, want 4 — looking somewhere new bought another carry-on", p.sentCount())
	}
	if runs != 2 {
		t.Errorf("the tool ran %d times, want 2", runs)
	}
}

type lookingTool struct{ runs *int }

func (t lookingTool) Name() string        { return "grep" }
func (t lookingTool) Description() string { return "search the project" }
func (t lookingTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t lookingTool) RequiresPermission(json.RawMessage) bool { return false }
func (t lookingTool) Execute(context.Context, json.RawMessage) tools.Result {
	*t.runs++
	return tools.Result{Content: "no matches"}
}

func lookCall(arg string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: "let me look"},
		{Type: provider.EventToolUseStart, ToolUseID: "look_" + arg, ToolName: "grep"},
		{Type: provider.EventToolUseEnd, ToolUseID: "look_" + arg, ToolInput: json.RawMessage(`{"pattern":"` + arg + `"}`)},
		{Type: provider.EventMessageStop, StopReason: "tool_calls"},
	}
}
