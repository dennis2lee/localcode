package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- pure translation functions ---

func TestToAnthropicMessagesTextAndToolBlocks(t *testing.T) {
	input := json.RawMessage(`{"pattern":"*.go"}`)
	msgs := []Message{
		{Role: RoleUser, Content: []Block{TextBlock("hi")}},
		{Role: RoleAssistant, Content: []Block{
			TextBlock("let me check"),
			{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "glob", ToolInput: input},
		}},
		{Role: RoleUser, Content: []Block{ToolResultBlock("call_1", "file1.go", false)}},
	}
	out := toAnthropicMessages(msgs)

	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(out), out)
	}
	if out[0].Role != "user" || out[0].Content[0].Type != "text" || out[0].Content[0].Text != "hi" {
		t.Errorf("out[0] = %+v", out[0])
	}
	if out[1].Role != "assistant" || len(out[1].Content) != 2 {
		t.Fatalf("out[1] = %+v", out[1])
	}
	if out[1].Content[1].Type != "tool_use" || out[1].Content[1].ID != "call_1" || out[1].Content[1].Name != "glob" {
		t.Errorf("tool_use block = %+v", out[1].Content[1])
	}
	if string(out[1].Content[1].Input) != string(input) {
		t.Errorf("tool_use input = %s, want %s", out[1].Content[1].Input, input)
	}
	if out[2].Content[0].Type != "tool_result" || out[2].Content[0].ToolUseID != "call_1" || out[2].Content[0].Content != "file1.go" {
		t.Errorf("tool_result block = %+v", out[2].Content[0])
	}
}

func TestToAnthropicMessagesToolUseEmptyInputDefaultsToObject(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, Content: []Block{{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "bash"}}},
	}
	out := toAnthropicMessages(msgs)
	if string(out[0].Content[0].Input) != "{}" {
		t.Errorf("Input = %s, want \"{}\" when ToolInput is empty", out[0].Content[0].Input)
	}
}

func TestToAnthropicTools(t *testing.T) {
	tools := []Tool{{Name: "glob", Description: "list files", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	out := toAnthropicTools(tools)
	if len(out) != 1 || out[0].Name != "glob" || out[0].Description != "list files" {
		t.Errorf("out = %+v", out)
	}
	if string(out[0].InputSchema) != `{"type":"object"}` {
		t.Errorf("InputSchema = %s", out[0].InputSchema)
	}
}

func TestMapAnthropicStopReason(t *testing.T) {
	cases := []struct{ in, want string }{
		{"tool_use", "tool_use"},
		{"max_tokens", "max_tokens"},
		{"end_turn", "end_turn"},
		{"stop_sequence", "end_turn"},
		{"", "end_turn"},
	}
	for _, c := range cases {
		if got := mapAnthropicStopReason(c.in); got != c.want {
			t.Errorf("mapAnthropicStopReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- full Chat() streaming integration, against a real httptest SSE server ---

func sseLine(v any) string {
	data, _ := json.Marshal(v)
	return fmt.Sprintf("data: %s\n\n", data)
}

func TestAnthropicChatSendsAuthHeadersAndStreamsText(t *testing.T) {
	var gotAPIKey, gotVersion, gotPath string
	var gotBody anthRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, sseLine(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text"}}))
		fmt.Fprint(w, sseLine(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "hello"}}))
		fmt.Fprint(w, sseLine(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": " world"}}))
		fmt.Fprint(w, sseLine(map[string]any{"type": "content_block_stop", "index": 0}))
		fmt.Fprint(w, sseLine(map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": "end_turn"}}))
		fmt.Fprint(w, sseLine(map[string]any{"type": "message_stop"}))
		flusher.Flush()
	}))
	defer srv.Close()

	p := NewAnthropicDirect("sk-ant-test123")
	p.BaseURL = srv.URL

	stream, err := p.Chat(context.Background(), ChatRequest{
		Model:     "claude-sonnet-5",
		System:    "be nice",
		Messages:  []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var text string
	var stopReason string
	for ev := range stream {
		switch ev.Type {
		case EventTextDelta:
			text += ev.TextDelta
		case EventMessageStop:
			stopReason = ev.StopReason
		case EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}

	if gotAPIKey != "sk-ant-test123" {
		t.Errorf("x-api-key = %q, want %q", gotAPIKey, "sk-ant-test123")
	}
	if gotVersion != anthropicAPIVersion {
		t.Errorf("anthropic-version = %q, want %q", gotVersion, anthropicAPIVersion)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("path = %q, want %q", gotPath, "/v1/messages")
	}
	if gotBody.Model != "claude-sonnet-5" || gotBody.System != "be nice" {
		t.Errorf("request body = %+v", gotBody)
	}
	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}
	if stopReason != "end_turn" {
		t.Errorf("stopReason = %q, want %q", stopReason, "end_turn")
	}
}

func TestAnthropicChatAccumulatesToolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, sseLine(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "tool_use", "id": "call_1", "name": "glob"}}))
		fmt.Fprint(w, sseLine(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": `{"pattern":`}}))
		fmt.Fprint(w, sseLine(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": `"*.go"}`}}))
		fmt.Fprint(w, sseLine(map[string]any{"type": "content_block_stop", "index": 0}))
		fmt.Fprint(w, sseLine(map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": "tool_use"}}))
		fmt.Fprint(w, sseLine(map[string]any{"type": "message_stop"}))
		flusher.Flush()
	}))
	defer srv.Close()

	p := NewAnthropicDirect("key")
	p.BaseURL = srv.URL

	stream, err := p.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var sawStart bool
	var toolInput string
	var stopReason string
	for ev := range stream {
		switch ev.Type {
		case EventToolUseStart:
			sawStart = true
			if ev.ToolUseID != "call_1" || ev.ToolName != "glob" {
				t.Errorf("start event = %+v", ev)
			}
		case EventToolUseEnd:
			toolInput = string(ev.ToolInput)
		case EventMessageStop:
			stopReason = ev.StopReason
		}
	}

	if !sawStart {
		t.Error("expected an EventToolUseStart")
	}
	if toolInput != `{"pattern":"*.go"}` {
		t.Errorf("accumulated tool input = %q, want %q", toolInput, `{"pattern":"*.go"}`)
	}
	if stopReason != "tool_use" {
		t.Errorf("stopReason = %q, want %q", stopReason, "tool_use")
	}
}

func TestAnthropicChatErrorEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, sseLine(map[string]any{"type": "error", "error": map[string]string{"type": "overloaded_error", "message": "try again"}}))
		flusher.Flush()
	}))
	defer srv.Close()

	p := NewAnthropicDirect("key")
	p.BaseURL = srv.URL

	stream, err := p.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var sawError bool
	for ev := range stream {
		if ev.Type == EventError {
			sawError = true
			if ev.Err == nil {
				t.Error("expected a non-nil error")
			}
		}
	}
	if !sawError {
		t.Error("expected an EventError for the stream's error event")
	}
}

func TestAnthropicChatNonOKStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	defer srv.Close()

	p := NewAnthropicDirect("bad-key")
	p.BaseURL = srv.URL

	_, err := p.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}}})
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}
