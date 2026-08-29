package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"localcode/internal/commands"
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
func historyEntries(msgs []provider.Message, delegated bool) []prompt.Entry {
	names := map[string]string{}
	inputs := map[string]json.RawMessage{}
	var out []prompt.Entry
	if len(msgs) > 0 {
		out = append(out, conversationEntry(msgs))
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			switch b.Type {
			case provider.BlockToolUse:
				names[b.ToolUseID] = b.ToolName
				inputs[b.ToolUseID] = b.ToolInput
				// A delegation's task text is in this request and stays
				// in it: the tool_use block carries its arguments
				// verbatim to every adapter, and history re-sends the
				// block on every later call.
				//
				// It is a different entry from the child's, though, and
				// that is the whole point of having two. Here it is the
				// parent model's own earlier output; in the child it is
				// that child's assignment. Same words, same author, two
				// authorities, and one constructor returning one trust
				// class for both made a model's own prior words an
				// instruction to itself.
				if task, agent, ok := delegatedTaskOf(b.ToolName, b.ToolInput); ok {
					e := parentDelegationEntry(agent, task)
					e.ID += "#" + b.ToolUseID
					out = append(out, e)
				}
			case provider.BlockToolResult:
				// A result that names its sources is carrying somebody
				// else's words, and each one is described over its own
				// span. Hashing the whole block once per source was the
				// first version: four children counted the same text
				// four times, and every child's record changed when any
				// sibling answered differently.
				if len(b.Sources) > 0 {
					for _, src := range b.Sources {
						text := spanOf(b.ToolResultContent, src)
						e := childResultEntry(strings.TrimPrefix(src.ID, "child.result."), text)
						e.ID = src.ID
						if len(b.Sources) > 1 {
							e.Reason = "one of " + strconv.Itoa(len(b.Sources)) +
								" sub-agent answers this collection carries"
						}
						out = append(out, e)
					}
					// The rest of the block is localcode's own framing
					// around them: the section headers, and its
					// sentences about children that failed or are still
					// running. Described as what it is rather than
					// folded into somebody's answer.
					if framing := outsideSpans(b.ToolResultContent, b.Sources); framing != "" {
						f := reminderEntry("collection_framing", framing)
						f.ID += "#" + b.ToolUseID
						f.Reason = "localcode's own framing around the collected answers"
						out = append(out, f)
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
				// Spans first: a command expansion is several sources in
				// one message, and declaring the whole of it as the
				// person's words promoted whatever a spliced file or
				// shell command happened to say.
				for _, src := range b.Sources {
					if e, ok := entryForSource(src.ID, spanOf(b.Text, src), delegated); ok {
						out = append(out, e)
					}
				}
				if b.Source == "" {
					continue
				}
				// What no span covers is the block's own source: the
				// command's template, or a skill's framing. Hashed over
				// exactly that rather than over the whole message,
				// because the rest of the message is somebody else's.
				text := b.Text
				if len(b.Sources) > 0 {
					text = outsideSpans(b.Text, b.Sources)
				}
				if e, ok := entryForSource(b.Source, text, delegated); ok {
					out = append(out, e)
				}
			}
		}
	}
	return out
}

// spanOf returns the part of text a source covers, bounded so a record
// written by an older build, or one whose text was capped after the
// spans were taken, cannot slice out of range.
func spanOf(text string, src provider.BlockSource) string {
	from, to := src.From, src.To
	if from < 0 || from > len(text) {
		return ""
	}
	if to > len(text) {
		to = len(text)
	}
	if to <= from {
		return ""
	}
	return text[from:to]
}

// outsideSpans returns everything in text that no source covers, which
// is the framing the tool wrote around the material it collected.
func outsideSpans(text string, srcs []provider.BlockSource) string {
	covered := make([]bool, len(text))
	for _, src := range srcs {
		for i := src.From; i < src.To && i >= 0 && i < len(text); i++ {
			covered[i] = true
		}
	}
	var b strings.Builder
	for i, r := range []byte(text) {
		if !covered[i] {
			b.WriteByte(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// conversationEntry describes the conversation a request carries as one
// record: how much of it there is, and a digest that changes when the
// messages do.
//
// One entry rather than one per message, and the reason is what the
// entries are for. A manifest names a source when there is a question
// about its authority: a tool result may not instruct, a spliced file
// may not, a delegated task may in one request and not in another.
// Ordinary conversation has no such question. The person's own message
// is the person, and the model's own reply is the model, and neither
// needs a declaration to say so.
//
// What it does need is identity. Without this, two sessions with the
// same assets and tools and entirely different conversations produced
// the same manifest id, so a record claiming to identify a request
// identified only the part of it that was not the conversation. The
// digest is over each block's role, kind and content hash, never over
// the text, which is the same rule every other entry follows.
//
// This is a deliberate boundary and it is stated in the inventory as
// one: the conversation is described in aggregate, not message by
// message.
func conversationEntry(msgs []provider.Message) prompt.Entry {
	h := sha256.New()
	blocks := 0
	runes := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			blocks++
			body := b.Text + b.ToolResultContent + string(b.ToolInput)
			runes += len([]rune(body))
			fmt.Fprintf(h, "%s|%s|%s|%s\n", m.Role, b.Type, b.ToolName, hashOfText(body))
		}
	}
	sum := h.Sum(nil)
	e := prompt.RuntimeEntry(
		"conversation", prompt.KindExternalContent, prompt.FromToolResult, prompt.TrustExternal,
		prompt.PlaceMessage, "", fmt.Sprintf("the %d message blocks this request carries", blocks))
	e.Hash = hex.EncodeToString(sum[:8])
	e.Tokens = runes / 4
	return e
}

// hashOfText is the same digest the prompt package takes of a rendered
// body, kept here so a conversation digest is built from block hashes
// rather than from block text.
func hashOfText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// entryForSource rebuilds the entry for a tagged message block from its
// ID and its text.
//
// The ID is the only thing persisted, deliberately. Writing the whole
// entry into the event log would put a second copy of the classification
// beside the one the constructors already own, and the two would drift;
// the constructors are the definition, and this is a lookup into them.
//
// delegated says whether the request being described belongs to a
// sub-agent. It is the one thing an id cannot carry: the same tag means
// "your assignment" in the session it was delegated to and "a model
// wrote this earlier" anywhere else, and a persisted string has no way
// to know which it is looking at.
func entryForSource(id, text string, delegated bool) (prompt.Entry, bool) {
	switch {
	case strings.HasPrefix(id, "skill.frame."):
		return skillFrameEntry(strings.TrimPrefix(id, "skill.frame."), text), true
	case strings.HasPrefix(id, "skill.body."):
		return skillBodyEntry(strings.TrimPrefix(id, "skill.body."), text), true
	case strings.HasPrefix(id, "command."):
		return commandEntry(strings.TrimPrefix(id, "command."), text), true
	case id == "injected.user":
		return injectedEntry(text), true
	case strings.HasPrefix(id, "argument."):
		return argumentEntry(strings.TrimPrefix(id, "argument."), text), true
	case strings.HasPrefix(id, "file."):
		return splicedFileEntry(strings.TrimPrefix(id, "file."), text), true
	case strings.HasPrefix(id, "shell."):
		return splicedShellEntry(strings.TrimPrefix(id, "shell."), text), true
	case strings.HasPrefix(id, "reminder."):
		return reminderEntry(strings.TrimPrefix(id, "reminder."), text), true
	case strings.HasPrefix(id, "debate.review."):
		return reviewEntry(strings.TrimPrefix(id, "debate.review."), text), true
	case strings.HasPrefix(id, "child.input."):
		// Only where the session actually is a sub-agent. The tag says
		// this message was somebody's delegated task; whether it still
		// instructs is a fact about the request being built, not about
		// the string.
		//
		// Forking is what makes the difference visible. A fork copies a
		// session's event log into a new top-level session with no
		// parent, so a forked sub-agent is a session a person drives
		// directly, whose opening message still carries the tag. The
		// task was written by a model, and outside the child context it
		// created it is that model's earlier output like any other.
		name := strings.TrimPrefix(id, "child.input.")
		if !delegated {
			return parentDelegationEntry(name, text), true
		}
		return childInputEntry(name, text), true
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

// isDelegatedSession reports whether a session is a sub-agent's, which
// is what decides whether a delegated task still instructs.
//
// The session's parent, not the tag on its opening message. A fork
// copies a child's event log into a new top-level session with no
// parent, and from then on a person drives it directly; the task that
// created the original child is, in that session, a model's earlier
// output like any other.
func (l *Loop) isDelegatedSession(sessionID string) bool {
	if l.Store == nil {
		return false
	}
	s, err := l.Store.Get(sessionID)
	return err == nil && s.ParentID != ""
}

// messageOrigin says where an opening message came from when it is not
// simply what the person typed.
//
// source names the entry the message as a whole belongs to; spans name
// the pieces of it that came from somewhere else, with the part of the
// text each one occupies. A command expansion needs both, because it is
// the command's own words with a file and a shell command's output
// spliced into the middle, and one classification over the lot was
// exactly the promotion this exists to stop.
type messageOrigin struct {
	source string
	spans  []provider.BlockSource
	// auto marks a message localcode wrote and sent on the person's
	// behalf, the way keep_going's nudge is marked: it belongs in the log,
	// because the model was really given it and a restart has to rebuild
	// the same history, and it does not belong in the transcript as a
	// typed line or in Up/Down recall. Both clients already honour it.
	auto bool
}

// expansionSpans turns a command's expanded segments into the text that
// goes on the wire and the spans that describe it.
//
// The text is the segments joined, which is what the expansion always
// produced; nothing about the request changes. What is new is that the
// pieces are still identifiable afterwards.
func expansionSpans(name string, segs []commands.Segment) (string, []provider.BlockSource) {
	var b strings.Builder
	var spans []provider.BlockSource
	shell := 0
	for _, seg := range segs {
		from := b.Len()
		b.WriteString(seg.Text)
		if seg.Text == "" {
			continue
		}
		switch seg.Kind {
		case commands.SegmentArguments:
			spans = append(spans, provider.BlockSource{ID: "argument." + name, From: from, To: b.Len()})
		case commands.SegmentFile:
			spans = append(spans, provider.BlockSource{ID: "file." + seg.Ref, From: from, To: b.Len()})
		case commands.SegmentShell:
			shell++
			ref := seg.Ref
			if ref == "" {
				ref = strconv.Itoa(shell)
			}
			spans = append(spans, provider.BlockSource{ID: "shell." + ref, From: from, To: b.Len()})
		}
	}
	return b.String(), spans
}

// skillFrameEntry describes the sentence localcode writes around an
// invoked skill: "follow this skill's instructions". Product text, and
// the smallest of the three authors in that one message.
func skillFrameEntry(name, text string) prompt.Entry {
	return prompt.RuntimeEntry(
		"skill.frame."+name, prompt.KindRuntimeReminder, prompt.FromProduct, prompt.TrustSystem,
		prompt.PlaceMessage, text, "localcode's framing around the "+name+" skill")
}

// injectedEntry describes something the person typed while the turn was
// already running.
//
// It is their own instruction, and it arrives inside a user-role message
// made of tool results, because two user messages in a row is a shape
// some providers reject. So the position says "tool output" and the
// author is the person: exactly the case the role cannot express, and
// the reason trust is a field rather than something read off the role.
func injectedEntry(text string) prompt.Entry {
	return prompt.RuntimeEntry(
		"injected.user", prompt.KindUserInstruction, prompt.FromUser, prompt.TrustUser,
		prompt.PlaceMessage, text, "typed by the person while the turn was running")
}

// argumentEntry describes what the person typed after a command name.
// The one part of an expansion that is unambiguously theirs.
func argumentEntry(name, text string) prompt.Entry {
	return prompt.RuntimeEntry(
		"argument."+name, prompt.KindUtilityPrompt, prompt.FromUser, prompt.TrustUser,
		prompt.PlaceMessage, text, "what the person typed after "+name)
}

// splicedFileEntry describes a file a command pasted into its own text
// with an @path reference.
//
// External, and that is the finding it comes from. The person chose to
// include the file; they did not write what is in it, and what is in it
// can change without the command changing. A dependency's README, a
// generated report, a file something downloaded: an expansion that
// declared all of it as the person's instruction promoted whatever the
// file happened to say. The instruction is the command's own text
// saying what to do with the file; the file is the material.
func splicedFileEntry(ref, text string) prompt.Entry {
	return prompt.RuntimeEntry(
		"file."+ref, prompt.KindExternalContent, prompt.FromWorkspace, prompt.TrustExternal,
		prompt.PlaceMessage, text, "the contents of "+ref+", spliced in by a command")
}

// splicedShellEntry describes the output of a shell command a command
// template ran. The same class as any other tool output, because that is
// what it is.
func splicedShellEntry(ref, text string) prompt.Entry {
	return prompt.RuntimeEntry(
		"shell."+ref, prompt.KindExternalContent, prompt.FromToolResult, prompt.TrustExternal,
		prompt.PlaceMessage, text, "the output of "+ref+", spliced in by a command")
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
// parentDelegationEntry is the same text seen from the other side: the
// task as it sits in the parent's own history, in the arguments of the
// tool_use block that asked for the delegation.
//
// Generated, not delegated. The parent model wrote it, so in the
// parent's request it is that model's own earlier output, and a model's
// own words must not instruct it by having been addressed to somebody
// else. A parent that read a hostile tool result and put the influence
// into a task would otherwise be reading it back with instruction
// authority on every later request of the turn.
//
// Two constructors rather than one with a flag, so a caller has to
// choose the side it is describing and cannot get the authority by
// default.
func parentDelegationEntry(agent, task string) prompt.Entry {
	return prompt.RuntimeEntry(
		"delegation."+agent, prompt.KindExternalContent, prompt.FromParentAgent,
		prompt.TrustGenerated, prompt.PlaceMessage, task,
		"the task this session's own model wrote for the "+agent+" sub-agent")
}

func childInputEntry(agent, task string) prompt.Entry {
	return prompt.RuntimeEntry(
		"child.input."+agent, prompt.KindAgentPrompt, prompt.FromParentAgent, prompt.TrustDelegated,
		prompt.PlaceMessage, task, "the task the "+agent+" sub-agent was given by its parent")
}

// reviewEntry describes what a reviewing agent said about this session's
// work, as it arrives in the author's next message.
//
// The same class as any other child's answer: another model's words,
// read as material rather than as instruction. It matters here more than
// most, because the message it arrives in is a user-role one and the
// reviewer is telling the author what to change — the framing around it
// is localcode's and instructs; the review itself is a report, and the
// span is what keeps the two apart.
func reviewEntry(agent, text string) prompt.Entry {
	return prompt.RuntimeEntry(
		"debate.review."+agent, prompt.KindExternalContent, prompt.FromChildResult,
		prompt.TrustExternal, prompt.PlaceMessage, text,
		"what the "+agent+" agent said when it reviewed this session's work")
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
	// Not "which tool ran" but "who wrote these words". The two are
	// different questions and answering the first was wrong: all three
	// delegation tools went through here, and one of them,
	// TaskBackground, returns a sentence localcode wrote saying the
	// work has started. That was recorded as a report from a child
	// which had at that moment said nothing at all. The same held for
	// every refusal and every failure the delegation tools return.
	//
	// A result that carries somebody else's material declares it, with
	// spans, and historyEntries describes those separately. Anything
	// left here is the tool answering for itself.
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
// runs a sub-agent. It answers "which tool is this", which is the right
// question for reading a task out of a call's arguments and the wrong
// one for deciding who wrote a result; see toolResultEntry.
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

// delegatedAgent names the sub-agent a delegation call ran, so a task
// is recorded under the agent it was written for rather than under the
// tool that carried it. Every delegation tool takes the agent by name;
// a call whose arguments will not parse falls back to the tool name,
// because a manifest entry with a slightly worse name is better than a
// manifest missing the entry.
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
