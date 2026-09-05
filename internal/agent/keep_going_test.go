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
// model that has finished — which is why the model is asked, and asked
// with the tools out of reach: see keepGoingVerdictPrompt.
//
// The scripts below therefore have a shape: a stop is followed by the
// model's answer to the question ("DONE", or "MORE" and what remains),
// and only after MORE does the carry-on go out and the next piece of
// work come back.

// textStream is a reply with no tool calls in it: the shape of both "I am
// done" and "here is what still needs doing".
func textStream(text string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: text},
		{Type: provider.EventMessageStop, StopReason: "end_turn"},
	}
}

// done and more are the model's answers to the question keep_going asks.
func done() []provider.StreamEvent { return textStream("DONE") }
func more(what string) []provider.StreamEvent {
	return textStream("MORE\n" + what)
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
		more("global_init.cpp still has to be updated."),
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
	// The first step, the stall, the question and its answer, the second
	// step, and the reply after it — which is past the budget of one, so
	// it is not even questioned.
	if p.sentCount() != 5 {
		t.Errorf("provider turns = %d, want 5", p.sentCount())
	}

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

	// Work, stall, "more", work, stall, "more", work, stall: a model that
	// would keep this up for as long as it is asked to. Each step CHANGES
	// something, which is what makes it progress — a step that only looks
	// somewhere new does not earn another carry-on, and a step that
	// repeats itself does not either. See
	// TestLookingSomewhereNewDoesNotEarnAnother and
	// TestACarryOnThatOnlyRepeatsItselfEndsTheTurn.
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		distinctToolCall("one"),
		textStream("one more thing to do."),
		more("one more thing."),
		distinctToolCall("two"),
		textStream("one more thing to do."),
		more("one more thing."),
		distinctToolCall("three"),
		textStream("one more thing to do."),
		more("one more thing."),
		distinctToolCall("four"),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 2)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// Eight requests: the first step and its stall, then twice over a
	// question, its answer, a step and a stall — and the third stall is
	// past the budget, so it is not questioned and ends the turn.
	if p.sentCount() != 8 {
		t.Errorf("provider turns = %d, want 8", p.sentCount())
	}
	if runs != 3 {
		t.Errorf("the tool ran %d times, want 3", runs)
	}
}

// A finished model is asked, says so, and that is the end of it. This is
// the whole cost of the setting on a completed task: one request with a
// one-word answer, and the tool is not run again.
//
// It used to be more. The question and the carry-on were one message,
// sent with the tools on, and a finished model told to check its work
// checked it — grep, find, grep again — and every one of those bought
// another round. See keepGoingVerdictPrompt for the measurement.
func TestAFinishedModelIsAskedOnceAndTakenAtItsWord(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		distinctToolCall("edit"),
		textStream("that is the whole change."),
		done(),
		distinctToolCall("check-again"), // never asked for
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 3)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if p.sentCount() != 3 {
		t.Errorf("provider turns = %d, want 3 — the work, the question, and the answer", p.sentCount())
	}
	if runs != 1 {
		t.Errorf("the tool ran %d times, want 1 — a finished model was made to work again", runs)
	}
}

// The question goes out with the tools defined but not callable, and the
// carry-on with them callable again. Defined, not removed: a local
// server's prefix cache is keyed on the rendered prompt, and the tool
// schemas are the front of it, so dropping them turns a one-word answer
// into a re-read of the whole conversation.
func TestTheQuestionGoesOutWithTheToolsOutOfReach(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		distinctToolCall("one"),
		textStream("one more thing to do."),
		more("one more thing."),
		distinctToolCall("two"),
		textStream("done."),
		done(),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 3)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reqs := p.sentRequests()
	if len(reqs) != 6 {
		t.Fatalf("provider turns = %d, want 6", len(reqs))
	}
	for i, want := range []string{"", "", provider.ToolChoiceNone, "", "", provider.ToolChoiceNone} {
		if reqs[i].ToolChoice != want {
			t.Errorf("request %d: tool_choice = %q, want %q", i+1, reqs[i].ToolChoice, want)
		}
		if len(reqs[i].Tools) == 0 {
			t.Errorf("request %d carries no tools — the prefix cache is lost on it", i+1)
		}
	}
}

