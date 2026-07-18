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

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// newUsageTestLoop builds a Loop with a single "balanced" profile on
// "claude-sonnet-5" (a known 200000-token family per internal/modelinfo,
// so percent math in tests is easy to reason about) pointed at modelURL.
func newUsageTestLoop(t *testing.T, modelURL string) (*Loop, *session.Store) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	registry := tools.NewRegistry(nil)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelURL},
		},
		Profiles: map[string]config.Profile{
			"balanced": {Provider: "local", Model: "claude-sonnet-5"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "balanced"},
		},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	providers := map[string]provider.Provider{"local": provider.NewOpenAICompat(modelURL, "")}
	loop := New(store, registry, providers, cfg)
	return loop, store
}

// recordingServer is an OpenAI-compat mock that records every request body
// it sees (in order) and, for any request whose last message contains
// "Summarize our conversation" (the compaction prompt), answers with a
// canned summary instead of the usual scripted reply.
type recordingServer struct {
	mu       sync.Mutex
	bodies   []string
	summary  string
	response func(body map[string]any) (text string, inputTokens, outputTokens int)
}

func (s *recordingServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		s.mu.Lock()
		s.bodies = append(s.bodies, string(raw))
		s.mu.Unlock()

		var body map[string]any
		_ = json.Unmarshal(raw, &body)

		text, inputTokens, outputTokens := "done.", 100, 20
		if isCompactionRequest(body) {
			text, inputTokens, outputTokens = s.summary, 500, 50
		} else if s.response != nil {
			text, inputTokens, outputTokens = s.response(body)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", mustMarshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"content": text}}},
		}))
		fmt.Fprintf(w, "data: %s\n\n", mustMarshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{}, "finish_reason": "stop"}},
		}))
		fmt.Fprintf(w, "data: %s\n\n", mustMarshal(map[string]any{
			"choices": []map[string]any{},
			"usage":   map[string]int{"prompt_tokens": inputTokens, "completion_tokens": outputTokens},
		}))
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}
}

func (s *recordingServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *recordingServer) body(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[i]
}

