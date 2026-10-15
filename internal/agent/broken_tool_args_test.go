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

// brokenArgs is the shape of the call that stopped a conversation: an
// ask_user call whose last option escaped the quote meant to close it, and
// whose closing brace never came.
const brokenArgs = `{"command": "pick one", "options": ["port it", "explain first (list every change)\""]`

// litellmLike is an openai-compat endpoint that converts each request the
// way LiteLLM does on its way to an Anthropic-format backend: every tool
// call already in the history has its arguments parsed, and one that will
// not parse refuses the whole request with a 400 that echoes the messages.
// The first request is answered with a call carrying brokenArgs, and every
// accepted request after it with text.
type litellmLike struct {
	mu       sync.Mutex
	requests int
	refused  int
	accepted []map[string]any
}

func (s *litellmLike) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests++
	first := s.requests == 1
	s.mu.Unlock()

	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		calls, _ := m.(map[string]any)["tool_calls"].([]any)
		for _, c := range calls {
			args := c.(map[string]any)["function"].(map[string]any)["arguments"].(string)
			var v map[string]any
			if err := json.Unmarshal([]byte(args), &v); err != nil {
				s.mu.Lock()
				s.refused++
				s.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"error":{"message":"litellm.BadRequestError: AnthropicException - Failed to parse tool call arguments: %s. Received Messages=%s"}}`,
					err, strings.Repeat("the conversation, echoed back, mentions quota and a server error. ", 200))
				return
			}
		}
	}
	s.mu.Lock()
	s.accepted = append(s.accepted, req)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	if first {
		delta := map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "bash", "arguments": brokenArgs},
		}}}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
	} else {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"carrying on.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// A model that writes a tool call's arguments as something other than a JSON
// object does not end the conversation. The call is not run, its result says
// what was wrong and how the arguments ended, and the history goes back to the
// endpoint with {} in their place, so the next request and every one after it
// is accepted. They used to go back as written: the endpoint refused the
// request, and every request after it, since each carried the same history.
func TestBrokenToolArgumentsDoNotStopTheConversation(t *testing.T) {
	srv := &litellmLike{}
	model := httptest.NewServer(srv)
	defer model.Close()
	loop, store := newCustomCommandTestLoop(t, model.URL, nil)
	runs := 0
	loop.Tools.Register(countingTool{runs: &runs})
	sess, err := store.CreateSession("s1", "", "general-purpose", true)
	if err != nil {
		t.Fatal(err)
	}

	if err := loop.SendMessage(context.Background(), sess.ID, "", "ask me how to proceed"); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sess.ID, "", "port it"); err != nil {
		t.Fatalf("the turn after: %v", err)
	}

	if runs != 0 {
		t.Errorf("the tool ran %d times on arguments that were not JSON", runs)
	}
	if srv.refused != 0 {
		t.Errorf("the endpoint refused %d requests over the call's arguments", srv.refused)
	}
	if len(srv.accepted) != 3 {
		t.Fatalf("%d requests accepted, want 3: the call, its result, the next message", len(srv.accepted))
	}

	var args, result string
	for _, m := range srv.accepted[1]["messages"].([]any) {
		msg := m.(map[string]any)
		if calls, ok := msg["tool_calls"].([]any); ok && len(calls) > 0 {
			args = calls[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
		}
		if msg["role"] == "tool" {
			result, _ = msg["content"].(string)
		}
	}
	if args != "{}" {
		t.Errorf("the call went back with arguments %q, want {}", args)
	}
	for _, want := range []string{"not run", "not a JSON object", "unexpected end of JSON input", `(list every change)\""]`, "Call bash again"} {
		if !strings.Contains(result, want) {
			t.Errorf("the result the model read does not say %q:\n%s", want, result)
		}
	}

	evs, err := store.Events(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Type == events.TypeError {
			t.Errorf("an error reached the transcript: %v", ev.Data)
		}
		if ev.Type == events.TypeToolEnd && ev.Data["input"] != brokenArgs {
			t.Errorf("the log keeps the arguments as %v, want what the model wrote", ev.Data["input"])
		}
	}
}

// recordingTool keeps the arguments it was called with.
type recordingTool struct {
	name string
	got  *[]string
}

func (t recordingTool) Name() string        { return t.name }
func (t recordingTool) Description() string { return "records its input" }
func (t recordingTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t recordingTool) RequiresPermission(json.RawMessage) bool { return false }
func (t recordingTool) Execute(_ context.Context, input json.RawMessage) tools.Result {
	*t.got = append(*t.got, string(input))
	return tools.Result{Content: "ran"}
}

func runOne(t *testing.T, loop *Loop, sessionID, name, raw string) string {
	t.Helper()
	call := provider.Block{Type: provider.BlockToolUse, ToolUseID: "call_" + name, ToolName: name, ToolInput: json.RawMessage(raw)}
	blocks, _, _ := loop.runTools(context.Background(), sessionID, []provider.Block{call}, nil, 1<<20)
	if len(blocks) == 0 {
		t.Fatalf("no result for %s(%s)", name, raw)
	}
	return blocks[0].ToolResultContent
}

// Through runTools: nothing and null are a call with no arguments, and the
// tool is given {}, which is what the history says it was given. A JSON
// object runs as written. Anything else is not handed to the tool.
func TestRunToolsHandsAToolOnlyAnObject(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	var got []string
	loop.Tools.Register(recordingTool{name: "probe", got: &got})
	sess, err := store.CreateSession("s1", "", "general-purpose", true)
	if err != nil {
		t.Fatal(err)
	}
	for raw, want := range map[string]string{"": "{}", "  ": "{}", "null": "{}", ` {"a": 1} `: ` {"a": 1} `} {
		got = nil
		if res := runOne(t, loop, sess.ID, "probe", raw); res != "ran" || len(got) != 1 || got[0] != want {
			t.Errorf("arguments %q: result %q, tool given %q, want it given %q", raw, res, got, want)
		}
	}
	for _, raw := range []string{`[1]`, `"text"`, `7`, `{"a":`, `{"a":1}}`} {
		got = nil
		res := runOne(t, loop, sess.ID, "probe", raw)
		if len(got) != 0 || !strings.Contains(res, "not run") || !strings.Contains(res, "Call probe again") {
			t.Errorf("arguments %q: tool given %q, result %q", raw, got, res)
		}
	}
}

// The name is judged before the arguments. A tool that does not exist is
// said not to exist, rather than the model being told to call it again; a
// decorated name that resolves to a tool is told to call that tool.
func TestBrokenArgumentsToAToolThatDoesNotExist(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	var got []string
	loop.Tools.Register(recordingTool{name: "probe", got: &got})
	sess, err := store.CreateSession("s1", "", "general-purpose", true)
	if err != nil {
		t.Fatal(err)
	}
	if res := runOne(t, loop, sess.ID, "nonesuch", `{"a":`); strings.Contains(res, "Call nonesuch again") || !strings.Contains(res, "probe") {
		t.Errorf("a tool that does not exist, with broken arguments: %q", res)
	}
	if res := runOne(t, loop, sess.ID, "functions.probe", `{"a":`); !strings.Contains(res, "Call probe again") || len(got) != 0 {
		t.Errorf("a decorated name with broken arguments: %q, tool given %q", res, got)
	}
}

// A bash call that was never run changed nothing, so it does not pay for
// another carry-on nudge.
func TestAnUnrunCallChangedNothing(t *testing.T) {
	broken := provider.Block{Type: provider.BlockToolUse, ToolName: "bash", ToolInput: json.RawMessage(`{"command": "make`)}
	if changedSomething([]provider.Block{broken}) {
		t.Error("a bash call with broken arguments counted as a change")
	}
	whole := provider.Block{Type: provider.BlockToolUse, ToolName: "bash", ToolInput: json.RawMessage(`{"command": "make"}`)}
	if !changedSomething([]provider.Block{broken, whole}) {
		t.Error("a bash call that ran did not count as a change")
	}
}
