package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

func newTestLoopWithDepth(t *testing.T, modelURL string, depth *int) (*Loop, *TaskManager) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	registry := tools.NewRegistry(nil)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelURL},
		},
		Profiles:       map[string]config.Profile{"only": {Provider: "local", Model: "m"}},
		DefaultProfile: "only",
		Agents: map[string]config.AgentConfig{
			"explore": {Profile: "only", Description: "explore agent"},
		},
		SubagentDepth: depth,
	}
	loop := New(store, registry, map[string]provider.Provider{"local": provider.NewOpenAICompat(modelURL, "")}, cfg)
	loop.SetSmartAgentEnabled(true)
	tasks := NewTaskManager(context.Background(), loop, 5)
	registry.Register(NewTaskTool(tasks, loop.DelegatableAgents))
	registry.Register(NewTaskBackgroundTool(tasks, loop.DelegatableAgents))
	registry.Register(NewTaskCollectTool(tasks))
	return loop, tasks
}

func TestSubagentDepthOneStopsSubagentDelegation(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()

	depthLimit := 1
	loop, _ := newTestLoopWithDepth(t, srv.URL, &depthLimit)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	input, _ := json.Marshal(map[string]string{"agent": "explore", "prompt": "do work"})

	// At depth 0 (top-level session), delegation is allowed
	ctxTop := withTaskDepth(WithSessionID(context.Background(), sid), 0)
	if res := loop.Tools.Call(ctxTop, "TaskBackground", input, ""); res.IsError {
		t.Fatalf("top-level delegation with subagent_depth=1 was refused: %s", res.Content)
	}

	// At depth 1 (subagent session), delegation is stopped for both Task and TaskBackground
	ctxSub := withTaskDepth(WithSessionID(context.Background(), sid), 1)

	resBG := loop.Tools.Call(ctxSub, "TaskBackground", input, "")
	if !resBG.IsError {
		t.Fatal("TaskBackground at depth 1 was allowed when subagent_depth=1")
	}
	if !strings.Contains(resBG.Content, "1") {
		t.Errorf("TaskBackground error %q does not name limit 1", resBG.Content)
	}

	resTask := loop.Tools.Call(ctxSub, "Task", input, "")
	if !resTask.IsError {
		t.Fatal("Task at depth 1 was allowed when subagent_depth=1")
	}
	if !strings.Contains(resTask.Content, "1") {
		t.Errorf("Task error %q does not name limit 1", resTask.Content)
	}
}

func TestSubagentDepthZeroStopsTopLevelDelegation(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()

	depthLimit := 0
	loop, _ := newTestLoopWithDepth(t, srv.URL, &depthLimit)

	const sid = "s0"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	input, _ := json.Marshal(map[string]string{"agent": "explore", "prompt": "do work"})

	// At depth 0 (top-level session), delegation is stopped immediately
	ctxTop := withTaskDepth(WithSessionID(context.Background(), sid), 0)

	resBG := loop.Tools.Call(ctxTop, "TaskBackground", input, "")
	if !resBG.IsError {
		t.Fatal("TaskBackground at depth 0 was allowed when subagent_depth=0")
	}
	if !strings.Contains(resBG.Content, "0") {
		t.Errorf("TaskBackground error %q does not name limit 0", resBG.Content)
	}

	resTask := loop.Tools.Call(ctxTop, "Task", input, "")
	if !resTask.IsError {
		t.Fatal("Task at depth 0 was allowed when subagent_depth=0")
	}
	if !strings.Contains(resTask.Content, "0") {
		t.Errorf("Task error %q does not name limit 0", resTask.Content)
	}
}

func TestSubagentDepthUnsetPreservesExistingDelegationBehavior(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()

	// nil SubagentDepth preserves existing default limit 3
	loop, _ := newTestLoopWithDepth(t, srv.URL, nil)

	const sid = "s-unset"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	input, _ := json.Marshal(map[string]string{"agent": "explore", "prompt": "do work"})

	// Depths 0, 1, and 2 must all succeed
	for d := 0; d < 3; d++ {
		ctx := withTaskDepth(WithSessionID(context.Background(), sid), d)
		if res := loop.Tools.Call(ctx, "TaskBackground", input, ""); res.IsError {
			t.Fatalf("TaskBackground at depth %d was refused when subagent_depth unset: %s", d, res.Content)
		}
	}

	// Depth 3 must be refused
	ctx3 := withTaskDepth(WithSessionID(context.Background(), sid), 3)

	resBG := loop.Tools.Call(ctx3, "TaskBackground", input, "")
	if !resBG.IsError {
		t.Fatal("TaskBackground at depth 3 was allowed when subagent_depth unset")
	}
	if !strings.Contains(resBG.Content, fmt.Sprintf("%d", maxTaskDepth)) {
		t.Errorf("TaskBackground error %q does not name limit %d", resBG.Content, maxTaskDepth)
	}

	resTask := loop.Tools.Call(ctx3, "Task", input, "")
	if !resTask.IsError {
		t.Fatal("Task at depth 3 was allowed when subagent_depth unset")
	}
	if !strings.Contains(resTask.Content, fmt.Sprintf("%d", maxTaskDepth)) {
		t.Errorf("Task error %q does not name limit %d", resTask.Content, maxTaskDepth)
	}
}
