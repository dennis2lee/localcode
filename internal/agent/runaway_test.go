package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// A turn that will not end.
//
// The loop has exactly one reason to stop: the model stops asking for
// tools. Everything here is about the case where it does not — which is
// not hypothetical. A debate reviewer that had already recorded its
// verdict called Verdict again a thousand times, held the session busy,
// and every message typed afterwards was injected into that turn rather
// than starting one of its own, so ordinary prompts looked as though they
// were being run by the debate.

func reviewing() context.Context {
	return withReviewerTools(context.Background(), []string{"read_file", verdictToolName})
}

// A verdict is the end of a reviewer's work. It used to say so in prose
// and hope: "the debate ends here; nothing further is needed from you".
func TestARecordedVerdictEndsTheReviewersTurn(t *testing.T) {
	for _, in := range []string{
		`{"approved":true,"summary":"looks fine"}`,
		`{"approved":false,"findings":"the retry has no ceiling"}`,
	} {
		res := VerdictTool{}.Execute(reviewing(), json.RawMessage(in))
		if res.IsError {
			t.Fatalf("Verdict(%s) errored: %s", in, res.Content)
		}
		if !res.EndsTurn {
			t.Errorf("Verdict(%s) does not end the turn, so the loop asks the reviewer again", in)
		}
	}
}

// The one branch that is not terminal, because it asks to be called
// again. Ending the turn here would lose the review.
func TestAVerdictWithNoFindingsAsksAgain(t *testing.T) {
	res := VerdictTool{}.Execute(reviewing(), json.RawMessage(`{"approved":false}`))
	if res.EndsTurn {
		t.Error("a verdict with no findings ended the turn; it asks to be called again")
	}
	if !strings.Contains(res.Content, "again") {
		t.Errorf("the result does not ask for another call: %q", res.Content)
	}
}

// The same shape, one feature over: a stage that has answered in its
// declared shape is finished, and left to itself a model calls Answer
// again with the same fields. This is the loop the orchestration harness
// showed before it was understood as the same defect.
func TestAnAnsweredStageEndsItsTurn(t *testing.T) {
	ctx := withStageAnswer(context.Background(), Stage{
		Name: "find", Returns: map[string]string{"findings": "strings"},
	})
	res := AnswerTool{}.Execute(ctx, json.RawMessage(`{"findings":["something"]}`))
	if res.IsError {
		t.Fatalf("Answer errored: %s", res.Content)
	}
	if !res.EndsTurn {
		t.Error("an answered stage does not end its turn, so the loop asks it again")
	}
}

// An answer that is not yet an answer keeps the turn, for the same reason
// a verdict with no findings does: it asks to be called again.
func TestAnIncompleteAnswerKeepsTheTurn(t *testing.T) {
	ctx := withStageAnswer(context.Background(), Stage{
		Name: "find", Returns: map[string]string{"findings": "strings", "why": "string"},
	})
	res := AnswerTool{}.Execute(ctx, json.RawMessage(`{"findings":["something"]}`))
	if !res.IsError {
		t.Fatal("an answer missing a declared field was accepted")
	}
	if res.EndsTurn {
		t.Error("an incomplete answer ended the turn, losing the stage")
	}
}

func toolUse(name, input string) []provider.Block {
	return []provider.Block{{
		Type: provider.BlockToolUse, ToolUseID: "t1",
		ToolName: name, ToolInput: json.RawMessage(input),
	}}
}

// The backstop for every tool that is not terminal. Repetition rather
// than a step count, so a long turn doing real work is untouched.
func TestRepeatingTheSameCallsReachesTheCeiling(t *testing.T) {
	seen := map[string]bool{}
	same := toolUse("bash", `{"command":"go test ./..."}`)

	if !newWork(seen, same) {
		t.Fatal("the first call was not counted as new work, so this proves nothing")
	}

	repeats := 0
	for range maxRepeatSteps + 2 {
		if newWork(seen, same) {
			repeats = 0
			continue
		}
		repeats++
	}
	if repeats < maxRepeatSteps {
		t.Fatalf("identical calls produced %d repeats, want the ceiling at %d to be reachable", repeats, maxRepeatSteps)
	}

	// And one genuinely new call resets it, which is what keeps alive a
	// turn that edits a file and then re-runs the same command.
	if !newWork(seen, toolUse("edit", `{"path":"a.go"}`)) {
		t.Error("a call never made before was not counted as new work")
	}
}

// A ceiling of one would end a turn the first time a model repeated
// anything, which ordinary work does.
func TestTheCeilingLeavesRoomForOrdinaryRepetition(t *testing.T) {
	if maxRepeatSteps < 2 {
		t.Fatalf("maxRepeatSteps is %d; below 2 a single repeated call ends a turn", maxRepeatSteps)
	}
}

// A model that will not stop, driven through the actual loop.
//
// The tests above prove the two tools set EndsTurn. This one proves the
// loop honours it — which is the half that was broken, and the half a
// test of the tool alone cannot see. The server answers with the same
// tool call every time and gives up after a bound of its own, so a
// regression fails loudly instead of hanging.

const runawayCutoff = 25

// alwaysCalls is a model that asks for one tool, with one set of
// arguments, on every request it is given.
type alwaysCalls struct {
	mu    sync.Mutex
	tool  string
	input string
	calls int
}

