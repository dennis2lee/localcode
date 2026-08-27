package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"localcode/internal/prompt"
	"localcode/internal/provider"
)

// The rest of the prompt surface: everything model-visible that is not a
// system-block asset.
//
// The registry in prompt_assets.go declares what a request says before
// the conversation starts. It is not the whole surface, and treating it
// as such was the round 12 finding: a tool description steers the model
// exactly as a system instruction does, an MCP server's description is
// written by a stranger, a tool result is the least trusted text a turn
// reads, and none of them appeared in any manifest.
//
// They are not registry assets, though, and forcing them to be would be
// the wrong fix. A tool description belongs in a native tool definition
// where the provider can use it; serializing it into the system prompt
// to satisfy an inventory would make the request worse to make the
// bookkeeping neater. And the executable definitions already live in
// internal/tools and internal/mcp, where they are the actual
// capabilities rather than a description of them.
//
// So these are runtime entries: the manifest learns each one's identity,
// provenance, trust, placement, size and hash, and the request keeps its
// native shape. Duplicating nothing, describing everything.

// mcpToolPrefix is how a tool advertised by an MCP server is named. The
// prefix is the only thing distinguishing another process's tool from
// one compiled into localcode, which is why trust is derived from it.
const mcpToolPrefix = "mcp__"

// toolEntries describes the tool definitions advertised for one call.
//
// One entry per tool, hashed over exactly what the model is told: the
// name, the description and the schema. That hash is what makes drift
// observable, which matters most for MCP, where the text can change
// between runs without anything in this repository changing.
func toolEntries(specs []provider.Tool) []prompt.Entry {
	out := make([]prompt.Entry, 0, len(specs))
	for _, s := range specs {
		body := s.Name + "\x00" + s.Description + "\x00" + string(s.InputSchema)
		if server, ok := mcpServerOf(s.Name); ok {
			// Another process, possibly another machine, and its
			// description is an instruction to the model. External by
			// default, exactly like its output.
			out = append(out, prompt.RuntimeEntry(
				"tool.mcp."+s.Name, prompt.KindToolDescription, prompt.FromMCPServer,
				prompt.TrustExternal, prompt.PlaceToolDefinition, body,
				"advertised by the MCP server "+server))
			continue
		}
		out = append(out, prompt.RuntimeEntry(
			"tool.builtin."+s.Name, prompt.KindToolDescription, prompt.FromProduct,
			prompt.TrustSystem, prompt.PlaceToolDefinition, body,
			"a built-in tool advertised for this call"))
	}
	return out
}

// historyEntries describes the runtime sources in the messages a request
// actually carries.
//
// Derived from the history being sent rather than accumulated as the
// turn runs, and that is the whole correction. The entries used to live
// in a slice inside one sendWithModelText call: a tool result was named
// on the manifest of every request of the turn that produced it, and on
// none of the requests after it. The text did not go anywhere. The next
// user turn re-sent the same tool result and its manifest said the
// request contained no external content, which is the one question
// UntrustedIDs exists to answer, answered wrongly.
//
// Deriving it from the messages has a second property worth more than
// the first: it needs nothing stored. A restarted daemon rebuilds
// history from the event log, and the entries come back with it,
// because they were never a separate thing to lose.
//
// Occurrence identity comes from the tool_use id. The same tool run
// three times in one turn is three sources, and "result.bash" three
// times over cannot be told apart by Explain, which answers with the
// first match and stops.
func historyEntries(msgs []provider.Message) []prompt.Entry {
	names := map[string]string{}
	inputs := map[string]json.RawMessage{}
	var out []prompt.Entry
	for _, m := range msgs {
		for _, b := range m.Content {
			switch b.Type {
			case provider.BlockToolUse:
				names[b.ToolUseID] = b.ToolName
				inputs[b.ToolUseID] = b.ToolInput
				// A delegation's task text is in this request and stays
				// in it: the tool_use block carries its arguments
				// verbatim to every adapter, and history re-sends the
				// block on every later call. The parent naming its own
				// words is right; what was wrong was the class they
				// were named under, and that the child's own request
				// named nothing at all.
				if task, agent, ok := delegatedTaskOf(b.ToolName, b.ToolInput); ok {
					e := childInputEntry(agent, task)
					e.ID += "#" + b.ToolUseID
					out = append(out, e)
				}
			case provider.BlockToolResult:
				// A result that names its sources aggregates several,
				// and one entry for the lump would lose which agents
				// are in it.
				if len(b.Sources) > 0 {
					for _, src := range b.Sources {
						e := childResultEntry(strings.TrimPrefix(src, "child.result."), b.ToolResultContent)
						e.ID = src
						e.Reason = "one of " + strconv.Itoa(len(b.Sources)) +
							" sub-agent answers this collection carries"
						out = append(out, e)
					}
					continue
				}
				name := names[b.ToolUseID]
				if name == "" {
					// A result whose call is no longer in history: a
					// compaction cut between them, or a log written
					// before tool names were recorded. Named as
					// unattributable rather than dropped, because the
					// text is in the request either way.
					name = "unknown"
				}
				e := toolResultEntry(name, b.ToolResultContent, inputs[b.ToolUseID])
				e.ID += "#" + b.ToolUseID
				out = append(out, e)
			case provider.BlockText:
				if b.Source == "" {
					continue
				}
				if e, ok := entryForSource(b.Source, b.Text); ok {
					out = append(out, e)
				}
			}
		}
	}
	return out
}

