package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

func TestClampMaxTokensLeavesRoomForTheInput(t *testing.T) {
	// The reported case: a 131072-token window, 67073 tokens of history,
	// and a configured max_tokens of 64000. 67073+64000 is one token over,
	// and the server refuses the whole request.
	got := clampMaxTokens(64000, 131072, 67073)
	if got+67073+contextHeadroom > 131072 {
		t.Errorf("clamped max_tokens %d still overflows: %d+%d > %d", got, got, 67073, 131072)
	}
	if got >= 64000 {
		t.Errorf("clamped max_tokens = %d, expected it to be reduced", got)
	}
}

func TestClampMaxTokensLeavesAFittingRequestAlone(t *testing.T) {
	if got := clampMaxTokens(4096, 200000, 1000); got != 4096 {
		t.Errorf("clamped a request that already fits: %d, want 4096", got)
	}
	// No window figure means no opinion — never make a request smaller
	// than configured just because the model is unrecognised.
	if got := clampMaxTokens(4096, 0, 1000); got != 4096 {
		t.Errorf("clamped without a window figure: %d, want 4096", got)
	}
}

func TestClampMaxTokensStopsAtTheFloor(t *testing.T) {
	// A history that has eaten the window: an answer of a few tokens is
	// not an answer, so the floor holds and the overflow recovery takes
	// over instead.
	if got := clampMaxTokens(4096, 8192, 8000); got != minOutputTokens {
		t.Errorf("clamped to %d, want the floor %d", got, minOutputTokens)
	}
}

func TestIsContextOverflowRecognizesTheProviders(t *testing.T) {
	overflow := []string{
		`openai-compat endpoint returned 400: {"error":{"message":"litellm.ContextWindowExceededError: This model's maximum context length is 131072 tokens. However, you requested 64000 output tokens and your prompt contains at least 67073 input tokens"}}`,
		"prompt is too long: 210000 tokens > 200000 maximum",
		"ValidationException: input length and `max_tokens` exceed context limit",
		"Please reduce the length of the messages or completion",
	}
	for _, msg := range overflow {
		if !isContextOverflow(errors.New(msg)) {
			t.Errorf("not recognised as an overflow: %q", msg)
		}
	}
	other := []string{
		"openai-compat endpoint returned 401: invalid api key",
		"dial tcp 127.0.0.1:1234: connect: connection refused",
		"model not found",
	}
	for _, msg := range other {
		if isContextOverflow(errors.New(msg)) {
			t.Errorf("wrongly recognised as an overflow: %q", msg)
		}
	}
}

func TestFitHistoryDropsFromTheFrontAndKeepsTheEnd(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(strings.Repeat("a", 4000))}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("b", 4000))}},
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("the recent bit")}},
	}
	kept, dropped, err := fitHistory("", msgs, 1000)
	if err != nil {
		t.Fatalf("fitHistory: %v", err)
	}
	if dropped != 2 {
		t.Errorf("dropped %d messages, want 2", dropped)
	}
	if len(kept) != 1 || kept[0].Content[0].Text != "the recent bit" {
		t.Errorf("kept the wrong end: %+v", kept)
	}
}

// A trim must not open on a tool_result whose tool_use it just dropped:
// providers reject that pairing, so a rescue that produced one would have
// rescued nothing.
func TestFitHistoryDoesNotOpenOnAnOrphanedToolResult(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(strings.Repeat("a", 8000))}},
		{Role: provider.RoleAssistant, Content: []provider.Block{{
			Type: provider.BlockToolUse, ToolUseID: "t1", ToolName: "bash", ToolInput: json.RawMessage(`{}`),
		}}},
		{Role: provider.RoleUser, Content: []provider.Block{provider.ToolResultBlock("t1", "ok", false)}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("done")}},
	}
	kept, _, err := fitHistory("", msgs, 100)
	if err != nil {
		t.Fatalf("fitHistory: %v", err)
	}
	if startsWithToolResult(kept[0]) {
		t.Error("trimmed history begins with an orphaned tool_result")
	}
}

// /compact has to work on a conversation that is already too big — that
// is the only situation anyone types it in.
//
// It used to send the whole history to be summarized, so the command that
// exists to rescue an overflowing session was refused by that session with
// "compaction failed: ... maximum context length is N tokens". Auto-
// compaction failed the same way, without even saying so.
func TestCompactWorksOnAHistoryThatNoLongerFits(t *testing.T) {
	const limitChars = 4000
	var refusals, accepted int
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		json.NewDecoder(r.Body).Decode(&raw)
		encoded, _ := json.Marshal(raw["messages"])

		mu.Lock()
		tooBig := len(encoded) > limitChars
		if tooBig {
			refusals++
		} else {
			accepted++
		}
		mu.Unlock()

		if tooBig {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 1024 tokens."}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"a summary"}}]}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	// A window small enough that fitHistory has to throw most of the
	// conversation away to get under it.
	profile := config.Profile{Provider: "local", Model: "small-model", ContextWindow: defaultMaxTokens + contextHeadroom + 250}
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL}},
		Profiles:       map[string]config.Profile{"small": profile},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "small"}},
		DefaultProfile: "small",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"local": provider.NewOpenAICompat(srv.URL, ""),
	}, cfg)

	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := range 20 {
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Block{provider.TextBlock(fmt.Sprintf("%d %s", i, strings.Repeat("y", 400)))},
		})
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Block{provider.TextBlock("ok")},
		})
	}

	if err := loop.compactHistory(context.Background(), sid, loop.Providers["local"], profile, "", "", true); err != nil {
		t.Fatalf("compaction failed on an over-long history: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if refusals != 0 {
		t.Errorf("the summarization request was refused %d times; it should have been trimmed to fit", refusals)
	}
	if accepted == 0 {
		t.Error("no summarization request was made")
	}
}

