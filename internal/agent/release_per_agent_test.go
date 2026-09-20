package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// captureStderr redirects os.Stderr while fn executes and returns what was written.
func captureStderr(fn func()) string {
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	outC := make(chan string)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		outC <- buf.String()
	}()

	fn()

	w.Close()
	os.Stderr = oldStderr
	return <-outC
}

// TestToolsForTurnWithToolSwitches verifies that:
// 1. ToolSwitches {"write": false} disables write_file while keeping edit, read_file, etc.
// 2. Unspecified tools remain enabled.
// 3. Opencode alias "task: false" disables Task, TaskBackground, and TaskCollect.
func TestToolsForTurnWithToolSwitches(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	registry := tools.NewRegistry(nil)
	registry.Register(tools.ReadFile{})
	registry.Register(tools.WriteFile{})
	registry.Register(tools.Edit{})
	registry.Register(tools.Bash{})
	registry.Register(tools.Glob{})
	registry.Register(tools.Grep{})

	cfg := &config.Config{
		Profiles: map[string]config.Profile{
			"smart": {Provider: "local", Model: "m"},
		},
		Agents: map[string]config.AgentConfig{
			"plan": {
				Profile: "smart",
				ToolSwitches: map[string]bool{
					"write": false,
				},
			},
			"notask": {
				Profile: "smart",
				ToolSwitches: map[string]bool{
					"task": false,
				},
			},
		},
	}

	loop := New(store, registry, map[string]provider.Provider{}, cfg)
	tasks := NewTaskManager(context.Background(), loop, 5)
	registry.Register(NewTaskTool(tasks, loop.DelegatableAgents))
	registry.Register(NewTaskBackgroundTool(tasks, loop.DelegatableAgents))
	registry.Register(NewTaskCollectTool(tasks))

	// For "plan": write_file must be disabled, while edit, read_file, glob, grep, bash remain
	ctxPlan := config.WithAgent(context.Background(), "plan")
	planTools := loop.toolsForTurn(ctxPlan, cfg.Agents["plan"])
	offered := map[string]bool{}
	for _, name := range planTools {
		offered[name] = true
	}

	if offered["write_file"] {
		t.Errorf("expected write_file to be excluded by 'write: false' switch, but it was offered")
	}
	for _, expected := range []string{"read_file", "edit", "bash", "glob", "grep"} {
		if !offered[expected] {
			t.Errorf("expected tool %q to be offered to plan agent, but was not", expected)
		}
	}

	// For "notask": Task, TaskBackground, TaskCollect must be disabled
	ctxNotask := config.WithAgent(context.Background(), "notask")
	notaskTools := loop.toolsForTurn(ctxNotask, cfg.Agents["notask"])
	for _, name := range notaskTools {
		if name == "Task" || name == "TaskBackground" || name == "TaskCollect" {
			t.Errorf("expected %s to be excluded by 'task: false' switch, but it was offered", name)
		}
	}
}

// TestToolsForTurnUnknownToolSwitchWarns verifies that an unknown tool switch
// emits a warning to stderr in the expected format and does not fail or crash.
func TestToolsForTurnUnknownToolSwitchWarns(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	registry := tools.NewRegistry(nil)
	registry.Register(tools.ReadFile{})

	cfg := &config.Config{
		Profiles: map[string]config.Profile{
			"smart": {Provider: "local", Model: "m"},
		},
		Agents: map[string]config.AgentConfig{
			"tester": {
				Profile: "smart",
				ToolSwitches: map[string]bool{
					"unknown_custom_tool": false,
				},
			},
		},
	}

	loop := New(store, registry, map[string]provider.Provider{}, cfg)

	ctxTester := config.WithAgent(context.Background(), "tester")
	stderr := captureStderr(func() {
		ts := loop.toolsForTurn(ctxTester, cfg.Agents["tester"])
		if len(ts) == 0 {
			t.Errorf("expected read_file to be offered")
		}
	})

	expectedWarning := "agent.tester.tools.unknown_custom_tool names no tool localcode has"
	if !strings.Contains(stderr, expectedWarning) {
		t.Errorf("expected stderr to contain %q, got %q", expectedWarning, stderr)
	}
}

