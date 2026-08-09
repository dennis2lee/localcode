package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// A turn that fails must not leave the session unable to run another one.
//
// The shape this guards: the user message is appended to history before
// the request, so a provider error returns with history ending in a user
// message. The user retries, a second user message is appended, and the
// request carries two in a row — which Bedrock's Converse API rejects,
// on that retry and on every retry after it, for the life of the session
// (the log persists, so a restart does not help either). One dropped
// connection ended the session.
//
// Driven through the real Loop and a real HTTP provider so the assertion
// is about the bytes that actually go out, not about a helper.
func TestRetryAfterProviderErrorSendsAValidConversation(t *testing.T) {
	var mu sync.Mutex
	var roleSequences [][]string
	failNext := true

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		var roles []string
		for _, m := range body.Messages {
			roles = append(roles, fmt.Sprint(m["role"]))
		}
		mu.Lock()
		roleSequences = append(roleSequences, roles)
		shouldFail := failNext
		failNext = false
		mu.Unlock()

		if shouldFail {
			// The transient failure: a throttle, a dropped upstream, a
			// proxy hiccup. Nothing exotic.
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"throttled"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer server.Close()

	loop, sessionID := testLoop(t, server.URL)

	// First attempt fails.
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "first try"); err == nil {
		t.Fatal("expected the first turn to fail")
	}
	// The user retries, as anyone would.
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "retry"); err != nil {
		t.Fatalf("retry after a transient failure: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(roleSequences) != 2 {
		t.Fatalf("got %d requests, want 2", len(roleSequences))
	}
	assertAlternating(t, "retry request", roleSequences[1])
}

// The same guarantee for the other way history goes wrong: cancelling a
// turn before the model has produced anything. The provider's stream
// goroutine returns on ctx.Done without a terminal event, which reads as
// a clean finish — and an assistant message with no content is rejected
// by name on the next request.
func TestTurnCancelledBeforeFirstTokenLeavesAUsableSession(t *testing.T) {
	var mu sync.Mutex
	var roleSequences [][]string
	first := true

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		var roles []string
		for _, m := range body.Messages {
			roles = append(roles, fmt.Sprint(m["role"]))
		}
		mu.Lock()
		roleSequences = append(roleSequences, roles)
		isFirst := first
		first = false
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if isFirst {
			// Headers sent, then nothing — the request is cancelled
			// while the model is still thinking.
			flusher.Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	loop, sessionID := testLoop(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Give the request time to reach the server, then stop it the
		// way Esc does.
		for {
			mu.Lock()
			started := len(roleSequences) > 0
			mu.Unlock()
			if started {
				cancel()
				return
			}
		}
	}()
	loop.SendMessage(ctx, sessionID, "general-purpose", "long one") // error or not, both are fine

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "next"); err != nil {
		t.Fatalf("turn after a cancel: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(roleSequences) < 2 {
		t.Fatalf("got %d requests, want at least 2", len(roleSequences))
	}
	assertAlternating(t, "request after cancel", roleSequences[len(roleSequences)-1])
}

// assertAlternating checks the one property every provider requires and
// Bedrock enforces: no two messages in a row from the same role.
func assertAlternating(t *testing.T, what string, roles []string) {
	t.Helper()
	if len(roles) == 0 {
		t.Fatalf("%s carried no messages", what)
	}
	for i := 1; i < len(roles); i++ {
		if roles[i] == roles[i-1] {
			t.Errorf("%s has two %q messages in a row (position %d): %v", what, roles[i], i, roles)
		}
	}
}

func testLoop(t *testing.T, serverURL string) (*Loop, string) {
	t.Helper()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	const sessionID = "s1"
	if _, err := store.CreateSession(sessionID, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: serverURL}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "test-model"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid test config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"local": provider.NewOpenAICompat(serverURL, ""),
	}, cfg)
	return loop, sessionID
}
