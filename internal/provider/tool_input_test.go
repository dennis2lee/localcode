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

// An endpoint's error reply is cut at 2 KB, on a character boundary, and
// says it was cut.
func TestErrorBodyIsCut(t *testing.T) {
	if got := errorBody(strings.NewReader("short reply")); got != "short reply" {
		t.Errorf("a short reply became %q", got)
	}
	exact := strings.Repeat("x", maxErrorBody)
	if got := errorBody(strings.NewReader(exact)); got != exact {
		t.Errorf("a reply of exactly %d bytes was changed", maxErrorBody)
	}
	// Three-byte characters, so byte 2048 falls inside one.
	long := "cause first. " + strings.Repeat("가", 4000)
	got := errorBody(strings.NewReader(long))
	if !strings.HasPrefix(got, "cause first. ") || !strings.HasSuffix(got, "[cut at 2 KB]") {
		t.Errorf("the cut reply does not keep the head and say it was cut: %q...%q", got[:20], got[len(got)-20:])
	}
	if !utf8.ValidString(got) {
		t.Error("the reply was cut inside a character")
	}
	if len(got) > maxErrorBody+len(" [cut at 2 KB]") {
		t.Errorf("the cut reply is %d bytes", len(got))
	}
}

// LiteLLM's refusals end with every message of the request. The error that
// reaches the transcript carries the head of the reply, not the
// conversation printed back.
func TestAnEndpointThatEchoesTheRequestIsNotPrintedWhole(t *testing.T) {
	echo := strings.Repeat("every message of the conversation. ", 3000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"litellm.BadRequestError: Failed to parse tool call arguments. Received Messages=` + echo + `"}}`))
	}))
	defer srv.Close()
	_, err := NewOpenAICompat(srv.URL, "").Chat(context.Background(), ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("a 400 was not an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "returned 400") || !strings.Contains(msg, "Failed to parse tool call arguments") {
		t.Errorf("the cause was lost: %.200s", msg)
	}
	if len(msg) > 3<<10 {
		t.Errorf("the error is %d bytes", len(msg))
	}
}
