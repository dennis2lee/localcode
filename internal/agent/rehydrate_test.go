package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

func ev(typ events.Type, data map[string]any) events.Event {
	return events.Event{Type: typ, Data: data}
}

func TestRehydrateHistorySimpleTextTurn(t *testing.T) {
	history := rehydrateHistory([]events.Event{
		ev(events.TypeUserMessage, map[string]any{"text": "hi"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "hello there"}),
	})

	if len(history) != 2 {
		t.Fatalf("history = %+v, want 2 messages", history)
	}
	if history[0].Role != provider.RoleUser || history[0].Content[0].Text != "hi" {
		t.Errorf("history[0] = %+v, want user \"hi\"", history[0])
	}
	if history[1].Role != provider.RoleAssistant || history[1].Content[0].Text != "hello there" {
		t.Errorf("history[1] = %+v, want assistant \"hello there\"", history[1])
	}
}

func TestRehydrateHistoryUsesModelTextOverDisplayText(t *testing.T) {
	history := rehydrateHistory([]events.Event{
		ev(events.TypeUserMessage, map[string]any{"text": "/skill review", "model_text": "Follow the review skill..."}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "ok"}),
	})

	if history[0].Content[0].Text != "Follow the review skill..." {
		t.Errorf("history[0] text = %q, want the model_text override", history[0].Content[0].Text)
	}
}

