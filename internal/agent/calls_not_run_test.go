package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// A tool call the turn decides not to run still gets a result.
//
// Two replies have calls that are not run: one cut off by max_tokens, whose
// last call may be half written, and one that arrives after the agent's
// step limit, when the request offered no tools. Both used to leave the
// call in the history with nothing after it. Every provider refuses that
// shape, so every request after the reply failed, on every provider, for
// the rest of the session. A conversation moved to another endpoint
// carried the same broken turn with it.

// runCountingTool counts its runs, so a call that should not run is seen not
// to have, and one that should is seen to have run once.
type runCountingTool struct{ runs *atomic.Int32 }

func (t runCountingTool) Name() string        { return "echo" }
func (t runCountingTool) Description() string { return "echo" }
func (t runCountingTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)
}
func (t runCountingTool) RequiresPermission(json.RawMessage) bool { return false }
func (t runCountingTool) Execute(context.Context, json.RawMessage) tools.Result {
	t.runs.Add(1)
	return tools.Result{Content: "ok"}
}

func textReply(text string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: text},
		{Type: provider.EventMessageStop, StopReason: "end_turn"},
	}
}

func callReply(id, input, stopReason string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: "calling it"},
		{Type: provider.EventToolUseStart, ToolUseID: id, ToolName: "echo"},
		{Type: provider.EventToolUseEnd, ToolUseID: id, ToolInput: json.RawMessage(input)},
		{Type: provider.EventMessageStop, StopReason: stopReason},
	}
}

// everyCallAnswered fails the test for a call in msgs with no result in the
// message right after it, and for a call whose input is not JSON.
func everyCallAnswered(t *testing.T, where string, msgs []provider.Message) {
	t.Helper()
	for i, m := range msgs {
		for _, b := range m.Content {
			if b.Type != provider.BlockToolUse {
				continue
			}
			if !json.Valid(b.ToolInput) {
				t.Errorf("%s: call %s carries input %q, which is not JSON", where, b.ToolUseID, b.ToolInput)
			}
			answered := false
			if i+1 < len(msgs) {
				for _, r := range msgs[i+1].Content {
					if r.Type == provider.BlockToolResult && r.ToolUseID == b.ToolUseID {
						answered = true
					}
				}
			}
			if !answered {
				t.Errorf("%s: call %s in message %d has no result in the message after it", where, b.ToolUseID, i)
			}
		}
	}
}

// callInput is the input one call carries in a history, or false.
func callInput(msgs []provider.Message, id string) (string, bool) {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == provider.BlockToolUse && b.ToolUseID == id {
				return string(b.ToolInput), true
			}
		}
	}
	return "", false
}

// resultFor is the result block for one call in a history, or false.
func resultFor(msgs []provider.Message, id string) (provider.Block, bool) {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == provider.BlockToolResult && b.ToolUseID == id {
				return b, true
			}
		}
	}
	return provider.Block{}, false
}

// sameHistory compares what a model is sent, block by block, ignoring
// fields a rebuilt history fills differently and no request depends on.
func sameHistory(t *testing.T, live, rebuilt []provider.Message) {
	t.Helper()
	type shape struct {
		Role, Type, ID, Text, Input, Result string
		IsError                             bool
	}
	flat := func(msgs []provider.Message) []shape {
		var out []shape
		for _, m := range sendableHistory(msgs) {
			for _, b := range m.Content {
				out = append(out, shape{string(m.Role), string(b.Type), b.ToolUseID, b.Text, string(b.ToolInput), b.ToolResultContent, b.IsError})
			}
		}
		return out
	}
	if a, b := flat(live), flat(rebuilt); !reflect.DeepEqual(a, b) {
		t.Errorf("a restarted daemon would send a different history\nlive:    %+v\nrebuilt: %+v", a, b)
	}
}