// entryForSource rebuilds the entry for a tagged message block from its
// ID and its text.
//
// The ID is the only thing persisted, deliberately. Writing the whole
// entry into the event log would put a second copy of the classification
// beside the one the constructors already own, and the two would drift;
// the constructors are the definition, and this is a lookup into them.
func entryForSource(id, text string) (prompt.Entry, bool) {
	switch {
	case strings.HasPrefix(id, "skill.body."):
		return skillBodyEntry(strings.TrimPrefix(id, "skill.body."), text), true
	case strings.HasPrefix(id, "command."):
		return commandEntry(strings.TrimPrefix(id, "command."), text), true
	case strings.HasPrefix(id, "reminder."):
		return reminderEntry(strings.TrimPrefix(id, "reminder."), text), true
	case strings.HasPrefix(id, "child.input."):
		return childInputEntry(strings.TrimPrefix(id, "child.input."), text), true
	}
	return prompt.Entry{}, false
}

// mcpServerOf reports the server a tool name belongs to, for the
// "mcp__<server>__<tool>" naming the MCP layer uses.
func mcpServerOf(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, mcpToolPrefix)
	if !ok {
		return "", false
	}
	server, _, found := strings.Cut(rest, "__")
	if !found || server == "" {
		return "", false
	}
	return server, true
}

// Runtime entry constructors for the model-visible sources that enter a
// conversation rather than a system block. Each one exists because the
// source has a provenance and a trust class that the message role it
// lands in does not express: a tool result placed in a user message is
// not the user speaking, and a child's answer is not the product's.

// skillBodyEntry describes an invoked skill's body, which is distinct
// from the skill index: the index lists what exists, the body is the
// procedure the model was actually handed.
func skillBodyEntry(name, body string) prompt.Entry {
	return prompt.RuntimeEntry(
		"skill.body."+name, prompt.KindSkill, prompt.FromSkill, prompt.TrustUser,
		prompt.PlaceMessage, body, "the body of skill "+name+", invoked this turn")
}

// commandEntry describes a custom or built-in command's expansion. User
// trusted, because a command is text the person installed and invoked,
// and recorded separately from ordinary typing because the model reads
// the expansion while the transcript shows the command.
func commandEntry(name, body string) prompt.Entry {
	return prompt.RuntimeEntry(
		"command."+name, prompt.KindUtilityPrompt, prompt.FromUser, prompt.TrustUser,
		prompt.PlaceMessage, body, "the expansion of the "+name+" command")
}

// childInputEntry describes the task text handed to a sub-agent, and
// childResultEntry what it handed back.
//
// The result is external: a child reads tool output, MCP output and
// files, and its answer is a rendering of all of it. It re-enters
// through a tool result, and a tool result is a data position, but the
// trust class is what says so rather than the position implying it.
// childInputEntry is recorded on the child's own request, which is the
// one that contains it. It used to be recorded on the parent's next
// request instead, where the text does not appear at all: the parent
// model wrote the task, handed it to a tool and got an answer back, and
// none of that puts the task into the parent's next prompt. A manifest
// that selected it there described a request that did not exist, while
// the request that really carried it named nothing.
//
// Its author is the parent model, so it is neither the product's text
// nor the person's, and its authority is its own class: it instructs the
// child, because it is the child's whole purpose, without claiming to be
// anybody else's words. See prompt.TrustDelegated.
func childInputEntry(agent, task string) prompt.Entry {
	return prompt.RuntimeEntry(
		"child.input."+agent, prompt.KindAgentPrompt, prompt.FromParentAgent, prompt.TrustDelegated,
		prompt.PlaceMessage, task, "the task the "+agent+" sub-agent was given by its parent")
}

