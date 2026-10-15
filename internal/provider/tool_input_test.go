package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// Only a JSON object goes back as a tool call's arguments. Anything else
// a model wrote goes back as {}: every endpoint refused it, and so every
// request after it, since each carries the same history.
func TestSendableToolInput(t *testing.T) {
	for raw, want := range map[string]string{
		`{"a": 1}`:                       `{"a": 1}`,
		` {"a": 1} `:                     ` {"a": 1} `,
		`{}`:                             `{}`,
		``:                               `{}`,
		`null`:                           `{}`,
		`[1]`:                            `{}`,
		`"text"`:                         `{}`,
		`{"options": ["a", "b)\""]`:      `{}`,
		`{"a": 1}}`:                      `{}`,
		`{"command": "rm -rf /tmp/build`: `{}`,
	} {
		if got := string(SendableToolInput(json.RawMessage(raw))); got != want {
			t.Errorf("SendableToolInput(%q) = %q, want %q", raw, got, want)
		}
	}
}

func brokenCallHistory() []Message {
	return []Message{
		{Role: RoleUser, Content: []Block{TextBlock("ask me")}},
		{Role: RoleAssistant, Content: []Block{{
			Type: BlockToolUse, ToolUseID: "call_1", ToolName: "ask_user",
			ToolInput: json.RawMessage(`{"question": "how?", "options": ["a", "b)\""]`),
		}}},
		{Role: RoleUser, Content: []Block{ToolResultBlock("call_1", "not run", true)}},
	}
}

// Anthropic's encoder carries the arguments as a json.RawMessage, and one
// that is not JSON failed the whole request at json.Marshal.
func TestAnthropicSendsBrokenToolArgumentsAsAnEmptyObject(t *testing.T) {
	data, err := json.Marshal(toAnthropicMessages(brokenCallHistory()))
	if err != nil {
		t.Fatalf("the request cannot be built: %v", err)
	}
	if !strings.Contains(string(data), `"input":{}`) {
		t.Errorf("the call did not go as {}: %s", data)
	}
}

func TestOpenAICompatSendsBrokenToolArgumentsAsAnEmptyObject(t *testing.T) {
	for _, m := range toOpenAIMessages("", brokenCallHistory()) {
		for _, c := range m.ToolCalls {
			if c.Function.Arguments != "{}" {
				t.Errorf("the call went with arguments %q, want {}", c.Function.Arguments)
			}
		}
	}
}

// An endpoint's error reply is kept up to 16 KB, cut on a character
// boundary, and says it was cut. A cause written after a long preamble is
// still in it: context overflow, a refused image and the fallback decisions
// are all read from this text.
func TestErrorBodyIsCut(t *testing.T) {
	if got := errorBody(strings.NewReader("short reply")); got != "short reply" {
		t.Errorf("a short reply became %q", got)
	}
	exact := strings.Repeat("x", maxErrorBody)
	if got := errorBody(strings.NewReader(exact)); got != exact {
		t.Errorf("a reply of exactly %d bytes was changed", maxErrorBody)
	}
	late := strings.Repeat("preamble line\n", 300) + "maximum context length is 32768 tokens"
	if got := errorBody(strings.NewReader(late)); !strings.Contains(got, "maximum context length") {
		t.Errorf("a cause after %d bytes of preamble was cut away", len(late)-38)
	}
	// Three-byte characters after a 14-byte head, so the byte at the cut
	// is inside one.
	long := "cause first. x" + strings.Repeat("가", 8000)
	if long[maxErrorBody]&0xC0 != 0x80 {
		t.Fatal("the test reply does not put the cut inside a character")
	}
	got := errorBody(strings.NewReader(long))
	if !strings.HasPrefix(got, "cause first. x") || !strings.HasSuffix(got, "[cut at 16 KB]") {
		t.Errorf("the cut reply does not keep the head and say it was cut: %q...%q", got[:20], got[len(got)-20:])
	}
	if !utf8.ValidString(got) {
		t.Error("the reply was cut inside a character")
	}
	if len(got) > maxErrorBody+len(" [cut at 16 KB]") {
		t.Errorf("the cut reply is %d bytes", len(got))
	}
}

// LiteLLM's refusals print the whole request back after the cause. The
// error keeps the cause and leaves the echo out, so the conversation is
// not printed into the transcript and nothing in it is read as a reason
// to retry.
func TestErrorBodyLeavesOutTheRequestLiteLLMEchoes(t *testing.T) {
	body := `{"error":{"message":"litellm.BadRequestError: AnthropicException - Failed to parse tool call arguments for tool 'ask_user'. Error: Expecting ',' delimiter: line 1 column 1018 (char 1017).\nReceived Messages=[{'role': 'user', 'content': 'the quota is fine, no server error here'}]"}}`
	got := errorBody(strings.NewReader(body))
	if !strings.Contains(got, "Failed to parse tool call arguments") || !strings.Contains(got, "(char 1017).") {
		t.Errorf("the cause was lost: %s", got)
	}
	if strings.Contains(got, "Received Messages") || strings.Contains(got, "quota") {
		t.Errorf("the echoed request was kept: %s", got)
	}
	if !strings.HasSuffix(got, "(char 1017). [the request it echoed back is left out]") {
		t.Errorf("the cut does not end cleanly and say what was left out: %s", got)
	}
}

// Both HTTP providers read an error reply the same way. Each is driven
// against an endpoint that echoes the request back.
func TestAnEndpointThatEchoesTheRequestIsNotPrintedWhole(t *testing.T) {
	echo := strings.Repeat("every message of the conversation, quota and all. ", 3000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"litellm.BadRequestError: Failed to parse tool call arguments.\nReceived Messages=` + echo + `"}}`))
	}))
	defer srv.Close()
	anthropic := NewAnthropicDirect("key")
	anthropic.BaseURL = srv.URL
	for name, p := range map[string]Provider{
		"openai-compat": NewOpenAICompat(srv.URL, ""),
		"anthropic":     anthropic,
	} {
		_, err := p.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}}})
		if err == nil {
			t.Fatalf("%s: a 400 was not an error", name)
		}
		msg := err.Error()
		if !strings.Contains(msg, "returned 400") || !strings.Contains(msg, "Failed to parse tool call arguments") {
			t.Errorf("%s: the cause was lost: %.200s", name, msg)
		}
		if strings.Contains(msg, "quota") || len(msg) > 1<<10 {
			t.Errorf("%s: the echoed request reached the error (%d bytes)", name, len(msg))
		}
	}
}
