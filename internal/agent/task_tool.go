package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"localcode/internal/config"
	"localcode/internal/tools"
)

// TaskTool lets the model delegate a self-contained piece of work to a
// named sub-agent and wait for its answer — the multi-agent-orchestration
// analog of Claude Code's own Task tool, or oh-my-opencode's category
// delegation: a "build" or orchestrator agent picks a specialized agent
// (e.g. a cheap/fast "explore" agent for grepping, a strong-model "review"
// agent for critique) by name rather than doing everything itself on one
// model.
//
// It lives in package agent (not tools) because it needs TaskManager,
// which in turn depends on Loop — tools intentionally has no knowledge of
// agent to avoid a cycle back the other way.
type TaskTool struct {
	manager *TaskManager
	// agents is asked on every call rather than captured once, because
	// the roster changes at runtime: turning Smart Agent on adds six
	// specialists, and a tool holding a startup snapshot would go on
	// advertising an enum that no longer describes what exists.
	agents func(context.Context) map[string]config.AgentConfig
}

func NewTaskTool(manager *TaskManager, agents func(context.Context) map[string]config.AgentConfig) TaskTool {
	return TaskTool{manager: manager, agents: agents}
}

func (t TaskTool) Name() string { return "Task" }

// Both halves of the schema name the agents this tool may be pointed at,
// so both are rendered from the caller's roster rather than the live one.
// The context-free forms remain for a listing that has no turn behind it,
// and hand the same job the same answer. See tools.Contextual.
func (t TaskTool) Description() string { return t.DescriptionFor(context.Background()) }

func (t TaskTool) DescriptionFor(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("Delegate a self-contained piece of work to a specialized sub-agent and wait for its final answer. Available agents:\n")
	writeAgentList(&b, t.agents(ctx))
	return b.String()
}

func (t TaskTool) InputSchema() json.RawMessage { return t.InputSchemaFor(context.Background()) }

func (t TaskTool) InputSchemaFor(ctx context.Context) json.RawMessage {
	return delegationSchema(agentNamesOf(t.agents(ctx)))
}

// RequiresPermission is false: delegating itself has no side effects — any
// tool the sub-agent calls goes through that sub-agent's own permission
// checks the same way a top-level session's would.
func (t TaskTool) RequiresPermission(json.RawMessage) bool { return false }

func (t TaskTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	var args struct {
		Agent  string `json:"agent"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tools.Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}

	agents := t.agents(ctx)
	if _, ok := agents[args.Agent]; !ok {
		return tools.Result{
			Content: fmt.Sprintf("unknown agent %q. Available: %s", args.Agent, strings.Join(agentNamesOf(agents), ", ")),
			IsError: true,
		}
	}

	depth := taskDepthFromContext(ctx)
	if depth >= maxTaskDepth {
		return tools.Result{
			Content: fmt.Sprintf("max sub-agent delegation depth (%d) reached; refusing to delegate further", maxTaskDepth),
			IsError: true,
		}
	}

	parentSessionID, ok := SessionIDFromContext(ctx)
	if !ok {
		return tools.Result{Content: "Task tool has no session context", IsError: true}
	}

	text, err := t.manager.SpawnSync(withTaskDepth(ctx, depth+1), parentSessionID, args.Agent, args.Prompt)
	if err != nil {
		// localcode's sentence about a delegation that did not work.
		// The child said nothing, so nothing here is the child's.
		return tools.Result{Content: fmt.Sprintf("sub-agent %q failed: %v", args.Agent, err), IsError: true}
	}
	// All of it is the child's answer, which is what the span says.
	return tools.Result{
		Content: text,
		Sources: []tools.ResultSource{{ID: args.Agent, From: 0, To: len(text)}},
	}
}