func childResultEntry(agent, result string) prompt.Entry {
	return prompt.RuntimeEntry(
		"child.result."+agent, prompt.KindExternalContent, prompt.FromChildResult,
		prompt.TrustExternal, prompt.PlaceMessage, result,
		"what the "+agent+" sub-agent reported back")
}

// toolResultEntry describes one tool's output as it re-enters the
// conversation. The least trusted text a turn reads, and the one most
// likely to contain something shaped like an instruction.
func toolResultEntry(tool, content string, input json.RawMessage) prompt.Entry {
	// A delegation's result is a child's answer, which is a rendering of
	// everything that child read. It arrives through the same tool-result
	// channel as any other tool, and the provenance is what keeps the two
	// apart: both are external, and only one of them is a sub-agent.
	if isDelegationTool(tool) {
		return childResultEntry(delegatedAgent(tool, input), content)
	}
	prov := prompt.FromToolResult
	reason := "output of the " + tool + " tool"
	if server, ok := mcpServerOf(tool); ok {
		prov = prompt.FromMCPServer
		reason = "output of " + tool + ", from the MCP server " + server
	}
	return prompt.RuntimeEntry(
		"result."+tool, prompt.KindExternalContent, prov, prompt.TrustExternal,
		prompt.PlaceMessage, content, reason)
}

// isDelegationTool reports whether a tool name is one of the ones that
// runs a sub-agent, so its result can be attributed to the child rather
// than to the tool call that fetched it.
func isDelegationTool(name string) bool {
	switch name {
	case "Task", "TaskBackground", "TaskCollect":
		return true
	}
	return false
}

// delegatedTaskOf pulls the task and the agent out of a delegation
// tool's arguments. Not a delegation, or arguments that will not parse,
// and there is nothing to record.
func delegatedTaskOf(tool string, input json.RawMessage) (task, agent string, ok bool) {
	if !isDelegationTool(tool) {
		return "", "", false
	}
	var args struct {
		Agent  string `json:"agent"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &args); err != nil || args.Prompt == "" {
		return "", "", false
	}
	return args.Prompt, delegatedAgent(tool, input), true
}

// delegatedAgent names the sub-agent a delegation call ran, so a child's
// input and answer are recorded under the agent that produced them
// rather than under the tool that fetched them. Every delegation tool
// takes the agent by name; a call whose arguments will not parse falls
// back to the tool name, because a manifest entry with a slightly worse
// name is better than a manifest missing the entry.
func delegatedAgent(tool string, input json.RawMessage) string {
	var args struct {
		Agent string `json:"agent"`
	}
	if err := json.Unmarshal(input, &args); err == nil && args.Agent != "" {
		return args.Agent
	}
	return tool
}

// delegatedTaskKey carries the task a sub-agent was given into that
// sub-agent's own turn.
//
// On the context rather than through SendMessage's signature, for the
// same reason the Smart Agent pin and the task depth are: SendMessage is
// the public entry point and every caller of it is not a delegation. The
// value is set by the task manager immediately before the child's turn
// starts and read once, where the opening message is built.
type delegatedTaskKey struct{}

type delegatedTask struct {
	agent string
	task  string
}

func withDelegatedTask(ctx context.Context, agent, task string) context.Context {
	if agent == "" || task == "" {
		return ctx
	}
	return context.WithValue(ctx, delegatedTaskKey{}, delegatedTask{agent: agent, task: task})
}

// delegatedTaskFrom reports the task this turn was delegated, if it was
// one. The comparison against the turn's own text is the caller's: a
// child whose first message is the delegated task tags that message, and
// a turn that is not a delegation tags nothing.
func delegatedTaskFrom(ctx context.Context) (delegatedTask, bool) {
	d, ok := ctx.Value(delegatedTaskKey{}).(delegatedTask)
	return d, ok
}

// reminderEntry describes a runtime message localcode itself writes into
// the conversation: a recovery notice, a carry-on nudge, a truncation
// note. Product text, and instruction, but one-shot and worth
// distinguishing from the standing prompt.
func reminderEntry(kind, text string) prompt.Entry {
	return prompt.RuntimeEntry(
		"reminder."+kind, prompt.KindRuntimeReminder, prompt.FromProduct,
		prompt.TrustSystem, prompt.PlaceMessage, text,
		"a runtime notice localcode wrote for this turn: "+kind)
}
