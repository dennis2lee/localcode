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

// Item 25. The conversation is the bigger half of a long session, and it
// used to carry no breakpoint at all. The strategy is the last block of
// each of the last two messages: the history is append-only, so the
// previous request's marked prefix is a prefix of this one and reads at
// the cache rate, with only the new suffix written at the premium.
func TestAnthropicMarksTheConversationTailWhenAsked(t *testing.T) {
	body := captureAnthropicRequest(t, ChatRequest{
		Model:  "claude-opus-5",
		System: "you are helpful",
		Messages: []Message{
			{Role: RoleUser, Content: []Block{TextBlock("first")}},
			{Role: RoleAssistant, Content: []Block{TextBlock("reply")}},
			{Role: RoleUser, Content: []Block{TextBlock("second")}},
		},
		MaxTokens:   100,
		CachePrefix: true,
	})

	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("got %d messages", len(msgs))
	}
	control := func(i int) any {
		blocks := msgs[i].(map[string]any)["content"].([]any)
		return blocks[len(blocks)-1].(map[string]any)["cache_control"]
	}
	if control(0) != nil {
		t.Error("a breakpoint reached past the last two messages")
	}
	if control(1) == nil || control(2) == nil {
		t.Error("the last two messages carry no cache breakpoints")
	}
}

// Unasked, the conversation stays untouched — the moving breakpoints are
// part of the same opt-in as the stable ones.
func TestAnthropicLeavesTheConversationAloneWhenNotAsked(t *testing.T) {
	body := captureAnthropicRequest(t, ChatRequest{
		Model:  "claude-opus-5",
		System: "you are helpful",
		Messages: []Message{
			{Role: RoleUser, Content: []Block{TextBlock("first")}},
			{Role: RoleUser, Content: []Block{TextBlock("second")}},
		},
		MaxTokens: 100,
	})
	for i, m := range body["messages"].([]any) {
		for _, b := range m.(map[string]any)["content"].([]any) {
			if b.(map[string]any)["cache_control"] != nil {
				t.Errorf("message %d carries a breakpoint without CachePrefix", i)
			}
		}
	}
}

// Four is the API's limit, and the request must stay inside it however
// the conversation is shaped: two stable marks plus at most two moving
// ones.
func TestAnthropicNeverExceedsFourBreakpoints(t *testing.T) {
	msgs := []Message{}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, Message{Role: RoleUser, Content: []Block{TextBlock(fmt.Sprintf("m%d", i))}})
	}
	body := captureAnthropicRequest(t, ChatRequest{
		Model:       "claude-opus-5",
		System:      "you are helpful",
		Messages:    msgs,
		Tools:       []Tool{{Name: "a", InputSchema: json.RawMessage(`{}`)}},
		MaxTokens:   100,
		CachePrefix: true,
	})
	count := 0
	if sys, ok := body["system"].([]any); ok {
		for _, b := range sys {
			if b.(map[string]any)["cache_control"] != nil {
				count++
			}
		}
	}
	for _, tl := range body["tools"].([]any) {
		if tl.(map[string]any)["cache_control"] != nil {
			count++
		}
	}
	for _, m := range body["messages"].([]any) {
		for _, b := range m.(map[string]any)["content"].([]any) {
			if b.(map[string]any)["cache_control"] != nil {
				count++
			}
		}
	}
	if count > 4 {
		t.Errorf("%d breakpoints on one request; the API allows 4", count)
	}
}

// The assembly's seams survive to the wire: a request that arrives with
// system blocks is sent as an array of system blocks, one per prompt
// asset in order, with the cache mark on the last — one breakpoint that
// caches the whole stable prefix. The fold to one string is only for a
// request that never had blocks.
func TestAnthropicSendsSystemBlocksAsThemselves(t *testing.T) {
	body := captureAnthropicRequest(t, ChatRequest{
		Model:  "claude-opus-5",
		System: "BASE\n\nRULES",
		SystemBlocks: []SystemBlock{
			{Text: "BASE", Asset: "system.base"},
			{Text: "RULES", Asset: "project.rules"},
		},
		Messages:    []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}},
		MaxTokens:   64,
		CachePrefix: true,
	})

	sys, ok := body["system"].([]any)
	if !ok {
		t.Fatalf("system = %T, want an array of blocks", body["system"])
	}
	if len(sys) != 2 {
		t.Fatalf("system has %d blocks, want the assembly's 2", len(sys))
	}
	first := sys[0].(map[string]any)
	last := sys[1].(map[string]any)
	if first["text"] != "BASE" || last["text"] != "RULES" {
		t.Errorf("blocks arrived as %v then %v, want BASE then RULES", first["text"], last["text"])
	}
	if _, marked := first["cache_control"]; marked {
		t.Error("the cache mark is not on the last block")
	}
	if _, marked := last["cache_control"]; !marked {
		t.Error("the last system block carries no cache mark, so the stable prefix is not cached")
	}
}
