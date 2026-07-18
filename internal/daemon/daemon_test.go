package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/client"
	"localcode/internal/commands"
	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/hooks"
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
			"plan":            {Profile: "balanced", Description: "Read-only planning.", Tools: []string{"glob"}},
			"build":           {Profile: "balanced", Description: "Implements changes."},
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

	return New(loop, broker, tasks, nil, nil, "test-version")
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

// TestDaemonVersion confirms GET /api/version reports back whatever
// version string the daemon was constructed with — this is what backs the
// /version prompt command in the TUI and Web UI.
func TestDaemonVersion(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != "test-version" {
		t.Errorf("version = %q, want %q", v, "test-version")
	}
}

// TestDaemonListAgents confirms GET /api/agents reports every configured
// agent, sorted, with its description — the picker Tab-cycling and the
// Web UI's agent selector are built from this.
func TestDaemonListAgents(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	agents, err := c.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}

	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d: %+v", len(agents), agents)
	}
	names := []string{agents[0].Name, agents[1].Name, agents[2].Name}
	want := []string{"build", "general-purpose", "plan"} // alphabetical
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("agents[%d].Name = %q, want %q (sorted order)", i, names[i], want[i])
		}
	}
	for _, a := range agents {
		if a.Name == "plan" && a.Description != "Read-only planning." {
			t.Errorf("plan agent description = %q, want %q", a.Description, "Read-only planning.")
		}
	}
}

// TestDaemonListCommands confirms GET /api/commands reports the custom
// commands loaded on the daemon's Loop, sorted by name.
func TestDaemonListCommands(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	d.Loop.Commands = []commands.Command{
		{Name: "zzz-last", Description: "sorts last"},
		{Name: "aaa-first", Description: "sorts first"},
	}
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	got, err := c.ListCommands(context.Background())
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 commands, got %d: %+v", len(got), got)
	}
	if got[0].Name != "aaa-first" || got[1].Name != "zzz-last" {
		t.Errorf("commands = %+v, want sorted by name", got)
	}
	if got[0].Description != "sorts first" {
		t.Errorf("Description = %q, want %q", got[0].Description, "sorts first")
	}
}

// TestDaemonGetSettings confirms GET /api/settings reflects the Loop's
// live "/config" toggles, both defaults and after they're changed.
func TestDaemonGetSettings(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	got, err := c.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !got.AutoCompactEnabled || !got.ShowTPS {
		t.Errorf("Settings = %+v, want both true by default", got)
	}

	d.Loop.SetAutoCompactEnabled(false)
	got, err = c.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.AutoCompactEnabled {
		t.Error("expected AutoCompactEnabled=false to be reflected after SetAutoCompactEnabled(false)")
	}
}

