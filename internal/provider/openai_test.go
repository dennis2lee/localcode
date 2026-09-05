package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These exercise only the pure translation functions between the internal
// block model and the OpenAI chat/completions wire format — no HTTP, no
// network.

func TestToOpenAIMessagesSystemAndText(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []Block{TextBlock("hi")}},
		{Role: RoleAssistant, Content: []Block{TextBlock("hello back")}},
	}
	out := toOpenAIMessages("be nice", msgs)

	if len(out) != 3 {
		t.Fatalf("expected 3 messages (system+user+assistant), got %d: %+v", len(out), out)
	}
	if out[0].Role != "system" || out[0].Content != "be nice" {
		t.Errorf("out[0] = %+v, want system/\"be nice\"", out[0])
	}
	if out[1].Role != "user" || out[1].Content != "hi" {
		t.Errorf("out[1] = %+v, want user/\"hi\"", out[1])
	}
	if out[2].Role != "assistant" || out[2].Content != "hello back" {
		t.Errorf("out[2] = %+v, want assistant/\"hello back\"", out[2])
	}
}

func TestToOpenAIMessagesNoSystem(t *testing.T) {
	out := toOpenAIMessages("", []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}})
	if len(out) != 1 {
		t.Fatalf("expected 1 message with no system prompt, got %d: %+v", len(out), out)
	}
}

func TestToOpenAIMessagesToolResultBecomesToolRole(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []Block{ToolResultBlock("call_1", "file1.go", false)}},
	}
	out := toOpenAIMessages("", msgs)

	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(out), out)
	}
	if out[0].Role != "tool" {
		t.Errorf("role = %q, want %q", out[0].Role, "tool")
	}
	if out[0].ToolCallID != "call_1" {
		t.Errorf("ToolCallID = %q, want %q", out[0].ToolCallID, "call_1")
	}
	if out[0].Content != "file1.go" {
		t.Errorf("Content = %q, want %q", out[0].Content, "file1.go")
	}
}

func TestToOpenAIMessagesMixedUserTextAndToolResult(t *testing.T) {
	// A single user-role Message can carry both a tool_result block (from
	// agent.Loop's tool feedback turn) — the tool_result must split into
	// its own role:"tool" message rather than being merged into the user
	// text.
	msgs := []Message{
		{Role: RoleUser, Content: []Block{
			ToolResultBlock("call_1", "result text", false),
			TextBlock("also some text"),
		}},
	}
	out := toOpenAIMessages("", msgs)

	if len(out) != 2 {
		t.Fatalf("expected 2 messages (tool + user), got %d: %+v", len(out), out)
	}
	if out[0].Role != "tool" || out[0].Content != "result text" {
		t.Errorf("out[0] = %+v", out[0])
	}
	if out[1].Role != "user" || out[1].Content != "also some text" {
		t.Errorf("out[1] = %+v", out[1])
	}
}

