package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// A delegated agent cannot hand its task to itself or back up its chain.
//
// A "vision" agent asked to look at an image file, with no tool that could
// open one, handed the request to "vision" again, which did the same,
// until the depth limit stopped it three levels down. The roster it was
// offered had itself in it.

func toolCall(id, name, input string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventToolUseStart, ToolUseID: id, ToolName: name},
		{Type: provider.EventToolUseEnd, ToolUseID: id, ToolInput: json.RawMessage(input)},
		{Type: provider.EventMessageStop, StopReason: "tool_use"},
	}
}

// delegationLoop is a loop with the given agents, all on one profile, the
// delegation tools registered and orchestration on.
func delegationLoop(t *testing.T, turns [][]provider.StreamEvent, agents ...string) (*Loop, *scriptedProvider, string) {
	t.Helper()
	reg := tools.NewRegistry(nil)
	p := &scriptedProvider{turns: turns}
	loop, sessionID := scriptedLoop(t, p, reg)
	loop.Config.Agents = map[string]config.AgentConfig{}
	for _, name := range agents {
		loop.Config.Agents[name] = config.AgentConfig{Profile: "balanced", Description: "the " + name + " agent"}
	}
	on := true
	loop.Config.Orchestrate = &on
	tm := NewTaskManager(context.Background(), loop, 4)
	loop.Tasks = tm
	// A tool that is never hidden, as every real registry has: an
	// allowlist with every tool hidden reads as no restriction at all.
	reg.Register(tools.ReadFile{})
	reg.Register(NewTaskTool(tm, loop.DelegatableAgents))
	reg.Register(NewTaskBackgroundTool(tm, loop.DelegatableAgents))
	reg.Register(NewTaskCollectTool(tm))
	return loop, p, sessionID
}

// offeredTo is the agent names a request's Task schema offered, and
// whether it carried the tool at all.
func offeredTo(req provider.ChatRequest) ([]string, bool) {
	for _, tool := range req.Tools {
		if tool.Name != "Task" {
			continue
		}
		var schema struct {
			Properties struct {
				Agent struct {
					Enum []string `json:"enum"`
				} `json:"agent"`
			} `json:"properties"`
		}
		_ = json.Unmarshal(tool.InputSchema, &schema)
		return schema.Properties.Agent.Enum, true
	}
	return nil, false
}

func TestADelegatedAgentCannotHandItsTaskToItself(t *testing.T) {
	loop, p, sessionID := delegationLoop(t, [][]provider.StreamEvent{
		toolCall("c1", "Task", `{"agent":"vision","prompt":"look at fig.png"}`),
		toolCall("c2", "Task", `{"agent":"vision","prompt":"look at fig.png"}`),
		textReply("I could not open the image."),
		textReply("vision could not open it."),
	}, "general-purpose", "vision", "explore")

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "what is in fig.png?"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	p.mu.Lock()
	reqs := append([]provider.ChatRequest(nil), p.requests...)
	p.mu.Unlock()
	if len(reqs) != 4 {
		t.Fatalf("%d requests, want 4: the parent, the child twice, the parent again", len(reqs))
	}

	// The person's own turn keeps its whole roster, itself included.
	if got, _ := offeredTo(reqs[0]); !reflect.DeepEqual(got, []string{"explore", "general-purpose", "vision"}) {
		t.Errorf("the top-level turn was offered %v", got)
	}
	// The delegated one is not offered itself or the agent above it.
	if got, _ := offeredTo(reqs[1]); !reflect.DeepEqual(got, []string{"explore"}) {
		t.Errorf("the vision sub-agent was offered %v, want only explore", got)
	}
	// And calling it anyway is refused, with what to do instead.
	res, ok := resultFor(reqs[2].Messages, "c2")
	if !ok || !res.IsError || !strings.Contains(res.ToolResultContent, `you are the "vision" agent`) {
		t.Errorf("the second delegation to vision was not refused: %+v (found %v)", res, ok)
	}
	if n := len(loop.Store.Children(sessionID)); n != 1 {
		t.Errorf("%d sub-agent sessions, want 1: a refused delegation creates none", n)
	}
	// The children of the child: none.
	for _, c := range loop.Store.Children(sessionID) {
		if n := len(loop.Store.Children(c.ID)); n != 0 {
			t.Errorf("the vision sub-agent started %d sub-agents of its own", n)
		}
	}
}

// With nothing else to offer, the delegation tools are not offered.
func TestADelegatedAgentWithNobodyElseIsNotOfferedDelegation(t *testing.T) {
	loop, p, sessionID := delegationLoop(t, [][]provider.StreamEvent{
		toolCall("c1", "Task", `{"agent":"vision","prompt":"look at fig.png"}`),
		textReply("done"),
		textReply("done"),
	}, "general-purpose", "vision")

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "look"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	p.mu.Lock()
	reqs := append([]provider.ChatRequest(nil), p.requests...)
	p.mu.Unlock()
	if len(reqs) < 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	if _, offered := offeredTo(reqs[0]); !offered {
		t.Error("the top-level turn lost Task")
	}
	for _, tool := range reqs[1].Tools {
		switch tool.Name {
		case "Task", "TaskBackground", "TaskCollect":
			t.Errorf("the sub-agent was offered %s with nobody to hand work to", tool.Name)
		}
	}
}

// Handing the task back up the chain is refused too, by either tool, and
// the refusal names the chain.
func TestADelegatedAgentCannotHandItsTaskBack(t *testing.T) {
	loop, _, sessionID := delegationLoop(t, nil, "general-purpose", "vision", "explore")
	ctx := WithSessionID(context.Background(), sessionID)
	ctx = withAgentInChain(ctx, "general-purpose")
	ctx = withAgentInChain(withTaskDepth(ctx, 1), "vision")

	for _, name := range []string{"Task", "TaskBackground"} {
		res := loop.Tools.Call(ctx, name, json.RawMessage(`{"agent":"general-purpose","prompt":"you do it"}`), "")
		if !res.IsError || !strings.Contains(res.Content, `"general-purpose" delegated this task to you`) ||
			!strings.Contains(res.Content, "general-purpose -> vision") {
			t.Errorf("%s back to the parent: %+v", name, res)
		}
	}
	if n := len(loop.Store.Children(sessionID)); n != 0 {
		t.Errorf("%d sub-agents started", n)
	}
}

// A background child starts from the manager's own context, so the chain
// is carried across by hand, as the depth is.
func TestABackgroundTaskKnowsWhoLaunchedIt(t *testing.T) {
	loop, _, _ := delegationLoop(t, nil, "general-purpose", "vision")
	launch := withAgentInChain(context.Background(), "general-purpose")
	child := loop.Tasks.childContext(launch, "")
	if got := agentChain(child); !reflect.DeepEqual(got, []string{"general-purpose"}) {
		t.Errorf("chain = %v, want the launching agent", got)
	}
	if taskDepthFromContext(child) != 1 {
		t.Errorf("depth = %d, want 1", taskDepthFromContext(child))
	}
}

// Two children of one parent never share a chain's backing array.
func TestChainsDoNotShareStorage(t *testing.T) {
	parent := withAgentInChain(withAgentInChain(context.Background(), "a"), "b")
	one := withAgentInChain(parent, "c")
	two := withAgentInChain(parent, "d")
	if got := agentChain(one); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("first child chain = %v", got)
	}
	if got := agentChain(two); !reflect.DeepEqual(got, []string{"a", "b", "d"}) {
		t.Errorf("second child chain = %v", got)
	}
}
