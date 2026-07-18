package agent

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/events"
	"localcode/internal/hooks"
)

// --- user_prompt_submit ---

func TestUserPromptSubmitHookBlocksMessage(t *testing.T) {
	srv := &recordingServer{}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	loop, store := newUsageTestLoop(t, server.URL)
	loop.Config.Hooks = hooks.Config{hooks.EventUserPromptSubmit: {{Command: "echo 'no thanks' >&2; exit 2"}}}
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if srv.requestCount() != 0 {
		t.Errorf("expected no model request once user_prompt_submit blocked, got %d", srv.requestCount())
	}
	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawError bool
	for _, ev := range all {
		if ev.Type == events.TypeError {
			sawError = true
			if !strings.Contains(ev.Data["error"].(string), "no thanks") {
				t.Errorf("error = %v, want it to contain the hook's reason", ev.Data["error"])
			}
		}
	}
	if !sawError {
		t.Error("expected an error event when user_prompt_submit blocks")
	}
}

func TestUserPromptSubmitHookAllowsMessageThrough(t *testing.T) {
	srv := &recordingServer{}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	loop, store := newUsageTestLoop(t, server.URL)
	loop.Config.Hooks = hooks.Config{hooks.EventUserPromptSubmit: {{Command: "true"}}}
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if srv.requestCount() != 1 {
		t.Errorf("expected the model to be called once user_prompt_submit allows, got %d requests", srv.requestCount())
	}
}

// --- stop ---

func TestStopHookFiresWhenTurnCompletes(t *testing.T) {
	srv := &recordingServer{}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	dir := t.TempDir()
	marker := filepath.Join(dir, "stopped")
	loop, store := newUsageTestLoop(t, server.URL)
	loop.Config.Hooks = hooks.Config{hooks.EventStop: {{Command: "echo done > " + marker}}}
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("expected the stop hook to have run once the turn completed")
	}
}

// --- /compact ---

func TestCompactCommandWithHistory(t *testing.T) {
	srv := &recordingServer{summary: "MANUAL_SUMMARY"}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	loop, store := newUsageTestLoop(t, server.URL)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "first message"); err != nil {
		t.Fatalf("SendMessage (first): %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/compact"); err != nil {
		t.Fatalf("SendMessage (/compact): %v", err)
	}

	if srv.requestCount() != 2 {
		t.Fatalf("expected 2 requests (first turn + compaction), got %d", srv.requestCount())
	}
	if !isCompactionRequest(mustUnmarshal(t, srv.body(1))) {
		t.Error("expected the second request to be the compaction request")
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawCompacted bool
	for _, ev := range all {
		if ev.Type == events.TypeCompacted {
			sawCompacted = true
			if ev.Data["manual"] != true {
				t.Errorf("manual = %v, want true for the /compact command", ev.Data["manual"])
			}
		}
	}
	if !sawCompacted {
		t.Error("expected a compacted event")
	}
	text := lastMessagePartEnd(t, store, sid)
	if !strings.Contains(text, "압축") {
		t.Errorf("text = %q, want a confirmation mentioning compaction", text)
	}
}

func TestCompactCommandWithCustomInstructions(t *testing.T) {
	var capturedBody string
	srv := &recordingServer{summary: "MANUAL_SUMMARY"}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	loop, store := newUsageTestLoop(t, server.URL)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "first message"); err != nil {
		t.Fatalf("SendMessage (first): %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/compact keep only the file paths"); err != nil {
		t.Fatalf("SendMessage (/compact): %v", err)
	}

	capturedBody = srv.body(srv.requestCount() - 1)
	if !strings.Contains(capturedBody, "keep only the file paths") {
		t.Errorf("compaction request = %s, want it to contain the custom instructions", capturedBody)
	}
}

func TestCompactCommandNoHistoryErrors(t *testing.T) {
	srv := &recordingServer{}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	loop, store := newUsageTestLoop(t, server.URL)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/compact"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawError bool
	for _, ev := range all {
		if ev.Type == events.TypeError {
			sawError = true
		}
	}
	if !sawError {
		t.Error("expected an error event when compacting with no history")
	}
	if srv.requestCount() != 0 {
		t.Errorf("expected no model request with no history to compact, got %d", srv.requestCount())
	}
}

// --- /cost ---

func TestCostCommandNoUsageYet(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/cost"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	text := lastMessagePartEnd(t, store, sid)
	if !strings.Contains(text, "사용량이 없습니다") {
		t.Errorf("text = %q, want a \"no usage yet\" message", text)
	}
}

func TestCostCommandBreaksDownByModel(t *testing.T) {
	srv := &recordingServer{}
	srv.response = func(body map[string]any) (string, int, int) { return "ok", 100, 20 }
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	// newCustomCommandTestLoop wires "general-purpose"->balanced-model and
	// "review"->strong-model, both against the same mock server.
	loop, store := newCustomCommandTestLoop(t, server.URL, nil)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage (general-purpose): %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "review", "hi again"); err != nil {
		t.Fatalf("SendMessage (review): %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/cost"); err != nil {
		t.Fatalf("SendMessage (/cost): %v", err)
	}

	text := lastMessagePartEnd(t, store, sid)
	if !strings.Contains(text, "balanced-model") || !strings.Contains(text, "strong-model") {
		t.Errorf("text = %q, want it to break down both models used", text)
	}
	if !strings.Contains(text, "입력 100") {
		t.Errorf("text = %q, want per-model input token counts", text)
	}
	if !strings.Contains(text, "전체 합계") {
		t.Errorf("text = %q, want a grand total line", text)
	}
}

func TestCostCommandIncludesCompactionCallUsage(t *testing.T) {
	srv := &recordingServer{summary: "SUMMARY"}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	loop, store := newUsageTestLoop(t, server.URL)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// One normal turn (100 in / 20 out per the mock) + one manual
	// compaction (500 in / 50 out): /cost must count both calls.
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/compact"); err != nil {
		t.Fatalf("SendMessage (/compact): %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/cost"); err != nil {
		t.Fatalf("SendMessage (/cost): %v", err)
	}

	text := lastMessagePartEnd(t, store, sid)
	if !strings.Contains(text, "입력 600") || !strings.Contains(text, "출력 70") {
		t.Errorf("text = %q, want totals including the compaction call (600 in / 70 out)", text)
	}
	if !strings.Contains(text, "호출 2회") {
		t.Errorf("text = %q, want the compaction call counted as a call", text)
	}
}

func TestClearSessionStateRemovesCumulativeUsage(t *testing.T) {
	srv := &recordingServer{}
	srv.response = func(body map[string]any) (string, int, int) { return "ok", 100, 20 }
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

	loop.ClearSessionState(sid)

	if _, ok := loop.getUsage(sid); ok {
		t.Error("expected getUsage to report nothing after ClearSessionState")
	}
	loop.mu.Lock()
	_, ok := loop.cumulativeUsage[sid]
	loop.mu.Unlock()
	if ok {
		t.Error("expected cumulativeUsage to be cleared after ClearSessionState")
	}
}