func (m *alwaysCalls) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		m.mu.Lock()
		m.calls++
		over := m.calls > runawayCutoff
		m.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		defer w.(http.Flusher).Flush()
		if over {
			// The escape hatch, so a regression is a failed assertion
			// rather than a test that never returns.
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"giving up"}}]}`+"\n\n")
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		esc, _ := json.Marshal(m.input)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":%q,\"arguments\":%s}}]}}]}\n\n", m.tool, esc)
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (m *alwaysCalls) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// terminalFake is a tool that is the end of the work, the way Verdict and
// Answer are, without needing a debate or an orchestration to exist.
type terminalFake struct{ runs int }

func (terminalFake) Name() string                            { return "finish" }
func (terminalFake) Description() string                     { return "end the work" }
func (terminalFake) InputSchema() json.RawMessage            { return json.RawMessage(`{"type":"object"}`) }
func (terminalFake) RequiresPermission(json.RawMessage) bool { return false }
func (f *terminalFake) Execute(context.Context, json.RawMessage) tools.Result {
	f.runs++
	return tools.Result{Content: "recorded", EndsTurn: true}
}

func TestTheLoopStopsWhenATerminalToolHasRun(t *testing.T) {
	model := &alwaysCalls{tool: "finish", input: `{}`}
	loop := newSmartLoop(t, model.server(t).URL)
	fake := &terminalFake{}
	loop.Tools.Register(fake)
	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if fake.runs != 1 {
		t.Errorf("the terminal tool ran %d times; the turn should end on the first", fake.runs)
	}
	// One request to ask for the tool. Anything more means the loop went
	// back to the model after the work was over.
	if n := model.count(); n != 1 {
		t.Errorf("the loop made %d model calls after a terminal tool; want 1", n)
	}
}

func TestTheLoopStopsAModelThatRepeatsItself(t *testing.T) {
	model := &alwaysCalls{tool: "bash", input: `{"command":"ls"}`}
	loop := newSmartLoop(t, model.server(t).URL)
	loop.Tools.Register(&namedFake{name: "bash"})
	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	n := model.count()
	if n > runawayCutoff {
		t.Fatalf("the loop ran past its own escape hatch at %d calls; it is unbounded", runawayCutoff)
	}
	// The first call is the work; the ceiling counts the repeats after it.
	if n > maxRepeatSteps+2 {
		t.Errorf("a model repeating one call was asked %d times, want it stopped by %d", n, maxRepeatSteps+2)
	}

	// And it says why, rather than ending in a silence that looks like a
	// finished turn.
	evs, err := loop.Store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	said := false
	for _, e := range evs {
		if e.Type == events.TypeError && strings.Contains(dataString(e.Data, "error"), "Nothing new was tried") {
			said = true
		}
	}
	if !said {
		t.Error("the turn was cut short with nothing in the log saying why")
	}
}

// The ceiling is the person's to move. Off means a turn is never ended
// for repeating itself, which is what somebody watching a model think by
// re-reading may want; a number means that many nothing-new steps.
func TestTheRepeatGuardHonoursTheLiveLimit(t *testing.T) {
	repeatNotices := func(loop *Loop) (int, string) {
		evs, err := loop.Store.Events("s1", 0)
		if err != nil {
			t.Fatal(err)
		}
		n, last := 0, ""
		for _, e := range evs {
			if e.Type == events.TypeError && strings.Contains(dataString(e.Data, "error"), "Nothing new was tried") {
				n++
				last = dataString(e.Data, "error")
			}
		}
		return n, last
	}

	t.Run("off", func(t *testing.T) {
		model := &alwaysCalls{tool: "bash", input: `{"command":"ls"}`}
		loop := newSmartLoop(t, model.server(t).URL)
		loop.Tools.Register(&namedFake{name: "bash"})
		loop.SetRepeatLimit(0)
		if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
		if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "do it"); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		// With the guard off the only thing that ends this turn is the
		// fake model giving up, which is the point: nothing here did.
		if n := model.count(); n <= maxRepeatSteps+2 {
			t.Errorf("the guard was off and the turn still ended after %d calls", n)
		}
		if n, _ := repeatNotices(loop); n != 0 {
			t.Errorf("the guard was off and still wrote %d notice(s)", n)
		}
	})

	t.Run("six", func(t *testing.T) {
		model := &alwaysCalls{tool: "bash", input: `{"command":"ls"}`}
		loop := newSmartLoop(t, model.server(t).URL)
		loop.Tools.Register(&namedFake{name: "bash"})
		loop.SetRepeatLimit(6)
		if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
		if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "do it"); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		if n := model.count(); n > 6+2 || n <= maxRepeatSteps+2 {
			t.Errorf("with a limit of 6 the model was asked %d times; want between %d and %d", n, maxRepeatSteps+3, 6+2)
		}
		n, last := repeatNotices(loop)
		if n != 1 {
			t.Fatalf("%d notices, want one", n)
		}
		// Named, because "the same tools with the same arguments" was
		// read as one call repeated when the rule is about steps that
		// add nothing, and the calls are what make that legible.
		for _, want := range []string{"6 steps in a row", `bash {"command":"ls"}`, "/repeat-limit"} {
			if !strings.Contains(last, want) {
				t.Errorf("notice lacks %q: %s", want, last)
			}
		}
	})
}
