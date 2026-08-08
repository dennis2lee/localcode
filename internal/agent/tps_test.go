package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
)

// paced streams `deltas` one-token chunks, `gap` apart, after waiting
// `prefill` — a stand-in for a model that thinks for a while and then
// generates at a steady rate. usageTokens is what it claims in its usage
// report, which is what tokens-per-second is actually computed from.
type paced struct {
	prefill     time.Duration
	deltas      int
	gap         time.Duration
	usageTokens int
	toolCall    string // if set, the reply is a bash call with this command
}

func (p paced) writeTo(w http.ResponseWriter) {
	flusher := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	flusher.Flush()

	time.Sleep(p.prefill)

	if p.toolCall != "" {
		args := mustMarshal(map[string]string{"command": p.toolCall})
		fmt.Fprintf(w, "data: %s\n\n", mustMarshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"tool_calls": []map[string]any{{
				"index": 0, "id": "call_1",
				"function": map[string]any{"name": "bash", "arguments": string(args)},
			}}}}},
		}))
		flusher.Flush()
	}
	for i := 0; i < p.deltas; i++ {
		fmt.Fprintf(w, "data: %s\n\n", mustMarshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"content": "x"}}},
		}))
		flusher.Flush()
		time.Sleep(p.gap)
	}
	finish := "stop"
	if p.toolCall != "" {
		finish = "tool_calls"
	}
	fmt.Fprintf(w, "data: %s\n\n", mustMarshal(map[string]any{
		"choices": []map[string]any{{"delta": map[string]any{}, "finish_reason": finish}},
	}))
	fmt.Fprintf(w, "data: %s\n\n", mustMarshal(map[string]any{
		"choices": []map[string]any{},
		"usage":   map[string]int{"prompt_tokens": 100, "completion_tokens": p.usageTokens},
	}))
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// TestTPSMeasuresGenerationNotWaiting is the fix for a rate that read far
// below what the model was doing. The clock used to start when the request
// was sent, so the wait before the first token — prefill, and on a shared
// local server the queue in front of you — was divided into the output as
// though the model had been generating the whole time.
func TestTPSMeasuresGenerationNotWaiting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paced{prefill: 400 * time.Millisecond, deltas: 10, gap: 20 * time.Millisecond, usageTokens: 200}.writeTo(w)
	}))
	defer srv.Close()

	loop, store := newUsageTestLoop(t, srv.URL)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	tps := usageTPS(t, store, "s1")

	// ~200ms of generation for 200 tokens is about 1000/s. Measured over
	// the whole 600ms request it would be about 333/s. The threshold sits
	// between the two, well clear of both.
	if tps < 600 {
		t.Errorf("tps %.1f: the wait before the first token is still being counted as generation time", tps)
	}
}

// TestTPSCoversTheWholeTurn guards the second half of the complaint: the
// number jumping between turns' worth of work. A turn that uses a tool is
// several model calls, and the last is routinely a handful of tokens —
// reported alone, it made a fast model look slow for no visible reason.
func TestTPSCoversTheWholeTurn(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			// A long, fast generation that asks for a tool: ~1000 tok/s.
			paced{deltas: 10, gap: 20 * time.Millisecond, usageTokens: 200, toolCall: "true"}.writeTo(w)
			return
		}
		// The wrap-up: a few tokens, taking just as long. ~50 tok/s.
		paced{deltas: 10, gap: 20 * time.Millisecond, usageTokens: 10}.writeTo(w)
	}))
	defer srv.Close()

	loop, store := newUsageTestLoop(t, srv.URL)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if call < 2 {
		t.Fatalf("expected the tool call to force a second model call, got %d", call)
	}

	tps := usageTPS(t, store, "s1")

	// 210 tokens over ~400ms is around 525/s. The final call on its own is
	// around 50/s, which is what used to be displayed.
	if tps < 200 {
		t.Errorf("tps %.1f: still reporting the last model call rather than the turn", tps)
	}
}

// TestTurnRateResetsBetweenTurns: the figure describes the turn in front of
// you. Without the reset it would converge on a lifetime average and stop
// responding to how the current turn is actually going.
func TestTurnRateResetsBetweenTurns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paced{deltas: 5, gap: 20 * time.Millisecond, usageTokens: 50}.writeTo(w)
	}))
	defer srv.Close()

	loop, store := newUsageTestLoop(t, srv.URL)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "hi"); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	}

	loop.mu.Lock()
	r := loop.turnRate["s1"]
	loop.mu.Unlock()
	if r.tokens != 50 {
		t.Errorf("turn rate carries %d tokens after a second turn of 50; it should hold only this turn's", r.tokens)
	}
}