// A server that does not carry tool_choice sends the question to a model
// with the tools in reach, and the model may answer by working. That
// reply is the carry-on, and it is counted as one, so the budget still
// bounds the turn.
func TestAServerThatIgnoresTheChoiceStillGetsABoundedCarryOn(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		distinctToolCall("one"),
		textStream("one more thing to do."),
		distinctToolCall("two"), // the answer to the question, with a tool call in it
		textStream("one more thing to do."),
		distinctToolCall("three"), // never asked for: the budget is spent
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 1)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if runs != 2 {
		t.Errorf("the tool ran %d times, want 2", runs)
	}
	if p.sentCount() != 4 {
		t.Errorf("provider turns = %d, want 4 — the carry-on the model took by itself was not counted", p.sentCount())
	}
}

// An answer that names neither word ends the turn. The cost of a carry-on
// the model did not ask for is the loop this feature is measured by; the
// cost of a missed one is the person typing "continue".
func TestAnAnswerThatSaysNeitherEndsTheTurn(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		distinctToolCall("one"),
		textStream("the change is in."),
		textStream("Let me think about what the task was."),
		distinctToolCall("two"), // never asked for
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 3)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if p.sentCount() != 3 {
		t.Errorf("provider turns = %d, want 3", p.sentCount())
	}
	if runs != 1 {
		t.Errorf("the tool ran %d times, want 1", runs)
	}
}