func isCompactionRequest(body map[string]any) bool {
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		return false
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	content, _ := last["content"].(string)
	return strings.Contains(content, "Summarize our conversation")
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func lastUsageEvent(t *testing.T, store *session.Store, sessionID string) events.Event {
	t.Helper()
	all, err := store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var found events.Event
	var ok bool
	for _, ev := range all {
		if ev.Type == events.TypeUsage {
			found, ok = ev, true
		}
	}
	if !ok {
		t.Fatal("expected at least one usage event")
	}
	return found
}

func TestUsageEventEmittedWithPercentAndTPS(t *testing.T) {
	srv := &recordingServer{}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	loop, store := newUsageTestLoop(t, server.URL)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	ev := lastUsageEvent(t, store, sid)
	if ev.Data["input_tokens"].(int) != 100 {
		t.Errorf("input_tokens = %v, want 100", ev.Data["input_tokens"])
	}
	if ev.Data["output_tokens"].(int) != 20 {
		t.Errorf("output_tokens = %v, want 20", ev.Data["output_tokens"])
	}
	if ev.Data["max_context"].(int) != 200000 {
		t.Errorf("max_context = %v, want 200000 (claude-sonnet-5)", ev.Data["max_context"])
	}
	wantPercent := float64(120) / 200000 * 100
	if got := ev.Data["percent"].(float64); got < wantPercent-0.001 || got > wantPercent+0.001 {
		t.Errorf("percent = %v, want ~%v", got, wantPercent)
	}
	if ev.Data["show_tps"] != true {
		t.Errorf("show_tps = %v, want true (default)", ev.Data["show_tps"])
	}
	if ev.Data["model"] != "claude-sonnet-5" {
		t.Errorf("model = %v, want %q", ev.Data["model"], "claude-sonnet-5")
	}
}

func TestAutoCompactTriggersAboveThresholdAndResetsHistory(t *testing.T) {
	srv := &recordingServer{summary: "SUMMARY_TEXT"}
	// First turn's usage pushes well past the 80% threshold: (170000 +
	// 10000) / 200000 = 90%.
	srv.response = func(body map[string]any) (string, int, int) { return "done.", 170000, 10000 }
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	loop, store := newUsageTestLoop(t, server.URL)
	loop.SetAutoCompactEnabled(true)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "first message"); err != nil {
		t.Fatalf("SendMessage (first): %v", err)
	}
	if srv.requestCount() != 1 {
		t.Fatalf("expected 1 request after the first turn, got %d", srv.requestCount())
	}

	// Second turn should trigger compaction (an extra request) before the
	// real turn's request.
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "second message"); err != nil {
		t.Fatalf("SendMessage (second): %v", err)
	}

	if srv.requestCount() != 3 {
		t.Fatalf("expected 3 requests total (1 + compaction + real turn), got %d", srv.requestCount())
	}
	if !isCompactionRequest(mustUnmarshal(t, srv.body(1))) {
		t.Errorf("request 2 should have been the compaction request, body: %s", srv.body(1))
	}
	realTurnBody := mustUnmarshal(t, srv.body(2))
	msgs, _ := realTurnBody["messages"].([]any)
	// system prompt + summary-as-user + the new turn = 3, not the original
	// history's system + first-message + assistant-reply + new turn.
	if len(msgs) != 3 {
		t.Fatalf("expected the post-compaction turn to send exactly 3 messages (system + summary + new turn), got %d: %+v", len(msgs), msgs)
	}
	summaryMsg, _ := msgs[1].(map[string]any)
	if !strings.Contains(summaryMsg["content"].(string), "SUMMARY_TEXT") {
		t.Errorf("expected the second message post-compaction to contain the summary, got %+v", summaryMsg)
	}
	if strings.Contains(fmt.Sprintf("%v", realTurnBody["messages"]), "first message") {
		t.Errorf("expected the original pre-compaction message to be gone from history, got %+v", realTurnBody["messages"])
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawCompacted bool
	for _, ev := range all {
		if ev.Type == events.TypeCompacted {
			sawCompacted = true
			if ev.Data["summary_length"].(int) != len("SUMMARY_TEXT") {
				t.Errorf("summary_length = %v, want %d", ev.Data["summary_length"], len("SUMMARY_TEXT"))
			}
		}
	}
	if !sawCompacted {
		t.Error("expected a compacted event")
	}
}

func TestAutoCompactDisabledNeverTriggers(t *testing.T) {
	srv := &recordingServer{summary: "SUMMARY_TEXT"}
	srv.response = func(body map[string]any) (string, int, int) { return "done.", 170000, 10000 }
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	loop, store := newUsageTestLoop(t, server.URL)
	loop.SetAutoCompactEnabled(false) // explicitly off
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	loop.SendMessage(context.Background(), sid, "general-purpose", "first message")
	loop.SendMessage(context.Background(), sid, "general-purpose", "second message")

	if srv.requestCount() != 2 {
		t.Errorf("expected exactly 2 requests (no compaction call) with AutoCompactEnabled=false, got %d", srv.requestCount())
	}
}

func TestAutoCompactBelowThresholdNeverTriggers(t *testing.T) {
	srv := &recordingServer{summary: "SUMMARY_TEXT"}
	// Well under 80%: (1000 + 100) / 200000 = 0.55%.
	srv.response = func(body map[string]any) (string, int, int) { return "done.", 1000, 100 }
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	loop, store := newUsageTestLoop(t, server.URL)
	loop.SetAutoCompactEnabled(true)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	loop.SendMessage(context.Background(), sid, "general-purpose", "first message")
	loop.SendMessage(context.Background(), sid, "general-purpose", "second message")

	if srv.requestCount() != 2 {
		t.Errorf("expected exactly 2 requests (no compaction call) below the threshold, got %d", srv.requestCount())
	}
}

func mustUnmarshal(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}
