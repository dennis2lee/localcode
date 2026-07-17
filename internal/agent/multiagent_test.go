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
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// recordedRequest captures just what these tests need to assert on from an
// OpenAI-compat chat/completions call.
type recordedRequest struct {
	model    string
	system   string
	toolsLen int
	toolset  map[string]bool
}

// multiAgentMockServer scripts a two-agent conversation:
//   - "strong-model" (the "build" agent): first turn asks to call the Task
//     tool delegating to "explore"; second turn (once it has the tool
//     result) answers with a final text that echoes the sub-agent's answer.
//   - "cheap-model" (the "explore" agent): answers directly with a fixed
//     text, no tool calls.
//
// It records every request body so the test can assert on per-agent
// system prompt and tool scoping, not just the final answer.
func multiAgentMockServer(t *testing.T) (*httptest.Server, *[]recordedRequest, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var requests []recordedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
			Tools    []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		system := ""
		hasToolResult := false
		for _, m := range body.Messages {
			if m["role"] == "system" {
				if s, ok := m["content"].(string); ok {
					system = s
				}
			}
			if m["role"] == "tool" {
				hasToolResult = true
			}
		}
		toolset := map[string]bool{}
		for _, tl := range body.Tools {
			if fn, ok := tl["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					toolset[name] = true
				}
			}
		}

		mu.Lock()
		requests = append(requests, recordedRequest{model: body.Model, system: system, toolsLen: len(body.Tools), toolset: toolset})
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		var chunks []string
		switch {
		case body.Model == "strong-model" && !hasToolResult:
			args := `{\"agent\":\"explore\",\"prompt\":\"find TODOs\"}`
			chunks = []string{
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Task","arguments":""}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"` + args + `"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			}
		case body.Model == "strong-model" && hasToolResult:
			chunks = []string{
				`{"choices":[{"delta":{"content":"build agent used: "}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			}
		case body.Model == "cheap-model":
			chunks = []string{
				`{"choices":[{"delta":{"content":"found 3 TODOs"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			}
		default:
			t.Fatalf("unexpected model in request: %q", body.Model)
		}

		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))

	return srv, &requests, &mu
}

func newMultiAgentLoop(t *testing.T, modelURL string) *Loop {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	registry := tools.NewRegistry(nil)
	registry.Register(tools.ReadFile{})
	registry.Register(tools.Glob{})
	registry.Register(tools.Grep{})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelURL},
		},
		Profiles: map[string]config.Profile{
			"strong": {Provider: "local", Model: "strong-model"},
			"cheap":  {Provider: "local", Model: "cheap-model"},
		},
		Agents: map[string]config.AgentConfig{
			"build": {
				Profile:     "strong",
				Description: "Implements features.",
				Prompt:      "You are the build agent.",
			},
			"explore": {
				Profile:     "cheap",
				Description: "Fast read-only search.",
				Prompt:      "You are the explore agent.",
				Tools:       []string{"read_file", "glob", "grep"},
			},
		},
		DefaultProfile: "strong",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	providers := map[string]provider.Provider{"local": provider.NewOpenAICompat(modelURL, "")}
	loop := New(store, registry, providers, cfg)

	tasks := NewTaskManager(context.Background(), loop, 5)
	registry.Register(NewTaskTool(tasks, cfg.Agents))

	return loop
}