func TestTheAnswerIsReadFromItsFirstLine(t *testing.T) {
	for text, want := range map[string]stopVerdict{
		"DONE":                                      stopDone,
		"done.":                                     stopDone,
		"**DONE** — both files are updated.":        stopDone,
		"Done: the build passes.":                   stopDone,
		"Yes, the task is complete.":                stopDone,
		"The task is complete.":                     stopDone,
		"Nothing remains to be done.":               stopDone,
		"완료했습니다.":                                   stopDone,
		"No further changes are needed.":            stopDone,
		"No more work is needed.":                   stopDone,
		"All done.":                                 stopDone,
		"MORE":                                      stopMore,
		"MORE\nglobal_init.go still calls it.":      stopMore,
		"- MORE: config.go":                         stopMore,
		"Not yet — config.go still calls it.":       stopMore,
		"No, global_init.go still needs the change": stopMore,
		"The task is not complete.":                 stopMore,
		"Incomplete.":                               stopMore,
		"config.go still needs the change.":         stopMore,
		"Two callers remain to be updated.":         stopMore,
		"MORE: nothing else after that.":            stopMore,
		"아직 남았습니다.":                                 stopMore,
		"":                                          stopUnclear,
		"Let me think about what the task was.":     stopUnclear,
		"I changed two files.":                      stopUnclear,
	} {
		if got := parseVerdict(text); got != want {
			t.Errorf("parseVerdict(%q) = %v, want %v", text, got, want)
		}
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
		more("global_init.cpp."),
		distinctToolCall("edit-global-init"),
		textStream("done — the build passes."),
		done(),
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

// The question and the carry-on are part of the conversation the model
// had, so they have to survive a daemon restart. They used to live only
// in memory: rehydrating the event log rebuilt the history with two
// assistant messages back to back — a shape Bedrock rejects outright —
// and a "continue" the model was never re-told about.
func TestACarriedOnTurnSurvivesARestart(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		distinctToolCall("edit-main"),
		textStream("global_init.cpp also has to be updated."),
		more("global_init.cpp."),
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
	var question, carryOn bool
	for _, m := range got {
		// Matched against the prompts themselves, not phrases copied out
		// of them: the wording is tuned from time to time and a test that
		// pins a sentence would fail for that instead of for this.
		if m.Role != provider.RoleUser {
			continue
		}
		text := replyText(m.Content)
		if strings.Contains(text, keepGoingVerdictPrompt) {
			question = true
		}
		if strings.Contains(text, keepGoingPrompt) {
			carryOn = true
		}
	}
	if !question || !carryOn {
		t.Errorf("rehydrated history has question=%v carry-on=%v, want both — the model would remember a different conversation", question, carryOn)
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

// A model that answers MORE and then only repeats a call it has already
// made has changed nothing, and the next stop is not questioned again.
// The guard under the question: a model can say MORE for as long as it
// is asked, and the budget alone would let it re-run the build three
// times to admire the result.
func TestACarryOnThatOnlyRepeatsItselfEndsTheTurn(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		distinctToolCall("build"),
		textStream("done — the build passes."),
		more("let me re-check the build."),
		distinctToolCall("build"), // the same call, to check its own work
		textStream("still done."),
		more("let me re-check the build."), // never asked for
		distinctToolCall("build"),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 3)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// The work, the stop, the question and MORE, the repeat, and the
	// stop after it: five requests, not the ten a budget of three allows.
	if p.sentCount() != 5 {
		t.Errorf("provider turns = %d, want 5 — a finished task was carried on %d times", p.sentCount(), (p.sentCount()-2)/2)
	}
	// Twice: the real work, and the one repeat the single carry-on bought.
	if runs != 2 {
		t.Errorf("the tool ran %d times, want 2 — finished work was re-executed", runs)
	}
}

// The question must not assert that the work is unfinished.
//
// localcode cannot tell a stall from a finished task — that is the whole
// premise of this file — so a sentence claiming the task is incomplete is
// a claim with no evidence behind it, put to a model that complies with
// the last thing it was told. The question names both answers, and it
// asks for an answer rather than for work, because the work is what a
// finished model does when told to check.
func TestTheQuestionNamesBothAnswersAndAsksForNoWork(t *testing.T) {
	for _, claim := range []string{"you ended your turn with the task unfinished", "you described the next step", "not finished"} {
		if strings.Contains(strings.ToLower(keepGoingVerdictPrompt), claim) {
			t.Errorf("the question asserts the task is unfinished (%q), which is what makes a finished model redo its work", claim)
		}
	}
	for _, word := range []string{"DONE", "MORE"} {
		if !strings.Contains(keepGoingVerdictPrompt, word) {
			t.Errorf("the question never offers %s as an answer", word)
		}
	}
	if !strings.Contains(strings.ToLower(keepGoingVerdictPrompt), "answer only") {
		t.Error("the question does not tell the model to answer rather than work")
	}
	// And the carry-on may take the model at its word: it is only ever
	// sent after MORE.
	if !strings.Contains(strings.ToLower(keepGoingPrompt), "next step") {
		t.Error("the carry-on does not tell the model to take the next step")
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
// The guard under the question. A model can answer MORE, look somewhere
// it has not looked, and stop again; under the old rule that counted as
// work and bought the next round, and measured on the model the feature
// exists for it ran the whole budget on `grep timeout`, `grep 30`. Only
// a change clears the guard; see changedSomething.
func TestLookingSomewhereNewDoesNotEarnAnother(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(lookingTool{runs: &runs})

	// Look, stall, "more", look, stall: the second look changes nothing,
	// so the second stall is not questioned.
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		lookCall("one"),
		textStream("one more thing to do."),
		more("one more thing."),
		lookCall("two"),
		textStream("one more thing to do."),
		more("one more thing."), // never asked for
		lookCall("three"),
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 3)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if p.sentCount() != 5 {
		t.Errorf("provider turns = %d, want 5 — looking somewhere new bought another carry-on", p.sentCount())
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

// Stopping a turn stops it. Nothing is asked afterwards.
//
// A cancelled stream closes without a terminal event, which the loop
// reads as a model that stopped after running tools — the exact shape
// keep_going exists for. Left alone, the question was appended to the
// history, the request carrying it died on the dead context, and the
// next thing the person typed arrived underneath "Is the task you were
// given complete?".
func TestStoppingATurnAsksTheModelNothing(t *testing.T) {
	runs := 0
	reg := tools.NewRegistry(nil)
	reg.Register(countingTool{runs: &runs})

	ctx, cancel := context.WithCancel(context.Background())
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		distinctToolCall("one"),
		// What a cancelled stream looks like from here: no blocks, no
		// terminal event. The harness cancels first so the loop reaches
		// this reply with a context already done.
		{},
	}}
	loop, sessionID := keepGoingLoop(t, p, reg, 3)
	loop.Tools.Resolver = func(context.Context, tools.Query) tools.Outcome {
		cancel()
		return tools.Outcome{Decision: tools.DecisionAllow}
	}

	_ = loop.SendMessage(ctx, sessionID, "general-purpose", "do it")

	for _, m := range loop.history(sessionID) {
		if m.Role != provider.RoleUser {
			continue
		}
		if text := replyText(m.Content); strings.Contains(text, keepGoingVerdictPrompt) || strings.Contains(text, keepGoingPrompt) {
			t.Fatalf("a stopped turn left localcode's own words in the history: %q", text)
		}
	}
	evs, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, ev := range evs {
		if ev.Type != events.TypeUserMessage {
			continue
		}
		if auto, _ := ev.Data["auto"].(bool); auto {
			t.Errorf("a stopped turn logged an automatic message: %v", ev.Data["text"])
		}
	}
}
