package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// TestLoopEndToEnd exercises the full path a real run takes: agent loop ->
// provider (OpenAI-compat) -> streamed tool_use -> tool execution -> second
// provider turn -> final text answer -> session event log. It stands in for
// exercising the TUI itself, which needs a real tty.
func TestLoopEndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		hasToolResult := false
		for _, m := range body.Messages {
			if m["role"] == "tool" {
				hasToolResult = true
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		if !hasToolResult {
			// First turn: ask to call the glob tool.
			chunks := []string{
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"glob","arguments":""}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pattern\""}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"*.go\"}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			}
			for _, c := range chunks {
				fmt.Fprintf(w, "data: %s\n\n", c)
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		// Second turn: answer using the tool result.
		chunks := []string{
			`{"choices":[{"delta":{"content":"Found "}}]}`,
			`{"choices":[{"delta":{"content":"the files."}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	const sessionID = "test-session"
	if _, err := store.CreateSession(sessionID, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	registry := tools.NewRegistry(nil) // glob requires no permission, so a nil handler is fine here
	registry.Register(tools.Glob{})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: server.URL},
		},
		Profiles: map[string]config.Profile{
			"balanced": {Provider: "local", Model: "test-model"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "balanced"},
		},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid test config: %v", err)
	}

	providers := map[string]provider.Provider{
		"local": provider.NewOpenAICompat(server.URL, ""),
	}

	loop := New(store, registry, providers, cfg)

	eventCh, unsubscribe, err := store.Subscribe(sessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var seen []events.Type
	done := make(chan struct{})
	go func() {
		for ev := range eventCh {
			seen = append(seen, ev.Type)
		}
		close(done)
	}()

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "find go files"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	allEvents, err := store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	var finalText strings.Builder
	sawToolStart, sawToolEnd := false, false
	for _, ev := range allEvents {
		switch ev.Type {
		case events.TypeMessagePartDelta:
			if text, ok := ev.Data["text"].(string); ok {
				finalText.WriteString(text)
			}
		case events.TypeToolStart:
			sawToolStart = true
			if ev.Data["name"] != "glob" {
				t.Errorf("expected tool_start for glob, got %v", ev.Data["name"])
			}
		case events.TypeToolEnd:
			sawToolEnd = true
			if isErr, _ := ev.Data["is_error"].(bool); isErr {
				t.Errorf("tool_end reported error: %v", ev.Data["content"])
			}
		}
	}

	if !sawToolStart || !sawToolEnd {
		t.Errorf("expected tool_start and tool_end events, got tool_start=%v tool_end=%v", sawToolStart, sawToolEnd)
	}
	if got := finalText.String(); got != "Found the files." {
		t.Errorf("final text = %q, want %q", got, "Found the files.")
	}

	unsubscribe()
	<-done
	if len(seen) == 0 {
		t.Error("subscriber saw no events at all")
	}
}