// TestDaemonSessionStartHookFires confirms creating a session runs any
// configured session_start hooks (fire-and-forget — verified here via a
// side effect, since the API response doesn't carry hook results).
func TestDaemonSessionStartHookFires(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	dir := t.TempDir()
	marker := filepath.Join(dir, "started")

	d := newTestDaemon(t, model.URL)
	d.Loop.Config.Hooks = hooks.Config{hooks.EventSessionStart: {{Command: "echo started > " + marker}}}
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	if _, err := c.CreateSession(context.Background(), "general-purpose"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// The hook runs synchronously inside handleCreateSession before the
	// HTTP response is written, so no polling/sleep is needed here.
	if _, err := os.Stat(marker); err != nil {
		t.Error("expected the session_start hook to have run")
	}
}

// TestDaemonUploadFile confirms POST /api/sessions/{id}/uploads saves the
// file under ~/.localcode/uploads/<session-id>/<filename> and returns that
// absolute path, with content intact.
func TestDaemonUploadFile(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	path, err := c.UploadFile(ctx, sess.ID, "notes.txt", strings.NewReader("hello upload"))
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	wantPath := filepath.Join(fakeHome, ".localcode", "uploads", sess.ID, "notes.txt")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(data) != "hello upload" {
		t.Errorf("uploaded content = %q, want %q", data, "hello upload")
	}
}

func TestDaemonUploadFileUnknownSession(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()
	t.Setenv("HOME", t.TempDir())

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	if _, err := c.UploadFile(context.Background(), "does-not-exist", "x.txt", strings.NewReader("x")); err == nil {
		t.Error("expected an error uploading to an unknown session")
	}
}

// TestDaemonUploadFileSanitizesPathTraversal confirms a filename with
// directory-traversal components can't escape the session's uploads dir.
func TestDaemonUploadFileSanitizesPathTraversal(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	path, err := c.UploadFile(ctx, sess.ID, "../../../etc/evil.txt", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	wantDir := filepath.Join(fakeHome, ".localcode", "uploads", sess.ID)
	if filepath.Dir(path) != wantDir {
		t.Errorf("saved to %q, want it confined to %q", path, wantDir)
	}
	if filepath.Base(path) != "evil.txt" {
		t.Errorf("filename = %q, want traversal components stripped to \"evil.txt\"", filepath.Base(path))
	}
}

// TestDaemonListMCPServersEmptyWhenNilManager confirms GET /api/mcp-servers
// returns an empty (not null) array when no MCP servers are configured —
// the daemon's MCP field is nil in that case.
func TestDaemonListMCPServersEmptyWhenNilManager(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	got, err := c.ListMCPServers(context.Background())
	if err != nil {
		t.Fatalf("ListMCPServers: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListMCPServers() = %v, want empty", got)
	}
}

// TestDaemonSwitchAgent confirms switching a session's agent mid-
// conversation updates the session (visible via GET /api/sessions/{id})
// and emits an agent.switched event, without touching anything else about
// the session.
func TestDaemonSwitchAgent(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "plan")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Agent != "plan" {
		t.Fatalf("expected session to start as \"plan\", got %q", sess.Agent)
	}

	updated, err := c.SwitchAgent(ctx, sess.ID, "build")
	if err != nil {
		t.Fatalf("SwitchAgent: %v", err)
	}
	if updated.Agent != "build" {
		t.Errorf("SwitchAgent returned Agent = %q, want %q", updated.Agent, "build")
	}

	got, err := d.Loop.Store.Get(sess.ID)
	if err != nil {
		t.Fatalf("re-fetch session: %v", err)
	}
	if got.Agent != "build" {
		t.Errorf("session Agent after switch = %q, want %q", got.Agent, "build")
	}

	all, err := d.Loop.Store.Events(sess.ID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	sawSwitch := false
	for _, ev := range all {
		if ev.Type == events.TypeAgentSwitched {
			sawSwitch = true
			if ev.Data["agent"] != "build" {
				t.Errorf("agent.switched event data = %+v, want agent=build", ev.Data)
			}
		}
	}
	if !sawSwitch {
		t.Error("expected an agent.switched event")
	}
}

func TestDaemonSwitchAgentUnknownAgent(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "plan")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := c.SwitchAgent(ctx, sess.ID, "does-not-exist"); err == nil {
		t.Error("expected an error switching to an unconfigured agent name")
	}
}

// TestDaemonRenameSession confirms POST /api/sessions/{id}/rename sets the
// session's Title (visible via GET /api/sessions/{id}) and emits a
// session.renamed event.
func TestDaemonRenameSession(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
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

	updated, err := c.RenameSession(ctx, sess.ID, "My renamed session")
	if err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if updated.Title != "My renamed session" {
		t.Errorf("RenameSession returned Title = %q, want %q", updated.Title, "My renamed session")
	}

	got, err := d.Loop.Store.Get(sess.ID)
	if err != nil {
		t.Fatalf("re-fetch session: %v", err)
	}
	if got.Title != "My renamed session" {
		t.Errorf("session Title after rename = %q, want %q", got.Title, "My renamed session")
	}

	all, err := d.Loop.Store.Events(sess.ID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawRenamed bool
	for _, ev := range all {
		if ev.Type == events.TypeSessionRenamed {
			sawRenamed = true
			if ev.Data["title"] != "My renamed session" {
				t.Errorf("session.renamed event data = %+v, want title=\"My renamed session\"", ev.Data)
			}
		}
	}
	if !sawRenamed {
		t.Error("expected a session.renamed event")
	}
}

func TestDaemonRenameSessionUnknownSession(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	if _, err := c.RenameSession(context.Background(), "does-not-exist", "x"); err == nil {
		t.Error("expected an error renaming an unknown session")
	}
}

// TestDaemonDeleteSession confirms DELETE /api/sessions/{id} removes the
// session — a subsequent GET 404s.
func TestDaemonDeleteSession(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
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

	if err := c.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	if _, err := d.Loop.Store.Get(sess.ID); err == nil {
		t.Error("expected the session to be gone after DeleteSession")
	}
}

func TestDaemonDeleteSessionUnknownSession(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	httpSrv := httptest.NewServer(d.Handler())
	defer httpSrv.Close()

	c := client.New(httpSrv.URL)
	if err := c.DeleteSession(context.Background(), "does-not-exist"); err == nil {
		t.Error("expected an error deleting an unknown session")
	}
}

// TestDaemonDeleteSessionRefusesWhileBusy confirms a session with a turn
// in progress can't be deleted out from under it (409, not deleted).
func TestDaemonDeleteSessionRefusesWhileBusy(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
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

	d.busyMu.Lock()
	d.busy[sess.ID] = true
	d.busyMu.Unlock()

	if err := c.DeleteSession(ctx, sess.ID); err == nil {
		t.Error("expected an error deleting a session with a turn in progress")
	}

	if _, err := d.Loop.Store.Get(sess.ID); err != nil {
		t.Error("expected the session to still exist after a refused delete")
	}
}