// overflowThenSucceedServer refuses the first request the way a
// context-window overflow is actually reported, and answers every request
// after that. It records each request so the test can see that the retry
// carried a shorter history than the attempt that failed.
func overflowThenSucceedServer(t *testing.T) (*httptest.Server, func() []int) {
	t.Helper()
	var mu sync.Mutex
	var inputLens []int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)

		mu.Lock()
		n := len(inputLens)
		inputLens = append(inputLens, len(body.Messages))
		mu.Unlock()

		if n == 0 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 4096 output tokens and your prompt contains at least 9000 input tokens."}}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, c := range []string{
			`{"choices":[{"delta":{"content":"recovered"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))

	return srv, func() []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), inputLens...)
	}
}

// A turn refused for being too long must compact and retry, not end.
//
// Before this, the 400 went straight into the transcript and the turn was
// over — with the meter reading half full, because what overflowed was the
// history plus the output the request reserved room for. Every later
// message failed the same way, so the session was finished.
func TestTurnRecoversFromAContextOverflow(t *testing.T) {
	srv, requests := overflowThenSucceedServer(t)
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL},
		},
		Profiles: map[string]config.Profile{
			"small": {Provider: "local", Model: "small-model", ContextWindow: 8192},
		},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "small"}},
		DefaultProfile: "small",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"local": provider.NewOpenAICompat(srv.URL, ""),
	}, cfg)

	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	// Enough history that there is something to summarize.
	for i := range 6 {
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Block{provider.TextBlock(fmt.Sprintf("message %d: %s", i, strings.Repeat("x", 200)))},
		})
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Block{provider.TextBlock("ok")},
		})
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "carry on"); err != nil {
		t.Fatalf("SendMessage returned an error instead of recovering: %v", err)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var text strings.Builder
	sawRecovery, sawCompaction := false, false
	for _, ev := range all {
		switch ev.Type {
		case "message.part.delta":
			if s, ok := ev.Data["text"].(string); ok {
				text.WriteString(s)
			}
		case "error":
			if ev.Data["recovered"] == true {
				sawRecovery = true
			}
		case "compacted":
			sawCompaction = true
		}
	}
	if !sawRecovery {
		t.Error("no event told the user the conversation was being summarized to recover")
	}
	if !sawCompaction {
		t.Error("the history was never compacted")
	}
	if got := text.String(); !strings.Contains(got, "recovered") {
		t.Errorf("final text = %q, want the answer from after the retry", got)
	}

	reqs := requests()
	if len(reqs) < 3 {
		t.Fatalf("expected refuse + summarize + retry, got %d requests", len(reqs))
	}
	if reqs[len(reqs)-1] >= reqs[0] {
		t.Errorf("the retry carried %d messages, no shorter than the %d that were refused", reqs[len(reqs)-1], reqs[0])
	}
}

// A configured context_window has to reach every consumer of it, not just
// the one that sizes the request.
//
// Three things read "how much room is there": the meter under the prompt,
// the 80% auto-compaction trigger, and the cap on the next reply. They
// used to disagree — the first two guessed from the model name while only
// the third read the config — so setting context_window fixed the request
// and left the meter reporting a window that wasn't there.
func TestConfiguredContextWindowReachesTheMeter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":1000,"completion_tokens":100}}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	// A name the lookup table would guess 1,000,000 for, configured to the
	// 32k this server actually serves.
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL}},
		Profiles: map[string]config.Profile{
			"p": {Provider: "local", Model: "claude-opus-5", ContextWindow: 32768},
		},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "p"}},
		DefaultProfile: "p",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"local": provider.NewOpenAICompat(srv.URL, ""),
	}, cfg)

	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	all, _ := store.Events(sid, 0)
	var usage map[string]any
	for _, ev := range all {
		if ev.Type == "usage" && ev.Data["max_context"] != nil {
			usage = ev.Data
		}
	}
	if usage == nil {
		t.Fatal("no usage event carrying a window")
	}
	if got := usage["max_context"]; got != 32768 {
		t.Errorf("the meter reports a window of %v, not the configured 32768", got)
	}
	// 1100 of 32768 is 3.4%; of the guessed 1,000,000 it would read 0.1%.
	if got := usage["percent"].(float64); got < 3.3 || got > 3.5 {
		t.Errorf("percent = %v, want ~3.4 (1100 of 32768)", got)
	}
}
