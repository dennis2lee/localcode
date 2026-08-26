package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Prompt caching, from the request side.
//
// The stable half of an agent request is the tool schemas and the system
// prompt, and in a long session it is the same bytes every turn. Marking
// it is the single largest cost saving available: a cache read is about a
// tenth of the price of reading it again.

func captureAnthropicRequest(t *testing.T, req ChatRequest) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	p := &AnthropicDirect{BaseURL: srv.URL, APIKey: "k"}
	stream, err := p.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range stream {
	}
	return body
}

func TestAnthropicMarksTheStablePrefixWhenAsked(t *testing.T) {
	body := captureAnthropicRequest(t, ChatRequest{
		Model:       "claude-opus-5",
		System:      "you are helpful",
		Messages:    []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}},
		Tools:       []Tool{{Name: "a", InputSchema: json.RawMessage(`{}`)}, {Name: "b", InputSchema: json.RawMessage(`{}`)}},
		MaxTokens:   100,
		CachePrefix: true,
	})

	// System becomes a one-block array so a breakpoint can be attached.
	sys, ok := body["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("system = %#v, want a single content block", body["system"])
	}
	block := sys[0].(map[string]any)
	if block["text"] != "you are helpful" {
		t.Errorf("system text = %v", block["text"])
	}
	if block["cache_control"] == nil {
		t.Error("the system prompt carries no cache breakpoint")
	}

	// The breakpoint goes on the last tool, so everything before it —
	// which is every tool — is the cached prefix.
	tools := body["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("got %d tools", len(tools))
	}
	if tools[0].(map[string]any)["cache_control"] != nil {
		t.Error("a breakpoint was put on a tool that is not the last one")
	}
	if tools[1].(map[string]any)["cache_control"] == nil {
		t.Error("the last tool carries no cache breakpoint")
	}
}

// Unasked, the request on the wire is exactly what it has always been:
// system as a plain string, no cache_control anywhere.
func TestAnthropicSendsTheOldShapeWhenNotAsked(t *testing.T) {
	body := captureAnthropicRequest(t, ChatRequest{
		Model:     "claude-opus-5",
		System:    "you are helpful",
		Messages:  []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}},
		Tools:     []Tool{{Name: "a", InputSchema: json.RawMessage(`{}`)}},
		MaxTokens: 100,
	})
	if _, isString := body["system"].(string); !isString {
		t.Errorf("system = %#v, want the plain string it has always been", body["system"])
	}
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		if tools[0].(map[string]any)["cache_control"] != nil {
			t.Error("a cache breakpoint was sent without being asked for")
		}
	}
}

// The read count is the only way to tell a working breakpoint from one
// that is silently doing nothing, so it has to survive the parse.
func TestAnthropicReportsWhatTheCacheDid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":0,"cache_read_input_tokens":4096,"cache_creation_input_tokens":128}}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_delta","usage":{"output_tokens":9}}`+"\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	p := &AnthropicDirect{BaseURL: srv.URL, APIKey: "k"}
	stream, err := p.Chat(context.Background(), ChatRequest{Model: "m", MaxTokens: 10})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var last StreamEvent
	for ev := range stream {
		if ev.Type == EventUsage {
			last = ev
		}
	}
	if last.CacheReadTokens != 4096 || last.CacheWriteTokens != 128 {
		t.Errorf("cache tokens = read %d, write %d; want 4096 and 128", last.CacheReadTokens, last.CacheWriteTokens)
	}
	// message_delta repeats neither, so they have to be carried forward
	// rather than reported as zero on the last usage event.
	if last.OutputTokens != 9 {
		t.Errorf("output tokens = %d, want the final count", last.OutputTokens)
	}
}

func TestBedrockAddsACachePointWhenAsked(t *testing.T) {
	cfg, err := toBedrockTools([]Tool{{Name: "a", InputSchema: json.RawMessage(`{}`)}}, true)
	if err != nil {
		t.Fatalf("toBedrockTools: %v", err)
	}
	if len(cfg.Tools) != 2 {
		t.Fatalf("got %d entries, want the tool and a cache point after it", len(cfg.Tools))
	}
	plain, err := toBedrockTools([]Tool{{Name: "a", InputSchema: json.RawMessage(`{}`)}}, false)
	if err != nil {
		t.Fatalf("toBedrockTools: %v", err)
	}
	if len(plain.Tools) != 1 {
		t.Errorf("got %d entries without asking, want just the tool", len(plain.Tools))
	}
}