// TestToolStartCarriesItsArguments: the transcript line for a running tool
// says which tool AND what it was asked to do. The arguments arrive one
// fragment at a time, so the event is emitted once they are complete.
func TestToolStartCarriesItsArguments(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			paced{deltas: 1, usageTokens: 5, toolCall: "echo hello"}.writeTo(w)
			return
		}
		paced{deltas: 1, usageTokens: 5}.writeTo(w)
	}))
	defer srv.Close()

	loop, store := newUsageTestLoop(t, srv.URL)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	all, err := store.Events("s1", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var start events.Event
	startIdx, endIdx := -1, -1
	for i, ev := range all {
		switch ev.Type {
		case events.TypeToolStart:
			start, startIdx = ev, i
		case events.TypeMessagePartEnd:
			if endIdx == -1 {
				endIdx = i
			}
		}
	}
	if startIdx == -1 {
		t.Fatal("no tool.start event")
	}
	var input map[string]string
	if err := json.Unmarshal([]byte(start.Data["input"].(string)), &input); err != nil {
		t.Fatalf("tool.start input is not the tool's arguments: %v", err)
	}
	if input["command"] != "echo hello" {
		t.Errorf("tool.start input = %v, want the command the model asked for", input)
	}
	// rehydrateHistory pairs a tool.start with the message.part.end that
	// closes the same model message, so the order matters as much as the
	// payload does.
	if startIdx > endIdx {
		t.Errorf("tool.start at %d comes after message.part.end at %d; history rehydration pairs them the other way", startIdx, endIdx)
	}
}

func usageTPS(t *testing.T, store *session.Store, sessionID string) float64 {
	t.Helper()
	ev := lastUsageEvent(t, store, sessionID)
	tps, ok := ev.Data["tps"].(float64)
	if !ok {
		t.Fatalf("usage event has no tps: %v", ev.Data)
	}
	return tps
}

// TestSingleChunkRepliesReportNoRate: a rate needs two points to measure
// between. A provider that delivers a short reply in one chunk gave a span
// of microseconds, and dividing the output by it produced six-figure
// tokens-per-second — a number worse than none, because it looks measured.
func TestSingleChunkRepliesReportNoRate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paced{deltas: 1, usageTokens: 30}.writeTo(w)
	}))
	defer srv.Close()

	loop, store := newUsageTestLoop(t, srv.URL)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if tps := usageTPS(t, store, "s1"); tps != 0 {
		t.Errorf("tps %.1f from a single chunk: there was no interval to measure across", tps)
	}
}

// TestInjectedMessageReachesTheModelMidTurn: what someone types during a
// long turn is handed to the model at its next step, not after the whole
// job finishes. It rides out with the tool results rather than as a user
// message of its own — two user messages back to back is a shape Bedrock
// rejects outright.
func TestInjectedMessageReachesTheModelMidTurn(t *testing.T) {
	call := 0
	var secondBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 2 {
			body, _ := io.ReadAll(r.Body)
			secondBody = string(body)
			r.Body = io.NopCloser(strings.NewReader(secondBody))
		}
		if call == 1 {
			paced{deltas: 1, usageTokens: 5, toolCall: "true"}.writeTo(w)
			return
		}
		paced{deltas: 1, usageTokens: 5}.writeTo(w)
	}))
	defer srv.Close()

	loop, store := newUsageTestLoop(t, srv.URL)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	pending := []string{"actually, skip the tests"}
	loop.PendingInput = func(string) (string, bool) {
		if len(pending) == 0 {
			return "", false
		}
		text := pending[0]
		pending = pending[1:]
		return text, true
	}

	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if !strings.Contains(secondBody, "actually, skip the tests") {
		t.Errorf("the model's next request did not carry what the user typed mid-turn:\n%s", secondBody)
	}

	// And it is in the transcript, marked, so a restart can put it back in
	// the same place rather than as a turn of its own.
	all, err := store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ev := range all {
		if ev.Type == events.TypeUserMessage && ev.Data["text"] == "actually, skip the tests" {
			found = true
			if ev.Data["injected"] != true {
				t.Error("the mid-turn message is not marked as injected; rehydration would replay it as its own turn")
			}
		}
	}
	if !found {
		t.Error("the mid-turn message never reached the transcript")
	}
}

// TestRehydrationPutsAnInjectedMessageBackWhereTheModelSawIt: after a
// daemon restart the reconstructed history has to match what was actually
// sent. An injected message belongs at the end of the tool_result message
// it travelled with; replaying it as a separate user message would leave
// two user messages in a row.
func TestRehydrationPutsAnInjectedMessageBackWhereTheModelSawIt(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			paced{deltas: 1, usageTokens: 5, toolCall: "true"}.writeTo(w)
			return
		}
		paced{deltas: 1, usageTokens: 5}.writeTo(w)
	}))
	defer srv.Close()

	loop, store := newUsageTestLoop(t, srv.URL)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sent := false
	loop.PendingInput = func(string) (string, bool) {
		if sent {
			return "", false
		}
		sent = true
		return "one more thing", true
	}
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	live := loop.history("s1")
	all, err := store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt := rehydrateHistory(all)

	if len(rebuilt) != len(live) {
		t.Fatalf("rebuilt history has %d messages, the live one had %d:\nrebuilt %+v\nlive    %+v", len(rebuilt), len(live), rebuilt, live)
	}
	for i := range live {
		if rebuilt[i].Role != live[i].Role {
			t.Errorf("message %d: rebuilt role %q, live %q", i, rebuilt[i].Role, live[i].Role)
		}
		if len(rebuilt[i].Content) != len(live[i].Content) {
			t.Errorf("message %d: rebuilt %d blocks, live %d", i, len(rebuilt[i].Content), len(live[i].Content))
		}
	}
	// No two user messages in a row, which is the shape Bedrock refuses.
	for i := 1; i < len(rebuilt); i++ {
		if rebuilt[i].Role == provider.RoleUser && rebuilt[i-1].Role == provider.RoleUser {
			t.Fatalf("messages %d and %d are both from the user; Bedrock rejects that outright", i-1, i)
		}
	}
}
