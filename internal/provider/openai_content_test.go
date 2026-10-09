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

// Every message on the OpenAI wire carries "content", and an assistant turn
// that is only tool calls carries null.
//
// The key was left out whenever a message had no text. A LiteLLM proxy in
// front of a model served in Anthropic's shape refused that with a 400
// naming the key, "AnthropicException - 'content'", and a session moved
// from Bedrock to that endpoint could not send another turn. The history
// below is the shape such a session has: thinking before each tool call,
// tool calls with no text, a result that came back empty, a turn that
// holds only reasoning, and an image.

// sentMessages runs one request through Chat and returns its messages as the
// server decoded them, as raw JSON, so a key that is missing is told apart
// from one that is null or empty. Decoding into oaRequest would not tell.
func sentMessages(t *testing.T, msgs []Message) []map[string]json.RawMessage {
	t.Helper()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ch, err := NewOpenAICompat(srv.URL, "").Chat(context.Background(), ChatRequest{Model: "m", System: "be brief", Messages: msgs})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range ch {
	}
	var req struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("the request is not JSON: %v\n%s", err, body)
	}
	return req.Messages
}

func toolUse(id, name, input string) Block {
	return Block{Type: BlockToolUse, ToolUseID: id, ToolName: name, ToolInput: json.RawMessage(input)}
}

func thinking(text string) Block {
	return Block{Type: BlockThinking, Text: text, Signature: "sig"}
}

func TestEveryMessageOnTheOpenAIWireCarriesContent(t *testing.T) {
	history := []Message{
		{Role: RoleUser, Content: []Block{TextBlock("understand this project")}},
		{Role: RoleAssistant, Content: []Block{thinking("look first"), toolUse("tooluse_a", "bash", `{"command":"ls"}`)}},
		{Role: RoleUser, Content: []Block{ToolResultBlock("tooluse_a", "README.md", false)}},
		{Role: RoleAssistant, Content: []Block{thinking("read it"), TextBlock("Reading the README."), toolUse("tooluse_b", "read_file", `{"path":"README.md"}`)}},
		{Role: RoleUser, Content: []Block{ToolResultBlock("tooluse_b", "", false)}},
		{Role: RoleAssistant, Content: []Block{thinking("nothing was said after this")}},
		{Role: RoleUser, Content: []Block{TextBlock("what is in this picture"), ImageBlock("image/png", []byte{0x89, 'P', 'N', 'G'})}},
		{Role: RoleAssistant, Content: []Block{TextBlock("A logo.")}},
		{Role: RoleUser, Content: []Block{TextBlock("go on")}},
	}
	got := sentMessages(t, history)

	want := []struct {
		role, content string
	}{
		{"system", `"be brief"`},
		{"user", `"understand this project"`},
		{"assistant", `null`},
		{"tool", `"README.md"`},
		{"assistant", `"Reading the README."`},
		{"tool", `""`},
		{"assistant", `""`},
		{"user", ""}, // an array of parts, checked below
		{"assistant", `"A logo."`},
		{"user", `"go on"`},
	}
	if len(got) != len(want) {
		t.Fatalf("%d messages sent, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		m := got[i]
		var role string
		_ = json.Unmarshal(m["role"], &role)
		if role != w.role {
			t.Errorf("message %d: role %q, want %q", i, role, w.role)
		}
		content, ok := m["content"]
		if !ok {
			raw, _ := json.Marshal(m)
			t.Errorf("message %d (%s) has no \"content\" key: %s", i, role, raw)
			continue
		}
		if w.content == "" {
			var parts []oaContentPart
			if err := json.Unmarshal(content, &parts); err != nil || len(parts) != 2 {
				t.Errorf("message %d: content %s, want the text and the image as two parts", i, content)
			}
			continue
		}
		if string(content) != w.content {
			t.Errorf("message %d (%s): content %s, want %s", i, role, content, w.content)
		}
	}

	// The tool calls are still there beside the null, and the turn that
	// had text kept both.
	for _, i := range []int{2, 4} {
		var calls []oaToolCall
		if err := json.Unmarshal(got[i]["tool_calls"], &calls); err != nil || len(calls) != 1 {
			t.Errorf("message %d: tool_calls %s, want one call", i, got[i]["tool_calls"])
		}
	}
}

// The value decides more than whether a key is there, so each case is
// pinned on its own, through the encoder the request uses.
func TestTheContentOfEachMessageShape(t *testing.T) {
	call := oaToolCall{ID: "c1", Type: "function"}
	call.Function.Name = "bash"
	call.Function.Arguments = "{}"
	cases := []struct {
		name string
		msg  oaMessage
		want string
	}{
		{"tool calls alone are null, as OpenAI returns them", oaMessage{Role: "assistant", ToolCalls: []oaToolCall{call}}, `null`},
		{"text beside tool calls is the text", oaMessage{Role: "assistant", Content: "running it", ToolCalls: []oaToolCall{call}}, `"running it"`},
		{"an assistant turn with nothing is an empty string", oaMessage{Role: "assistant"}, `""`},
		{"an empty tool result is an empty string", oaMessage{Role: "tool", ToolCallID: "c1"}, `""`},
		{"text is text", oaMessage{Role: "user", Content: "hi"}, `"hi"`},
		{"parts are an array", oaMessage{Role: "user", Content: "hi", MultiContent: []oaContentPart{{Type: "text", Text: "hi"}}}, `[{"type":"text","text":"hi"}]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.msg)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			content, ok := m["content"]
			if !ok {
				t.Fatalf("no \"content\" key: %s", raw)
			}
			if string(content) != c.want {
				t.Errorf("content %s, want %s (whole message %s)", content, c.want, raw)
			}
			if _, ok := m["tool_call_id"]; ok != (c.msg.ToolCallID != "") {
				t.Errorf("tool_call_id present = %v in %s", ok, raw)
			}
			if _, ok := m["tool_calls"]; ok != (len(c.msg.ToolCalls) > 0) {
				t.Errorf("tool_calls present = %v in %s", ok, raw)
			}
		})
	}
}
