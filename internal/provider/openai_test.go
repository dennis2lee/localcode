package provider

import (
	"encoding/json"
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