func TestRehydrateHistoryOneToolCallRoundTrip(t *testing.T) {
	history := rehydrateHistory([]events.Event{
		ev(events.TypeUserMessage, map[string]any{"text": "list go files"}),
		ev(events.TypeToolStart, map[string]any{"tool_use_id": "call_1", "name": "glob"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": ""}),
		ev(events.TypeToolEnd, map[string]any{"tool_use_id": "call_1", "content": "main.go", "is_error": false, "input": `{"pattern":"*.go"}`}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "Found main.go."}),
	})

	if len(history) != 4 {
		t.Fatalf("history = %+v, want 4 messages (user, assistant+tool_use, user+tool_result, assistant)", history)
	}
	if history[0].Role != provider.RoleUser {
		t.Errorf("history[0].Role = %q, want user", history[0].Role)
	}

	assistant1 := history[1]
	if assistant1.Role != provider.RoleAssistant || len(assistant1.Content) != 1 {
		t.Fatalf("history[1] = %+v, want one tool_use block (empty text is omitted)", assistant1)
	}
	toolUse := assistant1.Content[0]
	if toolUse.Type != provider.BlockToolUse || toolUse.ToolUseID != "call_1" || toolUse.ToolName != "glob" {
		t.Errorf("tool_use block = %+v, want call_1/glob", toolUse)
	}
	if string(toolUse.ToolInput) != `{"pattern":"*.go"}` {
		t.Errorf("tool_use input = %s, want the persisted input JSON", toolUse.ToolInput)
	}

	toolResultMsg := history[2]
	if toolResultMsg.Role != provider.RoleUser || len(toolResultMsg.Content) != 1 {
		t.Fatalf("history[2] = %+v, want one tool_result block", toolResultMsg)
	}
	if toolResultMsg.Content[0].Type != provider.BlockToolResult || toolResultMsg.Content[0].ToolResultContent != "main.go" {
		t.Errorf("tool_result block = %+v, want content \"main.go\"", toolResultMsg.Content[0])
	}

	final := history[3]
	if final.Role != provider.RoleAssistant || final.Content[0].Text != "Found main.go." {
		t.Errorf("history[3] = %+v, want final assistant text", final)
	}
}

func TestRehydrateHistoryMultiIterationToolLoop(t *testing.T) {
	history := rehydrateHistory([]events.Event{
		ev(events.TypeUserMessage, map[string]any{"text": "do two things"}),
		// iteration 1: one tool call
		ev(events.TypeToolStart, map[string]any{"tool_use_id": "call_1", "name": "glob"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": ""}),
		ev(events.TypeToolEnd, map[string]any{"tool_use_id": "call_1", "content": "a.go", "input": `{}`}),
		// iteration 2: another tool call
		ev(events.TypeToolStart, map[string]any{"tool_use_id": "call_2", "name": "read_file"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": ""}),
		ev(events.TypeToolEnd, map[string]any{"tool_use_id": "call_2", "content": "package main", "input": `{"path":"a.go"}`}),
		// iteration 3: final answer, no tool use
		ev(events.TypeMessagePartEnd, map[string]any{"text": "done"}),
	})

	// user, (assistant+tool, user+result) x2, final assistant = 6
	if len(history) != 6 {
		t.Fatalf("history = %+v, want 6 messages", history)
	}
	if history[1].Content[0].ToolUseID != "call_1" {
		t.Errorf("history[1] tool_use = %+v, want call_1", history[1].Content[0])
	}
	if history[3].Content[0].ToolUseID != "call_2" {
		t.Errorf("history[3] tool_use = %+v, want call_2", history[3].Content[0])
	}
	if history[5].Content[0].Text != "done" {
		t.Errorf("history[5] = %+v, want final text \"done\"", history[5])
	}
}

func TestRehydrateHistoryCompactionResetsToSummary(t *testing.T) {
	history := rehydrateHistory([]events.Event{
		ev(events.TypeUserMessage, map[string]any{"text": "turn 1"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "reply 1"}),
		ev(events.TypeCompacted, map[string]any{"summary": "SUMMARY_TEXT", "manual": true}),
		ev(events.TypeUserMessage, map[string]any{"text": "turn 2"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "reply 2"}),
	})

	if len(history) != 3 {
		t.Fatalf("history = %+v, want 3 messages (summary, turn 2 user, turn 2 assistant)", history)
	}
	if !strings.Contains(history[0].Content[0].Text, "SUMMARY_TEXT") {
		t.Errorf("history[0] = %+v, want it to contain the compaction summary", history[0])
	}
	if history[1].Content[0].Text != "turn 2" {
		t.Errorf("history[1] = %+v, want the post-compaction user turn", history[1])
	}
}

func TestRehydrateHistoryOldCompactedEventWithoutSummaryIsIgnored(t *testing.T) {
	// Pre-v0.12 logs only recorded summary_length, not the summary text
	// itself — rehydration can't reconstruct that exact state, so it
	// should just leave the fuller pre-compaction history intact rather
	// than silently discarding it with nothing to replace it.
	history := rehydrateHistory([]events.Event{
		ev(events.TypeUserMessage, map[string]any{"text": "turn 1"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "reply 1"}),
		ev(events.TypeCompacted, map[string]any{"summary_length": 500, "manual": false}),
		ev(events.TypeUserMessage, map[string]any{"text": "turn 2"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "reply 2"}),
	})

	if len(history) != 4 {
		t.Fatalf("history = %+v, want all 4 messages preserved (no summary to reset to)", history)
	}
	if history[0].Content[0].Text != "turn 1" {
		t.Errorf("history[0] = %+v, want the original turn 1 preserved", history[0])
	}
}

func TestRehydrateHistorySkipsLocalCommandAndItsReply(t *testing.T) {
	// /compact, /usage, /config, /memory, etc. append a "local" user
	// message (never sent to the model) followed by a message.part.end
	// that's just that command's own display-only answer — not something
	// the model ever said. Both must be excluded from rehydrated history,
	// or the next turn would show the model a fake assistant reply it
	// never produced.
	history := rehydrateHistory([]events.Event{
		ev(events.TypeUserMessage, map[string]any{"text": "turn 1"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "reply 1"}),
		ev(events.TypeUserMessage, map[string]any{"text": "/compact", "local": true}),
		ev(events.TypeCompacted, map[string]any{"summary": "SUMMARY"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "Conversation compacted."}),
		ev(events.TypeUserMessage, map[string]any{"text": "turn 2"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "reply 2"}),
	})

	// Compaction resets to [summary], then turn 2's user+assistant — the
	// /compact command itself and its "compacted" confirmation must
	// never appear.
	if len(history) != 3 {
		t.Fatalf("history = %+v, want exactly 3 messages (summary, turn 2 user, turn 2 assistant)", history)
	}
	if !strings.Contains(history[0].Content[0].Text, "SUMMARY") {
		t.Errorf("history[0] = %+v, want the compaction summary", history[0])
	}
	if history[1].Content[0].Text != "turn 2" {
		t.Errorf("history[1] = %+v, want turn 2's user message, not the /compact command", history[1])
	}
	if history[2].Content[0].Text != "reply 2" {
		t.Errorf("history[2] = %+v, want turn 2's reply, not the compaction confirmation", history[2])
	}
	for _, m := range history {
		if strings.Contains(m.Content[0].Text, "compacted") || m.Content[0].Text == "/compact" {
			t.Errorf("history contains the local /compact command or its confirmation: %+v", m)
		}
	}
}

func TestRehydrateHistorySkipsLocalCommandWithNoReply(t *testing.T) {
	// handleCompactCommand only emits a message.part.end on success —
	// a failed local command (e.g. "/compact" with no history) leaves
	// skipNextReply set with nothing to consume it. The *next* real turn
	// must not have its own reply swallowed as a result.
	history := rehydrateHistory([]events.Event{
		ev(events.TypeUserMessage, map[string]any{"text": "/compact", "local": true}),
		// (no message.part.end — the command errored before ever emitting one)
		ev(events.TypeUserMessage, map[string]any{"text": "turn 1"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "reply 1"}),
	})

	if len(history) != 2 {
		t.Fatalf("history = %+v, want 2 messages (turn 1 user + assistant), not swallowed by the earlier failed local command", history)
	}
	if history[0].Content[0].Text != "turn 1" || history[1].Content[0].Text != "reply 1" {
		t.Errorf("history = %+v, want turn 1's user/assistant pair intact", history)
	}
}

func TestRehydrateUsageSumsPerModel(t *testing.T) {
	latest, haveUsage, cum := rehydrateUsage([]events.Event{
		ev(events.TypeUsage, map[string]any{"input_tokens": 100, "output_tokens": 20, "max_context": 200000, "tps": 5.0, "model": "m1"}),
		ev(events.TypeUsage, map[string]any{"input_tokens": 150, "output_tokens": 30, "max_context": 200000, "tps": 6.0, "model": "m1"}),
	})

	if !haveUsage {
		t.Fatal("expected haveUsage = true")
	}
	if latest.InputTokens != 150 || latest.OutputTokens != 30 {
		t.Errorf("latest = %+v, want the most recent event's snapshot", latest)
	}
	mt := cum["m1"]
	if mt.InputTokens != 250 || mt.OutputTokens != 50 || mt.Calls != 2 {
		t.Errorf("cum[m1] = %+v, want summed totals across both calls", mt)
	}
}

func TestRehydrateUsageHandlesDiskRestoredFloat64Numbers(t *testing.T) {
	// Simulates data as it comes back from json.Unmarshal (all numbers
	// are float64), not the live in-process int values.
	latest, haveUsage, cum := rehydrateUsage([]events.Event{
		ev(events.TypeUsage, map[string]any{"input_tokens": float64(100), "output_tokens": float64(20), "max_context": float64(200000), "tps": float64(5), "model": "m1"}),
	})
	if !haveUsage || latest.InputTokens != 100 || latest.MaxContext != 200000 {
		t.Errorf("latest = %+v haveUsage=%v, want ints recovered from float64 data", latest, haveUsage)
	}
	if cum["m1"].InputTokens != 100 {
		t.Errorf("cum[m1] = %+v, want InputTokens=100", cum["m1"])
	}
}

func TestRehydrateUsageCompactionClearsSnapshotButKeepsCumulative(t *testing.T) {
	latest, haveUsage, cum := rehydrateUsage([]events.Event{
		ev(events.TypeUsage, map[string]any{"input_tokens": 170000, "output_tokens": 10000, "max_context": 200000, "model": "m1"}),
		ev(events.TypeCompacted, map[string]any{"summary": "x", "model": "m1", "input_tokens": 500, "output_tokens": 50}),
	})

	if haveUsage {
		t.Error("expected haveUsage = false after a compaction event (matches live clearUsage behavior)")
	}
	_ = latest
	mt := cum["m1"]
	if mt.InputTokens != 170500 || mt.OutputTokens != 10050 || mt.Calls != 2 {
		t.Errorf("cum[m1] = %+v, want the pre-compaction call plus the compaction call's own usage summed in", mt)
	}
}

// TestRehydrateAllRestoresContextAndCostAcrossRestart is the end-to-end
// check: run a real turn (with a tool call) plus a manual compaction
// against a Loop backed by a persisted session store, then simulate a
// daemon restart (fresh Store loaded from the same directory, fresh Loop,
// RehydrateAll), and confirm the next turn's request carries the
// rehydrated history and /usage still reports the pre-restart totals.
func TestRehydrateAllRestoresContextAndCostAcrossRestart(t *testing.T) {
	srv := &recordingServer{summary: "OLD_SUMMARY"}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()

	dir := t.TempDir()
	const sid = "s1"

	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: server.URL},
		},
		Profiles: map[string]config.Profile{
			"balanced": {Provider: "local", Model: "claude-sonnet-5"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "balanced"},
		},
		DefaultProfile: "balanced",
	}
	registry := tools.NewRegistry(nil)
	loop := New(store, registry, map[string]provider.Provider{"local": provider.NewOpenAICompat(server.URL, "")}, cfg)

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/compact"); err != nil {
		t.Fatalf("SendMessage /compact: %v", err)
	}

	// --- simulate a daemon restart ---
	restoredStore, warnings, err := session.LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	restoredLoop := New(restoredStore, registry, map[string]provider.Provider{"local": provider.NewOpenAICompat(server.URL, "")}, cfg)
	restoredLoop.RehydrateAll()

	// The next turn's request must include the rehydrated (post-compaction)
	// history — i.e. the summary text — even though this Loop instance
	// never ran the original conversation itself.
	if err := restoredLoop.SendMessage(context.Background(), sid, "general-purpose", "continue"); err != nil {
		t.Fatalf("SendMessage after restore: %v", err)
	}
	lastReq := mustUnmarshal(t, srv.body(srv.requestCount()-1))
	msgs, _ := lastReq["messages"].([]any)
	var sawSummary bool
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if content, _ := mm["content"].(string); strings.Contains(content, "OLD_SUMMARY") {
			sawSummary = true
		}
	}
	if !sawSummary {
		t.Errorf("request after restart = %+v, want it to include the rehydrated summary from before the restart", msgs)
	}

	// /usage must also carry over the pre-restart totals (the first turn's
	// call plus the compaction call), not reset to zero.
	if err := restoredLoop.SendMessage(context.Background(), sid, "general-purpose", "/usage"); err != nil {
		t.Fatalf("SendMessage /usage: %v", err)
	}
	text := lastMessagePartEnd(t, restoredStore, sid)
	if strings.Contains(text, "No usage yet") {
		t.Errorf("/usage text = %q, want rehydrated totals, not \"no usage yet\"", text)
	}
	if !strings.Contains(text, "calls") {
		t.Errorf("/usage text = %q, want a call count reflecting rehydrated usage", text)
	}
}

func TestDataIntAndDataFloatHelpers(t *testing.T) {
	if got := dataInt(map[string]any{"n": 5}, "n"); got != 5 {
		t.Errorf("dataInt(int) = %d, want 5", got)
	}
	if got := dataInt(map[string]any{"n": float64(5)}, "n"); got != 5 {
		t.Errorf("dataInt(float64) = %d, want 5", got)
	}
	if got := dataInt(nil, "n"); got != 0 {
		t.Errorf("dataInt(nil map) = %d, want 0", got)
	}
	if got := dataFloat(map[string]any{"n": 5}, "n"); got != 5 {
		t.Errorf("dataFloat(int) = %v, want 5", got)
	}
}