// TestAgentStepsIterationCapEnforced verifies that:
// 1. The turn loop cuts off after N tool-using rounds when agent.Steps == N.
// 2. A recovered error notice is appended to the session.
// 3. The subsequent request forces text-only (req.Tools == nil).
// 4. The model answers in text and the turn succeeds.
func TestAgentStepsIterationCapEnforced(t *testing.T) {
	var mu sync.Mutex
	type reqRecord struct {
		toolsCount int
		hasTools   bool
	}
	var requests []reqRecord

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}

		mu.Lock()
		idx := len(requests)
		requests = append(requests, reqRecord{
			toolsCount: len(body.Tools),
			hasTools:   len(body.Tools) > 0,
		})
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		defer w.(http.Flusher).Flush()

		switch idx {
		case 0:
			// Step 1: request tool call glob "*.go"
			esc, _ := json.Marshal(`{"pattern":"*.go"}`)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"glob\",\"arguments\":%s}}]}}]}\n\n", esc)
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		case 1:
			// Step 2: request tool call glob "*.md" (distinct args to avoid repeat guard)
			esc, _ := json.Marshal(`{"pattern":"*.md"}`)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c2\",\"function\":{\"name\":\"glob\",\"arguments\":%s}}]}}]}\n\n", esc)
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			// Step 3 (post-cap): should have no tools offered, responds with text
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"summary text after reaching cap"}}]}`+"\n\n")
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	registry := tools.NewRegistry(nil)
	registry.Register(tools.Glob{})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL},
		},
		Profiles: map[string]config.Profile{
			"strong": {Provider: "local", Model: "claude-opus-5"},
		},
		Agents: map[string]config.AgentConfig{
			"capped": {
				Profile: "strong",
				Steps:   2,
			},
		},
		DefaultProfile: "strong",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	loop := New(store, registry, map[string]provider.Provider{"local": provider.NewOpenAICompat(srv.URL, "")}, cfg)

	sid := "test-steps-session"
	if _, err := loop.Store.CreateSession(sid, "", "capped", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "capped", "please analyze the repository"); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(requests) != 3 {
		t.Fatalf("expected 3 requests to model (2 tool steps + 1 forced text step), got %d", len(requests))
	}
	if !requests[0].hasTools || requests[0].toolsCount == 0 {
		t.Errorf("request 0 expected tools to be offered, got count %d", requests[0].toolsCount)
	}
	if !requests[1].hasTools || requests[1].toolsCount == 0 {
		t.Errorf("request 1 expected tools to be offered, got count %d", requests[1].toolsCount)
	}
	// The 3rd request MUST have had req.Tools = nil (0 tools)
	if requests[2].toolsCount != 0 {
		t.Errorf("request 2 (post-cap) expected 0 tools, got %d", requests[2].toolsCount)
	}

	// Verify session contains the recovered error notice mentioning the cap
	evs, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Store.Events: %v", err)
	}
	foundCapNotice := false
	for _, ev := range evs {
		if ev.Type == events.TypeError {
			if errMsg, ok := ev.Data["error"].(string); ok && strings.Contains(errMsg, "limit of 2 tool-using iterations for agent \"capped\"") {
				foundCapNotice = true
				break
			}
		}
	}
	if !foundCapNotice {
		t.Errorf("expected session to contain iteration cap notice in events")
	}
}

// TestAgentDefaultRunsNormally verifies regression behavior:
// A turn with an agent without a steps cap offers registered tools and runs normally.
func TestAgentDefaultRunsNormally(t *testing.T) {
	srv, recorded := smartServer(t)
	defer srv.Close()

	loop := newSmartLoop(t, srv.URL)
	sendOne(t, loop, "s-default", "general-purpose")

	reqs := recorded()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].toolsLen == 0 {
		t.Errorf("expected tools to be offered for default agent, got 0")
	}
}

// The line is a fact about the configuration, not about this turn, and a
// turn-shaped line is one people stop reading.
func TestAnUnknownToolSwitchIsSaidOnce(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	registry := tools.NewRegistry(nil)
	registry.Register(tools.ReadFile{})
	cfg := &config.Config{
		Profiles: map[string]config.Profile{"p": {Provider: "local", Model: "m"}},
		Agents: map[string]config.AgentConfig{
			"tester": {Profile: "p", ToolSwitches: map[string]bool{"no_such_tool": false}},
		},
	}
	loop := New(store, registry, map[string]provider.Provider{}, cfg)
	ctx := config.WithAgent(context.Background(), "tester")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	for i := 0; i < 5; i++ {
		loop.toolsForTurn(ctx, cfg.Agents["tester"])
	}
	os.Stderr = old
	w.Close()
	out, _ := io.ReadAll(r)

	if n := strings.Count(string(out), "no_such_tool"); n != 1 {
		t.Errorf("the line was printed %d times across five turns, want once:\n%s", n, out)
	}
}

// A skill run through /skill is one agent's work, and the read it does is
// a permission decision. It was the only permission path in the program
// that did not say whose work it was, so it was answered against the
// top-level rules while the agent's own block said otherwise.
func TestASkillRunIsDecidedUnderTheAgentThatRunsIt(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	dir := t.TempDir()
	skill := filepath.Join(dir, "myskill.md")
	if err := os.WriteFile(skill, []byte("---\nname: myskill\ndescription: d\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := tools.NewRegistry(NewPermissionBroker(store).Func())
	registry.Register(tools.ReadFile{})
	var cfg *config.Config
	// The same resolver the daemon wires in, so the config's rules are
	// the ones consulted. See cmd/localcode/wire.go.
	registry.Resolver = tools.ComposeResolver(
		func(ctx context.Context, toolName, subject string, static bool) tools.Decision {
			return tools.Decision(cfg.ResolvePermissionFor(ctx, toolName, subject, static))
		},
		NewPermissionPolicy(store, nil).ToolsPolicy(),
	)
	cfg = &config.Config{
		Profiles:    map[string]config.Profile{"p": {Provider: "local", Model: "m"}},
		Permissions: config.Permissions{"read_file": {Flat: config.DecisionAllow}},
		Agents: map[string]config.AgentConfig{
			"locked": {Profile: "p", Permission: config.Permissions{"read_file": {Flat: config.DecisionDeny}}},
		},
	}
	loop := New(store, registry, map[string]provider.Provider{}, cfg)
	if _, err := store.CreateSession("s", "", "locked", true); err != nil {
		t.Fatal(err)
	}

	err = loop.runSkillPath(context.Background(), "s", "locked", "/skill "+skill, skill, "")
	if err != nil {
		t.Fatalf("runSkillPath: %v", err)
	}
	evs, err := store.Events("s", 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawDenial bool
	for _, ev := range evs {
		if ev.Type == events.TypeError {
			if msg, _ := ev.Data["error"].(string); strings.Contains(msg, "denied") {
				sawDenial = true
			}
		}
	}
	if !sawDenial {
		t.Error("the agent's own permission block denied read_file and the skill was read anyway")
	}
}
