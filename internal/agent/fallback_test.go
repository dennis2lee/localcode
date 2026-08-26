package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

// failingServer refuses with status for the first n requests and answers
// normally afterwards, recording what each request was.
func failingServer(t *testing.T, status int, body string, failures int) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []recordedRequest
	seen := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		system := ""
		for _, m := range req.Messages {
			if m["role"] == "system" {
				system, _ = m["content"].(string)
			}
		}
		mu.Lock()
		requests = append(requests, recordedRequest{model: req.Model, system: system})
		n := seen
		seen++
		mu.Unlock()

		if n < failures {
			http.Error(w, body, status)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answered\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	return srv, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), requests...)
	}
}

// newFallbackLoop wires one provider serving three model names, so a
// fallback is a different profile without needing three servers.
func newFallbackLoop(t *testing.T, url string) *Loop {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: url},
		},
		Profiles: map[string]config.Profile{
			// A hosted flagship first, a local open-weight model second:
			// the pair that makes the prompt-variant question real.
			"primary": {Provider: "local", Model: "claude-opus-5", Fallback: []string{"backup", "last"}},
			"backup":  {Provider: "local", Model: "qwen3-coder-30b"},
			"last":    {Provider: "local", Model: "claude-haiku-4-5"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "primary", Description: "the default"},
			"other":           {Profile: "last", Description: "somewhere to delegate"},
		},
		DefaultProfile: "primary",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": provider.NewOpenAICompat(url, "")}, cfg)
	return loop
}

func recovered(t *testing.T, loop *Loop, sid string) []string {
	t.Helper()
	all, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var out []string
	for _, ev := range all {
		if ev.Type == events.TypeError {
			text, _ := ev.Data["error"].(string)
			out = append(out, text)
		}
	}
	return out
}

// A rate limit is not the end of a turn when there is somewhere else to
// ask. This is the whole feature in one test.
func TestARateLimitedModelFallsBackToTheNextProfile(t *testing.T) {
	srv, recordedReqs := failingServer(t, http.StatusTooManyRequests, "rate limit exceeded", 1)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	reqs := recordedReqs()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want the failure and the fallback", len(reqs))
	}
	if reqs[0].model != "claude-opus-5" || reqs[1].model != "qwen3-coder-30b" {
		t.Errorf("models were %q then %q, want the primary then the first fallback", reqs[0].model, reqs[1].model)
	}
	if msgs := recovered(t, loop, sid); len(msgs) == 0 || !strings.Contains(msgs[0], "falling back to qwen3-coder-30b") {
		t.Errorf("the switch was not reported in the transcript: %v", msgs)
	}
}

// The point the whitepaper makes about fallback chains, and the reason
// this is not three lines: changing the model changes which prompt the
// model should be given. A local open-weight model that catches an
// overflow from a hosted flagship must get the prompt written for it, not
// the one written for the model that just failed.
func TestAFallbackGetsThePromptWrittenForItsOwnModel(t *testing.T) {
	srv, recordedReqs := failingServer(t, http.StatusServiceUnavailable, "service unavailable", 1)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	reqs := recordedReqs()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}
	if !strings.Contains(reqs[0].system, "1. Understand.") {
		t.Error("the primary did not get the full orchestration policy")
	}
	if strings.Contains(reqs[1].system, "1. Understand.") {
		t.Error("the local fallback was sent the policy written for the flagship")
	}
	if !strings.Contains(reqs[1].system, "Smart Agent is on") {
		t.Error("the fallback got no orchestration prompt at all")
	}
}

// Two failures walk two links. The chain is flat, so the second fallback
// comes from the primary's own list rather than from the first fallback's.
func TestTheChainIsWalkedInOrder(t *testing.T) {
	srv, recordedReqs := failingServer(t, http.StatusBadGateway, "bad gateway", 2)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	got := []string{}
	for _, r := range recordedReqs() {
		got = append(got, r.model)
	}
	want := []string{"claude-opus-5", "qwen3-coder-30b", "claude-haiku-4-5"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("models tried = %v, want %v", got, want)
	}
}

// The chain is finite. A provider having a bad hour must not turn one turn
// into an unbounded sweep of every model configured.
func TestAnExhaustedChainFailsTheTurn(t *testing.T) {
	srv, recordedReqs := failingServer(t, http.StatusServiceUnavailable, "service unavailable", 99)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err == nil {
		t.Fatal("the turn reported success with every model refusing")
	}
	if n := len(recordedReqs()); n != 3 {
		t.Errorf("made %d requests, want one per profile in the chain", n)
	}
}

// Off is off. A fallback is a visible change in who is answering, so it
// does not start happening to somebody who installed an update.
func TestWithSmartAgentOffThereIsNoFallback(t *testing.T) {
	srv, recordedReqs := failingServer(t, http.StatusTooManyRequests, "rate limit", 1)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err == nil {
		t.Fatal("the turn survived a rate limit with Smart Agent off")
	}
	if n := len(recordedReqs()); n != 1 {
		t.Errorf("made %d requests, want just the one that failed", n)
	}
}

// A conversation too long for the window is not an endpoint failure. It is
// already handled properly, by summarizing and retrying on the same model,
// and sending it to a smaller fallback would make it worse and throw away
// the cached prefix as well.
func TestAnOverflowIsNotAFallbackCase(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("openai-compat endpoint returned 400: the request exceeds the maximum context length"),
		fmt.Errorf("input is too long for requested model"),
	} {
		if worthFallingBackOver(err) {
			t.Errorf("%v was treated as an endpoint failure", err)
		}
	}
	for _, err := range []error{
		fmt.Errorf("openai-compat endpoint returned 429: rate limit exceeded"),
		fmt.Errorf("do request: dial tcp 127.0.0.1:1234: connect: connection refused"),
		fmt.Errorf("bedrock ConverseStream: ThrottlingException"),
		fmt.Errorf("openai-compat endpoint returned 503: service unavailable"),
	} {
		if !worthFallingBackOver(err) {
			t.Errorf("%v was not treated as an endpoint failure", err)
		}
	}
}