// TestMultiAgentDelegation drives the "build" agent through a full turn
// that delegates to the "explore" agent via the Task tool and incorporates
// its answer — proving agent-specific models, prompts, and tool scoping
// all actually take effect end to end, not just that the config parses.
func TestMultiAgentDelegation(t *testing.T) {
	srv, requestsPtr, mu := multiAgentMockServer(t)
	defer srv.Close()

	loop := newMultiAgentLoop(t, srv.URL)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "build", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "build", "add a feature and check for leftover TODOs"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	all, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var finalText strings.Builder
	for _, ev := range all {
		if ev.Type == "message.part.delta" {
			if text, ok := ev.Data["text"].(string); ok {
				finalText.WriteString(text)
			}
		}
	}
	if got, want := finalText.String(), "build agent used: "; got != want {
		t.Errorf("final text = %q, want %q", got, want)
	}

	mu.Lock()
	requests := append([]recordedRequest(nil), (*requestsPtr)...)
	mu.Unlock()

	if len(requests) != 3 {
		t.Fatalf("expected 3 model calls (build turn 1, explore, build turn 2), got %d", len(requests))
	}

	buildReq1, exploreReq, buildReq2 := requests[0], requests[1], requests[2]

	if buildReq1.model != "strong-model" || buildReq2.model != "strong-model" {
		t.Errorf("expected the build agent's calls to use strong-model, got %q and %q", buildReq1.model, buildReq2.model)
	}
	if exploreReq.model != "cheap-model" {
		t.Errorf("expected the delegated explore call to use cheap-model, got %q", exploreReq.model)
	}

	if !strings.Contains(buildReq1.system, "build agent") {
		t.Errorf("build request system prompt = %q, want it to mention the build agent", buildReq1.system)
	}
	if !strings.Contains(exploreReq.system, "explore agent") {
		t.Errorf("explore request system prompt = %q, want it to mention the explore agent", exploreReq.system)
	}

	// explore is tool-scoped to read_file/glob/grep only — it should never
	// see Task or the write-capable tools this test didn't even register.
	if exploreReq.toolset["Task"] {
		t.Error("explore agent should not see the Task tool (not in its allowed tools)")
	}
	if !exploreReq.toolset["read_file"] || !exploreReq.toolset["grep"] {
		t.Errorf("explore agent missing its allowed tools: %+v", exploreReq.toolset)
	}

	// build has no Tools restriction configured, so it should see
	// everything including Task.
	if !buildReq1.toolset["Task"] {
		t.Error("build agent should see the Task tool (unrestricted)")
	}
}

// TestTaskToolDepthGuard confirms an agent configured to delegate to
// itself doesn't recurse forever — it stops at maxTaskDepth.
func TestTaskToolDepthGuard(t *testing.T) {
	var calls int
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()

		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		hasToolResult := false
		for _, m := range body.Messages {
			if m["role"] == "tool" {
				hasToolResult = true
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		var chunks []string
		if hasToolResult {
			// This turn already delegated once (successfully or refused by
			// the depth guard) — stop here instead of asking to delegate
			// again, or the guard blocking *deeper* recursion would never
			// stop *same-depth* re-delegation within one turn either.
			chunks = []string{
				`{"choices":[{"delta":{"content":"done"}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			}
		} else {
			args := `{\"agent\":\"loopy\",\"prompt\":\"go deeper\"}`
			chunks = []string{
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Task","arguments":""}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"` + args + `"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			}
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	registry := tools.NewRegistry(nil)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL}},
		Profiles:  map[string]config.Profile{"p": {Provider: "local", Model: "m"}},
		Agents: map[string]config.AgentConfig{
			"loopy": {Profile: "p", Description: "delegates to itself"},
		},
		DefaultProfile: "p",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	providers := map[string]provider.Provider{"local": provider.NewOpenAICompat(srv.URL, "")}
	loop := New(store, registry, providers, cfg)
	tasks := NewTaskManager(context.Background(), loop, 10)
	registry.Register(NewTaskTool(tasks, cfg.Agents))

	if _, err := store.CreateSession("s1", "", "loopy", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), "s1", "loopy", "start"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	mu.Lock()
	got := calls
	mu.Unlock()

	// Levels 0..maxTaskDepth (inclusive) each make 2 model calls: one
	// asking to delegate, one producing the final "done" answer after
	// getting a tool_result back (a real delegation, or the guard's
	// refusal at the deepest level). If the guard failed to stop
	// recursion this would run until the test times out instead of
	// settling on a fixed count.
	want := (maxTaskDepth + 1) * 2
	if got != want {
		t.Errorf("model was called %d times, want exactly %d (depth guard should stop recursion)", got, want)
	}
}