func TestToOpenAIMessagesAssistantToolUse(t *testing.T) {
	input := json.RawMessage(`{"pattern":"*.go"}`)
	msgs := []Message{
		{Role: RoleAssistant, Content: []Block{
			TextBlock("let me check"),
			{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "glob", ToolInput: input},
		}},
	}
	out := toOpenAIMessages("", msgs)

	if len(out) != 1 {
		t.Fatalf("expected 1 assistant message, got %d: %+v", len(out), out)
	}
	if out[0].Content != "let me check" {
		t.Errorf("Content = %q, want %q", out[0].Content, "let me check")
	}
	if len(out[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %+v", out[0].ToolCalls)
	}
	tc := out[0].ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "glob" {
		t.Errorf("tool call = %+v", tc)
	}
	if tc.Function.Arguments != string(input) {
		t.Errorf("arguments = %q, want %q", tc.Function.Arguments, string(input))
	}
}

func TestToOpenAITools(t *testing.T) {
	tools := []Tool{
		{Name: "glob", Description: "list files", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	out := toOpenAITools(tools)

	if len(out) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(out))
	}
	if out[0].Type != "function" {
		t.Errorf("Type = %q, want %q", out[0].Type, "function")
	}
	if out[0].Function.Name != "glob" || out[0].Function.Description != "list files" {
		t.Errorf("Function = %+v", out[0].Function)
	}
	if string(out[0].Function.Parameters) != `{"type":"object"}` {
		t.Errorf("Parameters = %s", out[0].Function.Parameters)
	}
}

func TestToOpenAIToolsEmpty(t *testing.T) {
	out := toOpenAITools(nil)
	if len(out) != 0 {
		t.Errorf("expected empty slice, got %+v", out)
	}
}

func TestMapFinishReason(t *testing.T) {
	cases := []struct{ in, want string }{
		{"tool_calls", "tool_use"},
		{"length", "max_tokens"},
		{"stop", "end_turn"},
		{"", "end_turn"},
		{"something_unexpected", "end_turn"},
	}
	for _, c := range cases {
		if got := mapFinishReason(c.in); got != c.want {
			t.Errorf("mapFinishReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestOpenAICompatChatRequestsAndEmitsUsage confirms the request sets
// stream_options.include_usage and that a final usage-only chunk (empty
// "choices") is translated into an EventUsage rather than being silently
// dropped.
func TestOpenAICompatChatRequestsAndEmitsUsage(t *testing.T) {
	var gotBody oaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":123,\"completion_tokens\":45}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	p := NewOpenAICompat(srv.URL, "")
	stream, err := p.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var usageEvents []StreamEvent
	for ev := range stream {
		if ev.Type == EventUsage {
			usageEvents = append(usageEvents, ev)
		}
	}

	if gotBody.StreamOptions == nil || !gotBody.StreamOptions.IncludeUsage {
		t.Errorf("request StreamOptions = %+v, want IncludeUsage=true", gotBody.StreamOptions)
	}
	if len(usageEvents) != 1 {
		t.Fatalf("expected 1 usage event, got %d: %+v", len(usageEvents), usageEvents)
	}
	if usageEvents[0].InputTokens != 123 || usageEvents[0].OutputTokens != 45 {
		t.Errorf("usage event = %+v, want InputTokens=123 OutputTokens=45", usageEvents[0])
	}
}

// The OpenAI API sends usage on a final chunk with no choices, but vLLM
// and several compatible proxies attach it to a chunk that still carries
// one. Reading it only in the no-choices case dropped their token counts
// silently, and the context meter never moved.
func TestOpenAIUsageOnAChunkThatAlsoHasAChoice(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}

data: [DONE]

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	p := NewOpenAICompat(srv.URL, "")
	ch, err := p.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: []Block{{Type: BlockText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	var in, out int
	var text string
	for ev := range ch {
		switch ev.Type {
		case EventUsage:
			in, out = ev.InputTokens, ev.OutputTokens
		case EventTextDelta:
			text += ev.TextDelta
		}
	}
	if in != 11 || out != 7 {
		t.Errorf("usage = %d in / %d out, want 11/7", in, out)
	}
	if text != "hi" {
		t.Errorf("text = %q; the choice on the usage chunk was dropped", text)
	}
}

// collect drains a stream into a slice, for the tool-call tests below.
func collect(t *testing.T, body string) []StreamEvent {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	p := NewOpenAICompat(srv.URL, "")
	ch, err := p.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var out []StreamEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func stopReasonOf(evs []StreamEvent) string {
	for _, ev := range evs {
		if ev.Type == EventMessageStop {
			return ev.StopReason
		}
	}
	return ""
}

func toolEnds(evs []StreamEvent) []StreamEvent {
	var out []StreamEvent
	for _, ev := range evs {
		if ev.Type == EventToolUseEnd {
			out = append(out, ev)
		}
	}
	return out
}

// A local server that streams tool calls and then says finish_reason
// "stop" is describing a reply that asked for tools. Reported as end_turn,
// the loop ended the turn with the calls never run: the model said what it
// was about to do and then stopped for no visible reason.
func TestToolCallsWithFinishReasonStopAreStillToolUse(t *testing.T) {
	evs := collect(t, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read_file","arguments":"{\"path\":\"x\"}"}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`)
	if got := stopReasonOf(evs); got != "tool_use" {
		t.Errorf("stop reason = %q, want %q", got, "tool_use")
	}
	if ends := toolEnds(evs); len(ends) != 1 {
		t.Fatalf("tool_use_end events = %d, want 1", len(ends))
	}
}

// A reply with no tool calls means what it says.
func TestPlainFinishReasonStopStaysEndTurn(t *testing.T) {
	evs := collect(t, `data: {"choices":[{"delta":{"content":"done"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`)
	if got := stopReasonOf(evs); got != "end_turn" {
		t.Errorf("stop reason = %q, want %q", got, "end_turn")
	}
}

// Some servers close the stream after [DONE] without ever sending a
// finish_reason. The tool calls they streamed used to be left in the
// accumulator and dropped with the goroutine.
func TestToolCallsSurviveAStreamWithNoFinishReason(t *testing.T) {
	evs := collect(t, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"bash","arguments":"{}"}}]}}]}

data: [DONE]

`)
	ends := toolEnds(evs)
	if len(ends) != 1 {
		t.Fatalf("tool_use_end events = %d, want 1 — the call was dropped", len(ends))
	}
	if ends[0].ToolUseID != "call_a" {
		t.Errorf("tool_use_id = %q, want %q", ends[0].ToolUseID, "call_a")
	}
	if got := stopReasonOf(evs); got != "tool_use" {
		t.Errorf("stop reason = %q, want %q", got, "tool_use")
	}
}

// A tool call with no id at all: the start event never fired, so the call
// was never registered and never ran. One is made up instead, and both
// halves have to agree on it or the result matches nothing.
func TestToolCallWithoutAnIDGetsOne(t *testing.T) {
	evs := collect(t, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"bash","arguments":"{}"}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`)
	var startID, endID, name string
	for _, ev := range evs {
		switch ev.Type {
		case EventToolUseStart:
			startID, name = ev.ToolUseID, ev.ToolName
		case EventToolUseEnd:
			endID = ev.ToolUseID
		}
	}
	if name != "bash" {
		t.Errorf("tool name = %q, want %q — the call was never started", name, "bash")
	}
	if startID == "" || startID != endID {
		t.Errorf("tool_use ids: start %q, end %q — want one non-empty id used by both", startID, endID)
	}
}

// A server that streams the function name in pieces.
//
// Allowed by the OpenAI streaming shape and done by several local
// servers: the name arrives across deltas for one index, the way the
// arguments do. Nothing here is hypothetical about the consequence — the
// name is what picks which tool runs.
func TestAToolNameSplitAcrossDeltasIsPutBackTogether(t *testing.T) {
	evs := collect(t, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read_"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"file"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"x\"}"}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`)
	var name string
	for _, ev := range evs {
		if ev.Type == EventToolUseStart {
			name = ev.ToolName
		}
	}
	if name != "read_file" {
		t.Errorf("tool name = %q, want read_file", name)
	}
}

// tool_choice "none" goes on the wire alongside the tools it constrains,
// and never without them: the field on a request with no tools is one
// OpenAI refuses. The tools themselves stay, which is the point — a
// local server's prefix cache is keyed on the rendered prompt, and the
// tool schemas are the front of it.
func TestToolChoiceNoneKeepsTheToolsAndConstrainsThem(t *testing.T) {
	var bodies []oaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b oaRequest
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"DONE\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAICompat(srv.URL, "")
	tool := Tool{Name: "bash", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`)}
	for _, req := range []ChatRequest{
		{Model: "m", Messages: []Message{{Role: "user", Content: []Block{{Type: BlockText, Text: "hi"}}}}, Tools: []Tool{tool}, ToolChoice: ToolChoiceNone},
		{Model: "m", Messages: []Message{{Role: "user", Content: []Block{{Type: BlockText, Text: "hi"}}}}, Tools: []Tool{tool}},
		{Model: "m", Messages: []Message{{Role: "user", Content: []Block{{Type: BlockText, Text: "hi"}}}}, ToolChoice: ToolChoiceNone},
	} {
		ch, err := p.Chat(context.Background(), req)
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		for range ch {
		}
	}
	if len(bodies) != 3 {
		t.Fatalf("requests = %d, want 3", len(bodies))
	}
	if bodies[0].ToolChoice != "none" || len(bodies[0].Tools) != 1 {
		t.Errorf("constrained request: tool_choice=%q tools=%d, want none and 1", bodies[0].ToolChoice, len(bodies[0].Tools))
	}
	if bodies[1].ToolChoice != "" {
		t.Errorf("ordinary request carries tool_choice=%q", bodies[1].ToolChoice)
	}
	if bodies[2].ToolChoice != "" {
		t.Errorf("a request with no tools carries tool_choice=%q, which the API refuses", bodies[2].ToolChoice)
	}
}