func TestACallFromACutOffReplyIsAnsweredAndNotRun(t *testing.T) {
	var runs atomic.Int32
	reg := tools.NewRegistry(nil)
	reg.Register(runCountingTool{runs: &runs})
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		callReply("call_1", `{"text":"hel`, "max_tokens"),
		textReply("next answer"),
	}}
	loop, sessionID := scriptedLoop(t, p, reg)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("first message: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "and now?"); err != nil {
		t.Fatalf("second message: %v", err)
	}
	if n := runs.Load(); n != 0 {
		t.Errorf("a call from a reply cut off by max_tokens ran %d time(s)", n)
	}

	p.mu.Lock()
	reqs := append([]provider.ChatRequest(nil), p.requests...)
	p.mu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want 2", len(reqs))
	}
	everyCallAnswered(t, "the request after the cut-off", reqs[1].Messages)

	res, ok := resultFor(reqs[1].Messages, "call_1")
	if !ok {
		t.Fatal("the cut-off call has no result in the next request")
	}
	if !res.IsError || !strings.Contains(res.ToolResultContent, "not run") || !strings.Contains(res.ToolResultContent, "max_tokens") {
		t.Errorf("result = %+v, want an error saying the call was not run and why", res)
	}

	evs, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	sameHistory(t, loop.history(sessionID), rehydrateHistory(evs))
}

func TestACallAfterTheStepLimitIsAnsweredAndNotRun(t *testing.T) {
	var runs atomic.Int32
	reg := tools.NewRegistry(nil)
	reg.Register(runCountingTool{runs: &runs})
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		callReply("call_1", `{"text":"one"}`, "tool_use"),
		// The request offers no tools now, and this server sends a call
		// anyway.
		callReply("call_2", `{"text":"two"}`, "tool_use"),
		textReply("next answer"),
	}}
	loop, sessionID := scriptedLoop(t, p, reg)
	loop.Config.Agents["general-purpose"] = config.AgentConfig{Profile: "balanced", Steps: 1}

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("first message: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "and now?"); err != nil {
		t.Fatalf("second message: %v", err)
	}
	if n := runs.Load(); n != 1 {
		t.Errorf("the tool ran %d time(s), want 1: the call before the limit only", n)
	}

	p.mu.Lock()
	reqs := append([]provider.ChatRequest(nil), p.requests...)
	p.mu.Unlock()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want 3", len(reqs))
	}
	everyCallAnswered(t, "the request after the step limit", reqs[2].Messages)
	// Whole arguments stay as they arrived: only input that is not JSON
	// is replaced.
	if in, ok := callInput(reqs[2].Messages, "call_2"); !ok || in != `{"text":"two"}` {
		t.Errorf("the call after the limit went into the history with input %q (found %v), want it as it arrived", in, ok)
	}
	res, ok := resultFor(reqs[2].Messages, "call_2")
	if !ok || !res.IsError || !strings.Contains(res.ToolResultContent, "limit") {
		t.Errorf("result for the call after the limit = %+v (found %v), want an error naming the limit", res, ok)
	}

	evs, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	sameHistory(t, loop.history(sessionID), rehydrateHistory(evs))
}

// A reply that runs its calls is untouched: its input goes into the history
// as it arrived, and nothing is answered for it twice.
func TestCallsNotRunIsNothingForAReplyThatRunsItsCalls(t *testing.T) {
	call := provider.Block{Type: provider.BlockToolUse, ToolUseID: "c", ToolName: "echo", ToolInput: json.RawMessage(`{"a":1}`)}
	for _, stop := range []string{"tool_use", "end_turn", "stop", ""} {
		if got, why := callsNotRun([]provider.Block{call}, false, stop); got != nil || why != "" {
			t.Errorf("stop reason %q: %v %q, want nothing", stop, got, why)
		}
	}
	if got, _ := callsNotRun(nil, true, "max_tokens"); got != nil {
		t.Errorf("a reply with no calls has calls not run: %v", got)
	}

	blocks := []provider.Block{provider.TextBlock("x"), call, {Type: provider.BlockToolUse, ToolUseID: "d", ToolInput: json.RawMessage(`{"a":`)}}
	out := withSendableInputs(blocks, blocks[1:])
	if string(out[1].ToolInput) != `{"a":1}` {
		t.Errorf("an input that is JSON was changed: %s", out[1].ToolInput)
	}
	if string(out[2].ToolInput) != `{}` {
		t.Errorf("a half-written input went into the history as %s", out[2].ToolInput)
	}
	if string(blocks[2].ToolInput) != `{"a":` {
		t.Error("the reply as it arrived was changed in place")
	}
}
