package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/hooks"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// Release verification for how a request is sized, seen from the only
// place it can be seen honestly: the max_tokens the request carries when
// it reaches the server.

// sizingLoop is a loop on a window small enough that a few hundred tokens
// of input move the reply cap, so a sizing mistake shows up in max_tokens
// rather than being absorbed by a large window.
func sizingLoop(t *testing.T, modelURL string, window int) (*Loop, *session.Store) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelURL},
		},
		Profiles: map[string]config.Profile{
			"tight": {Provider: "local", Model: "m", ContextWindow: window},
		},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "tight"}},
		DefaultProfile: "tight",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"local": provider.NewOpenAICompat(modelURL, ""),
	}, cfg)
	return loop, store
}

// maxTokensOf reads the reply cap out of a captured request body.
func maxTokensOf(t *testing.T, body string) int {
	t.Helper()
	var req struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req.MaxTokens
}

// The reply is priced from the provider's own count of it, never from its
// characters. A Korean reply measured by characters comes out at a third
// of what the provider counted, and the next request then asks for output
// the window does not have.
func TestTheNextRequestPricesTheReplyFromItsCount(t *testing.T) {
	const window = 6000
	reply := strings.Repeat("가", 1500) // 1500 tokens by the provider, 1129 by characters
	srv := &recordingServer{}
	calls := 0
	srv.response = func(map[string]any) (string, int, int) {
		calls++
		if calls == 1 {
			return reply, 100, 1500
		}
		return "done.", 100, 20
	}
	server := httptest.NewServer(srv.handler(t))
	defer server.Close()
	loop, store := sizingLoop(t, server.URL, window)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, text := range []string{"tell me", "again"} {
		if err := loop.SendMessage(context.Background(), sid, "general-purpose", text); err != nil {
			t.Fatalf("SendMessage %q: %v", text, err)
		}
	}
	if srv.requestCount() != 2 {
		t.Fatalf("got %d requests, want 2", srv.requestCount())
	}

	// What the second request may ask for: the window, less the count
	// for the first request and its reply, less the second message,
	// less the margin. The second message is the only thing estimated.
	// It is the last user message: the second reply came after it.
	var second provider.Message
	for _, m := range loop.history(sid) {
		if m.Role == provider.RoleUser {
			second = m
		}
	}
	if second.Role != provider.RoleUser {
		t.Fatal("no user message in the history")
	}
	added := estimateTokens("", []provider.Message{second})
	want := clampMaxTokens(defaultMaxTokens, window, 100+1500+added)
	got := maxTokensOf(t, srv.body(1))
	if got < want-2 || got > want+2 {
		t.Errorf("the second request asks for %d output tokens, want %d: the reply is priced at %d by its characters and at 1500 by the provider", got, want, estimateTokens("", []provider.Message{{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(reply)}}}))
	}
}

// Text a pre_model hook injects is part of what the provider reads, and
// it arrives after the request was sized. It has to count, or a large
// injection pushes the request over the window the sizing thought it fit.
func TestHookTextIsCountedBeforeTheRequestIsClamped(t *testing.T) {
	const window = 6000
	injected := strings.Repeat("x", 6000) // about 1500 tokens
	run := func(withHook bool) int {
		srv := &recordingServer{}
		server := httptest.NewServer(srv.handler(t))
		defer server.Close()
		loop, store := sizingLoop(t, server.URL, window)
		if withHook {
			loop.Config.Hooks = hooks.Config{
				hooks.EventPreModel: {{Command: `echo '{"context":"` + injected + `"}'`}},
			}
		}
		const sid = "s1"
		if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hi"); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		if !withHook {
			return maxTokensOf(t, srv.body(0))
		}
		if !strings.Contains(srv.body(0), injected[:64]) {
			t.Fatal("precondition: the hook's text did not reach the request")
		}
		return maxTokensOf(t, srv.body(0))
	}
	plain := run(false)
	hooked := run(true)
	if plain <= minOutputTokens {
		t.Fatalf("precondition: the plain request is already at the floor (%d), so nothing here can be seen", plain)
	}
	if hooked > plain-1400 {
		t.Errorf("with 1500 tokens of hook text the request asks for %d output tokens, and %d without it: the injection was not counted", hooked, plain)
	}
}
