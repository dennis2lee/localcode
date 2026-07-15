package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/client"
	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// mockModelServer scripts a two-turn OpenAI-compat conversation: first turn
// asks to run write_file against writePath (so the test also exercises the
// permission broker over HTTP), second turn answers with plain text.
func mockModelServer(t *testing.T, writePath string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		var chunks []string
		if !hasToolResult {
			escapedPath := strings.ReplaceAll(writePath, `\`, `\\`)
			args := `{\"path\":\"` + escapedPath + `\",\"content\":\"hi\"}`
			chunks = []string{
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"write_file","arguments":""}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"` + args + `"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			}
		} else {
			chunks = []string{
				`{"choices":[{"delta":{"content":"done."}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			}
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

func newTestDaemon(t *testing.T, modelURL string) *Daemon {
	t.Helper()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	broker := agent.NewPermissionBroker(store)
	registry := tools.NewRegistry(broker.Func())
	registry.Register(tools.WriteFile{})
	registry.Register(tools.Glob{})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelURL},
		},
		Profiles: map[string]config.Profile{
			"balanced": {Provider: "local", Model: "test-model"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "balanced"},
		},
		DefaultProfile:     "balanced",
		MaxConcurrentTasks: 2,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	providers := map[string]provider.Provider{"local": provider.NewOpenAICompat(modelURL, "")}
	loop := agent.New(store, registry, providers, cfg)
	tasks := agent.NewTaskManager(context.Background(), loop, cfg.MaxConcurrentTasks)

	return New(loop, broker, tasks, nil)
}

// TestDaemonEndToEnd drives the daemon purely over HTTP via internal/client,
// the same way the TUI (or a Web UI) would: create a session, subscribe to
// its SSE stream, send a message that triggers a permission-gated tool
// call, approve it over HTTP, and confirm the final answer arrives.
func TestDaemonEndToEnd(t *testing.T) {
	writePath := filepath.Join(t.TempDir(), "out.txt")
	model := mockModelServer(t, writePath)
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// A dedicated, cancellable context for the long-lived SSE connection:
	// httptest.Server.Close() waits for all connections to end, so this
	// must be cancelled (closing the connection) before the deferred
	// server Close() runs. Deferring it here, after the server's own
	// defer, makes it run first (LIFO).
	evCtx, cancelEvents := context.WithCancel(ctx)
	defer cancelEvents()

	evCh, err := c.SubscribeEvents(evCtx, sess.ID, 0)
	if err != nil {
		t.Fatalf("SubscribeEvents: %v", err)
	}

	var permissionID string
	done := make(chan struct{})
	var sawToolStart, sawToolEnd bool
	var finalText strings.Builder

	go func() {
		defer close(done)
		for ev := range evCh {
			switch ev.Type {
			case events.TypePermissionRequest:
				permissionID, _ = ev.Data["id"].(string)
				if err := c.ResolvePermission(ctx, sess.ID, permissionID, true); err != nil {
					t.Errorf("ResolvePermission: %v", err)
				}
			case events.TypeToolStart:
				sawToolStart = true
			case events.TypeToolEnd:
				sawToolEnd = true
				if isErr, _ := ev.Data["is_error"].(bool); isErr {
					t.Errorf("tool_end is_error: %v", ev.Data["content"])
				}
			case events.TypeMessagePartDelta:
				if text, ok := ev.Data["text"].(string); ok {
					finalText.WriteString(text)
				}
				if finalText.String() == "done." {
					return
				}
			}
		}
	}()

	if err := c.SendMessage(ctx, sess.ID, "write a file"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for turn to complete")
	}

	if !sawToolStart || !sawToolEnd {
		t.Errorf("expected tool_start/tool_end, got start=%v end=%v", sawToolStart, sawToolEnd)
	}
	if permissionID == "" {
		t.Error("expected a permission.request event")
	}
	if got := finalText.String(); got != "done." {
		t.Errorf("final text = %q, want %q", got, "done.")
	}
}

// TestDaemonListSessions confirms visible (top-level) sessions show up
// for resuming, newest first, while background task sessions (visible:
// false) are excluded.
func TestDaemonListSessions(t *testing.T) {
	textOnly := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
	}))
	defer textOnly.Close()

	d := newTestDaemon(t, textOnly.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	ctx := context.Background()

	first, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // ensure distinct CreatedAt for ordering
	second, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := c.SpawnTask(ctx, second.ID, "general-purpose", "background work"); err != nil {
		t.Fatalf("SpawnTask: %v", err)
	}

	list, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 visible sessions (task session excluded), got %d: %+v", len(list), list)
	}
	if list[0].ID != second.ID || list[1].ID != first.ID {
		t.Errorf("expected newest-first order [%s, %s], got [%s, %s]", second.ID, first.ID, list[0].ID, list[1].ID)
	}
}

// TestDaemonBackgroundTask exercises the Task Manager over HTTP: spawn a
// background task from a parent session and poll until it completes.
func TestDaemonBackgroundTask(t *testing.T) {
	// A model server that never asks for tools, so the task finishes in
	// one turn.
	textOnly := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer textOnly.Close()

	d := newTestDaemon(t, textOnly.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	ctx := context.Background()

	parent, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	taskID, err := c.SpawnTask(ctx, parent.ID, "general-purpose", "do something in the background")
	if err != nil {
		t.Fatalf("SpawnTask: %v", err)
	}
	if taskID == "" {
		t.Fatal("expected a non-empty task id")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		tasks, err := c.ListTasks(ctx, parent.ID)
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		found := false
		for _, ts := range tasks {
			if ts.ID == taskID {
				found = true
			}
		}
		if !found {
			t.Fatalf("spawned task %s not found in parent's task list", taskID)
		}

		// Short-lived per-iteration context: each poll opens its own SSE
		// connection and must close it again (rather than leaking one per
		// iteration) so httptest.Server.Close() doesn't hang at the end
		// waiting for dangling connections to end.
		pollCtx, cancelPoll := context.WithTimeout(ctx, 150*time.Millisecond)
		evCh, err := c.SubscribeEvents(pollCtx, parent.ID, 0)
		if err != nil {
			cancelPoll()
			t.Fatalf("SubscribeEvents: %v", err)
		}
		completed := false
	drain:
		for {
			select {
			case ev, ok := <-evCh:
				if !ok {
					break drain
				}
				if ev.Type == events.TypeTaskStatus && ev.Data["task_id"] == taskID && ev.Data["status"] == "completed" {
					completed = true
				}
			case <-pollCtx.Done():
				break drain
			}
		}
		cancelPoll()
		if completed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for background task to complete")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
