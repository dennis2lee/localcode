// Package agent implements the core agent loop: send a user message, stream
// the model's response into the session's event log, execute any requested
// tool calls, and repeat until the model stops asking for tools.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"localcode/internal/commands"
	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/hooks"
	"localcode/internal/memory"
	"localcode/internal/modelinfo"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/skills"
	"localcode/internal/tools"
)

const defaultSystemPrompt = "You are a helpful coding assistant with access to file and shell tools. Use them when needed; otherwise answer directly."

const defaultMaxTokens = 4096

// initPrompt is what "/init" sends to the model — the same idea as
// opencode's "/init": scan the repo and write an AGENTS.md rules file so
// future turns (in this project or picked up by opencode/Claude Code too,
// since both read AGENTS.md/CLAUDE.md) start with real project context.
const initPrompt = `Scan this repository (file listing, README, package/build manifests, existing build/lint/test tooling) and create or update an AGENTS.md file at the project root with concise, project-specific guidance for a coding agent: build/lint/test commands, an architecture overview, and code conventions. If AGENTS.md already exists, improve it in place rather than replacing it wholesale. Use your file tools (Glob/Grep/Read to explore, Write or Edit to save AGENTS.md).`

// compactThresholdPercent is the context-window fill percentage that
// triggers auto-compaction (when Loop.AutoCompactEnabled is true).
const compactThresholdPercent = 80.0

// compactionPrompt asks the model to summarize the conversation so far in
// place of running any tools — deliberately sent as a bare Chat call (see
// drainText), not through the normal turn machinery, so it never appears
// in the visible transcript as an ordinary assistant reply.
const compactionPrompt = "Summarize our conversation so far concisely, preserving important facts, decisions, file paths, and outstanding tasks needed for continuity. Output ONLY the summary, with no preamble."

// sessionUsage is the latest known token usage for one session, used to
// compute the context-window-fill percentage and drive auto-compaction.
type sessionUsage struct {
	InputTokens  int
	OutputTokens int
	MaxContext   int
	TPS          float64
}

// modelTotals accumulates token usage across every provider.Chat call a
// session has made against one model. Unlike sessionUsage (the latest
// snapshot, used for context-window-fill %), this is a running sum: each
// API call is billed for its own full request (history included), so
// summing every call's tokens is the correct "how much has this session
// used" figure — see /usage.
type modelTotals struct {
	InputTokens  int
	OutputTokens int
	Calls        int
}

// Loop wires a session store, tool registry, and the set of configured
// model providers together. One Loop instance is shared across sessions;
// per-session conversation history is kept in memory.
type Loop struct {
	Store        *session.Store
	Tools        *tools.Registry
	Providers    map[string]provider.Provider // provider config key -> client
	Config       *config.Config
	SystemPrompt string
	// Skills backs the /skill slash command (list / load by name). It's
	// separate from the Skill *tool* the model can call on its own —
	// this is the same skill set, just also reachable directly by the
	// user typing a command instead of waiting on the model to decide to
	// use it.
	Skills []skills.Skill

	// Commands backs custom user-defined slash commands ("/<name>"),
	// loaded from .localcode/commands/*.md (project) and
	// ~/.localcode/commands/*.md (global) — see internal/commands.
	Commands []commands.Command

	// ProjectDir is the working directory custom commands resolve
	// "!`shell`" and "@file" expansions against.
	ProjectDir string

	// Tasks runs sub-agents in their own sessions. Set by NewTaskManager,
	// so a Loop built without one (a bare Loop in a test) simply has no
	// delegation rather than a nil dereference.
	Tasks *TaskManager

	// MemoryDir is this project's auto-memory directory (see
	// internal/memory) — "" if auto memory is disabled. Backs the
	// "/memory" local command; the actual read/write of memory files
	// happens via the model's ordinary file tools, not here.
	MemoryDir string

	mu                 sync.Mutex
	messages           map[string][]provider.Message     // sessionID -> history
	usage              map[string]sessionUsage           // sessionID -> latest known usage
	cumulativeUsage    map[string]map[string]modelTotals // sessionID -> model -> running totals, see /usage
	autoCompactEnabled bool                              // process-global runtime setting, toggleable via "/config"
	showTPS            bool                              // process-global runtime setting, toggleable via "/config"
	autoDelegate       bool                              // process-global runtime setting, toggleable via "/config"
}

func New(store *session.Store, reg *tools.Registry, providers map[string]provider.Provider, cfg *config.Config) *Loop {
	return &Loop{
		Store:              store,
		Tools:              reg,
		Providers:          providers,
		Config:             cfg,
		SystemPrompt:       defaultSystemPrompt,
		autoCompactEnabled: cfg.CompactEnabled(),
		showTPS:            cfg.TPSEnabled(),
		autoDelegate:       cfg.DelegateEnabled(),
		messages:           map[string][]provider.Message{},
		usage:              map[string]sessionUsage{},
		cumulativeUsage:    map[string]map[string]modelTotals{},
	}
}

// ClearSessionState drops all in-memory state Loop keeps for sessionID
// (conversation history, usage snapshot, cumulative per-model totals) —
// called when a session is deleted, so its memory isn't retained forever.
func (l *Loop) ClearSessionState(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.messages, sessionID)
	delete(l.usage, sessionID)
	delete(l.cumulativeUsage, sessionID)
}

// AutoCompactEnabled reports whether auto-compaction is currently on —
// process-global, toggleable live via "/config auto_compact on|off".
func (l *Loop) AutoCompactEnabled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.autoCompactEnabled
}

// SetAutoCompactEnabled changes the live auto-compaction setting.
func (l *Loop) SetAutoCompactEnabled(v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.autoCompactEnabled = v
}

// ShowTPS reports whether usage events should carry a tokens-per-second
// figure for display — process-global, toggleable live via "/config
// show_tps on|off".
func (l *Loop) ShowTPS() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.showTPS
}

// SetShowTPS changes the live TPS-display setting.
func (l *Loop) SetShowTPS(v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.showTPS = v
}

// AutoDelegateEnabled reports whether prompts matching the auto_delegate
// rules are routed to the configured sub-agent — process-global,
// toggleable live via "/config auto_delegate on|off".
func (l *Loop) AutoDelegateEnabled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.autoDelegate
}

// SetAutoDelegateEnabled changes the live auto-delegation setting. It has
// no effect when the config has no auto_delegate block to say which agent
// to delegate to — see delegateTarget.
func (l *Loop) SetAutoDelegateEnabled(v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.autoDelegate = v
}

// SendMessage appends a user turn to sessionID's history and drives the
// agent loop (model call -> optional tool calls -> model call -> ...) until
// the model produces a final answer. agentName selects which model profile
// to use, per the config's agents map.
func (l *Loop) SendMessage(ctx context.Context, sessionID, agentName, text string) error {
	if len(l.Config.Hooks) > 0 {
		blocked, reason, _ := hooks.Run(ctx, l.Config.Hooks, hooks.EventUserPromptSubmit, map[string]any{
			"session_id": sessionID,
			"prompt":     text,
		})
		if blocked {
			l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
			l.Store.Append(sessionID, events.TypeError, map[string]any{
				"error": fmt.Sprintf("blocked by user_prompt_submit hook: %s", reason),
			})
			return nil
		}
	}

	// /skill lists available skills locally (no model call); /skill <name>
	// splices that skill's full body into what the model sees, so it
	// starts following it immediately instead of the user hoping the
	// model decides to call the Skill tool on its own. Either way the
	// displayed transcript keeps the short "/skill ..." the user typed.
	//
	// "/skill <name>" is the older spelling, kept working so it doesn't
	// break under anyone's fingers; "/<name>" below is the documented one.
	if arg, ok := parseSkillCommand(text); ok {
		if arg == "" {
			return l.listSkills(sessionID, text)
		}
		name, args := arg, ""
		if idx := strings.IndexAny(arg, " \t"); idx >= 0 {
			name, args = arg[:idx], strings.TrimSpace(arg[idx+1:])
		}
		sk, found := l.findSkill(name)
		if !found {
			l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
			l.Store.Append(sessionID, events.TypeError, map[string]any{
				"error": fmt.Sprintf("unknown skill %q. Available: %s", name, l.skillNames()),
			})
			return nil
		}
		return l.sendWithModelText(ctx, sessionID, agentName, text, skillModelText(sk, args), "", "")
	}

	if strings.TrimSpace(text) == "/init" {
		return l.sendWithModelText(ctx, sessionID, agentName, text, initPrompt, "", "")
	}

	if strings.TrimSpace(text) == "/memory" {
		return l.showMemoryInfo(sessionID, text)
	}

	if arg, ok := parseConfigCommand(text); ok {
		return l.handleConfigCommand(sessionID, text, arg)
	}

	if arg, ok := parseCompactCommand(text); ok {
		return l.handleCompactCommand(ctx, sessionID, agentName, text, arg)
	}

	if strings.TrimSpace(text) == "/usage" {
		return l.handleCostCommand(sessionID, text)
	}

	if cmd, args, ok := l.matchCustomCommand(text); ok {
		modelText, err := commands.Expand(cmd, args, l.ProjectDir)
		if err != nil {
			l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
			l.Store.Append(sessionID, events.TypeError, map[string]any{"error": err.Error()})
			return nil
		}
		return l.sendWithModelText(ctx, sessionID, agentName, text, modelText, cmd.Agent, cmd.Model)
	}

	// A registered skill runs by its own name, "/<skill-name>", the same
	// shape custom commands use. Checked last so nothing user-facing can
	// be shadowed by a skill that happens to share a name: built-in
	// commands win first, then custom commands, then skills.
	if sk, args, ok := l.matchSkillName(text); ok {
		return l.sendWithModelText(ctx, sessionID, agentName, text, skillModelText(sk, args), "", "")
	}

	// Everything above is a command of some kind. What's left is an
	// ordinary prompt, the only thing worth handing to a cheaper agent.
	if target, ok := l.delegateTarget(sessionID, agentName, text); ok {
		return l.delegatePrompt(ctx, sessionID, target, text)
	}

	return l.sendWithModelText(ctx, sessionID, agentName, text, text, "", "")
}

// delegateTarget decides whether this prompt should be answered by a
// cheaper sub-agent instead of the session's own, and names that agent.
//
// The guards matter more than the match: delegating from within a
// delegated session, or to the agent already running, would recurse
// forever, so both are refused before the patterns are even consulted.
func (l *Loop) delegateTarget(sessionID, agentName, text string) (string, bool) {
	cfg := l.Config.AutoDelegate
	if cfg == nil || !l.AutoDelegateEnabled() {
		return "", false
	}
	// Delegating to the agent that's already running would spawn a child
	// whose prompt matches the same rule, and so on without end.
	if cfg.Agent == "" || cfg.Agent == agentName {
		return "", false
	}
	// Task and delegated sessions are children (they carry a parent ID and
	// are hidden from the session list). Recursing from one is the other
	// half of the same infinite-regress problem.
	if sess, err := l.Store.Get(sessionID); err == nil && sess.ParentID != "" {
		return "", false
	}
	if !cfg.MatchesPrompt(text) {
		return "", false
	}
	return cfg.Agent, true
}

// delegatePrompt answers a turn from a sub-agent instead of the session's
// own model, and is the whole point of the feature: the sub-agent runs in
// its own session, so its (different) model never touches this session's
// cached prefix. Switching the session's own model would have invalidated
// tools, system prompt, and every prior turn at once.
//
// The transcript records the prompt and the answer exactly as an ordinary
// turn would, plus a marker naming the agent that handled it, so the
// delegation is visible rather than silently swapping models underneath
// the user.
func (l *Loop) delegatePrompt(ctx context.Context, sessionID, targetAgent, text string) error {
	if l.Tasks == nil {
		// No task manager wired up (a bare Loop in a test, say). Fall back
		// to answering normally rather than failing the turn.
		return l.sendWithModelText(ctx, sessionID, l.sessionAgent(sessionID), text, text, "", "")
	}

	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text})
	l.Store.Append(sessionID, events.TypeDelegated, map[string]any{"agent": targetAgent, "prompt": text})

	answer, err := l.Tasks.SpawnSync(ctx, sessionID, targetAgent, text)
	if err != nil {
		l.Store.Append(sessionID, events.TypeError, map[string]any{
			"error": fmt.Sprintf("delegation to %q failed: %v", targetAgent, err),
		})
		return nil
	}

	// Record both halves in the history the main model sees, so its next
	// turn has the exchange as context even though it never ran for it.
	// Both are needed: appending only the answer would leave the history
	// with two assistant turns in a row, which some providers reject.
	l.appendDelegatedTurn(sessionID, text, answer)
	l.Store.Append(sessionID, events.TypeMessagePartDelta, map[string]any{"text": answer})
	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": answer})
	return nil
}

// sessionAgent reads a session's current agent, falling back to the
// configured default when the session is unknown.
func (l *Loop) sessionAgent(sessionID string) string {
	if sess, err := l.Store.Get(sessionID); err == nil && sess.Agent != "" {
		return sess.Agent
	}
	return "general-purpose"
}

// appendDelegatedTurn adds the prompt and the sub-agent's answer to the
// in-memory history the main model sees, without any provider call.
func (l *Loop) appendDelegatedTurn(sessionID, prompt, answer string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages[sessionID] = append(l.messages[sessionID],
		provider.Message{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(prompt)}},
		provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(answer)}},
	)
}

// matchSkillName recognizes "/<skill-name>" and "/<skill-name> <args>"
// against a registered skill.
func (l *Loop) matchSkillName(text string) (skills.Skill, string, bool) {
	trimmed := strings.TrimSpace(text)
	rest, ok := strings.CutPrefix(trimmed, "/")
	if !ok {
		return skills.Skill{}, "", false
	}
	name, args := rest, ""
	if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
		name, args = rest[:idx], strings.TrimSpace(rest[idx+1:])
	}
	if name == "" {
		return skills.Skill{}, "", false
	}
	sk, found := l.findSkill(name)
	if !found {
		return skills.Skill{}, "", false
	}
	return sk, args, true
}

// skillModelText builds what the model actually receives when a skill is
// invoked: the skill's whole body, plus whatever the user typed after the
// command name, if anything. The transcript keeps only the short
// "/<name> ..." line the user typed.
func skillModelText(sk skills.Skill, args string) string {
	text := fmt.Sprintf("Follow the %q skill's instructions below to help with my request.\n\n---\n%s\n---", sk.Name, sk.Body)
	if args != "" {
		text += "\n\nMy request: " + args
	}
	return text
}

// matchCustomCommand recognizes "/<name>" or "/<name> <args>" against a
// loaded custom command. Built-in commands (/skill, /init) are checked by
// the caller first, so they always take precedence over a same-named
// custom command.
func (l *Loop) matchCustomCommand(text string) (commands.Command, string, bool) {
	trimmed := strings.TrimSpace(text)
	rest, ok := strings.CutPrefix(trimmed, "/")
	if !ok {
		return commands.Command{}, "", false
	}
	name, args := rest, ""
	if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
		name, args = rest[:idx], strings.TrimSpace(rest[idx+1:])
	}
	for _, c := range l.Commands {
		if c.Name == name {
			return c, args, true
		}
	}
	return commands.Command{}, "", false
}

// sendWithModelText drives one full agent turn. displayText is what gets
// recorded as the message.user event (what the user actually typed);
// modelText is what the model receives as the user turn's content — they
// differ for /skill <name> and custom commands, where the model needs the
// expanded body but the transcript should stay readable. agentOverride and
// modelOverride, if non-empty, apply for this turn only (a custom
// command's "agent"/"model" frontmatter) without changing the session's
// standing agent.
func (l *Loop) sendWithModelText(ctx context.Context, sessionID, agentName, displayText, modelText, agentOverride, modelOverride string) error {
	resolveAgent := agentName
	if agentOverride != "" {
		resolveAgent = agentOverride
	}

	profile, err := l.Config.ResolveProfile(resolveAgent)
	if err != nil {
		return fmt.Errorf("resolve profile for agent %q: %w", resolveAgent, err)
	}
	if modelOverride != "" {
		profile.Model = modelOverride
	}
	p, ok := l.Providers[profile.Provider]
	if !ok {
		return fmt.Errorf("no provider client configured for %q (check Providers map at startup)", profile.Provider)
	}

	maxTokens := profile.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}

	// Per-agent system prompt addition and tool scoping — this is what
	// makes agentName more than just a model choice. An empty AgentConfig
	// (agent not found, or found with no Prompt/Tools set) is a no-op:
	// same behavior as before per-agent config existed.
	agentCfg := l.Config.Agents[resolveAgent]
	systemPrompt := l.SystemPrompt
	if agentCfg.Prompt != "" {
		systemPrompt = systemPrompt + "\n\n" + agentCfg.Prompt
	}

	l.maybeAutoCompact(ctx, sessionID, p, profile, systemPrompt)

	// modelText differs from displayText for /skill <name>, custom
	// commands, and /init — the transcript shows the short command the
	// user typed, but the model needs the expanded body. Persist both so
	// rehydrateHistory can reconstruct the exact message the model saw,
	// not just what's shown on screen.
	userMsgData := map[string]any{"text": displayText}
	if modelText != displayText {
		userMsgData["model_text"] = modelText
	}
	l.Store.Append(sessionID, events.TypeUserMessage, userMsgData)

	l.appendHistory(sessionID, provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Block{provider.TextBlock(modelText)},
	})

	for {
		history := l.history(sessionID)

		req := provider.ChatRequest{
			Model:       profile.Model,
			System:      systemPrompt,
			Messages:    history,
			Tools:       l.Tools.SpecsFor(agentCfg.Tools),
			MaxTokens:   maxTokens,
			Temperature: profile.Temperature,
		}

		stream, err := p.Chat(ctx, req)
		if err != nil {
			l.Store.Append(sessionID, events.TypeError, map[string]any{"error": err.Error()})
			return fmt.Errorf("chat request: %w", err)
		}

		assistantBlocks, toolUses, stopReason, usage, err := l.consumeStream(sessionID, stream)
		if err != nil {
			return err
		}
		if usage.hasUsage {
			l.recordUsage(sessionID, profile.Model, usage)
		}

		l.appendHistory(sessionID, provider.Message{Role: provider.RoleAssistant, Content: assistantBlocks})

		if stopReason != "tool_use" || len(toolUses) == 0 {
			if len(l.Config.Hooks) > 0 {
				// Fire-and-forget: a Stop hook is purely a notification
				// point here (e.g. "ping me when a turn finishes") — its
				// block decision, if any, has no effect, since there's no
				// well-defined "keep going without a new user turn" flow
				// to force.
				hooks.Run(ctx, l.Config.Hooks, hooks.EventStop, map[string]any{"session_id": sessionID})
			}
			return nil
		}

		resultBlocks := l.runTools(ctx, sessionID, toolUses, agentCfg.Tools)
		l.appendHistory(sessionID, provider.Message{Role: provider.RoleUser, Content: resultBlocks})
	}
}

// streamUsage carries the token usage seen while draining one stream, plus
// enough timing information to compute tokens-per-second.
type streamUsage struct {
	hasUsage     bool
	inputTokens  int
	outputTokens int
	elapsed      time.Duration
}

// consumeStream drains one model response, mirroring each piece into the
// session's event log, and returns the assistant's content blocks, any
// tool_use blocks it requested, and whatever token usage the provider
// reported (see provider.EventUsage — not every provider/request reports
// it, hence streamUsage.hasUsage).
func (l *Loop) consumeStream(sessionID string, stream <-chan provider.StreamEvent) (blocks []provider.Block, toolUses []provider.Block, stopReason string, usage streamUsage, err error) {
	start := time.Now()
	var text strings.Builder
	toolNames := map[string]string{}
	toolInputs := map[string]*strings.Builder{}
	var toolOrder []string

	for ev := range stream {
		switch ev.Type {
		case provider.EventTextDelta:
			text.WriteString(ev.TextDelta)
			l.Store.Append(sessionID, events.TypeMessagePartDelta, map[string]any{"text": ev.TextDelta})

		case provider.EventToolUseStart:
			toolNames[ev.ToolUseID] = ev.ToolName
			toolInputs[ev.ToolUseID] = &strings.Builder{}
			toolOrder = append(toolOrder, ev.ToolUseID)
			l.Store.Append(sessionID, events.TypeToolStart, map[string]any{
				"tool_use_id": ev.ToolUseID,
				"name":        ev.ToolName,
			})

		case provider.EventToolUseInputDelta:
			if b, ok := toolInputs[ev.ToolUseID]; ok {
				b.WriteString(ev.InputDelta)
			}

		case provider.EventToolUseEnd:
			input := ev.ToolInput
			if len(input) == 0 {
				if b, ok := toolInputs[ev.ToolUseID]; ok && b.Len() > 0 {
					input = json.RawMessage(b.String())
				} else {
					input = json.RawMessage("{}")
				}
			}
			toolUses = append(toolUses, provider.Block{
				Type:      provider.BlockToolUse,
				ToolUseID: ev.ToolUseID,
				ToolName:  toolNames[ev.ToolUseID],
				ToolInput: input,
			})

		case provider.EventMessageStop:
			stopReason = ev.StopReason

		case provider.EventUsage:
			usage.hasUsage = true
			usage.inputTokens = ev.InputTokens
			usage.outputTokens = ev.OutputTokens

		case provider.EventError:
			l.Store.Append(sessionID, events.TypeError, map[string]any{"error": ev.Err.Error()})
			return nil, nil, "", usage, fmt.Errorf("provider stream error: %w", ev.Err)
		}
	}
	usage.elapsed = time.Since(start)

	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": text.String()})

	if text.Len() > 0 {
		blocks = append(blocks, provider.TextBlock(text.String()))
	}
	blocks = append(blocks, toolUses...)
	return blocks, toolUses, stopReason, usage, nil
}

// runTools executes each requested tool call in order and returns the
// resulting tool_result blocks to feed back to the model. allowedTools, if
// non-empty, is enforced here too (not just in the specs the model saw) —
// a belt-and-suspenders check in case a model calls a tool it wasn't
// offered.
func (l *Loop) runTools(ctx context.Context, sessionID string, toolUses []provider.Block, allowedTools []string) []provider.Block {
	ctx = WithSessionID(ctx, sessionID)
	results := make([]provider.Block, 0, len(toolUses))
	for _, tu := range toolUses {
		var res tools.Result
		if !tools.IsAllowed(allowedTools, tu.ToolName) {
			res = tools.Result{
				Content: fmt.Sprintf("tool %q is not available to this agent", tu.ToolName),
				IsError: true,
			}
		} else {
			res = l.Tools.Call(ctx, tu.ToolName, tu.ToolInput, "")
		}
		l.Store.Append(sessionID, events.TypeToolEnd, map[string]any{
			"tool_use_id": tu.ToolUseID,
			"content":     res.Content,
			"is_error":    res.IsError,
			// input carries this call's arguments (not just its result) —
			// the event log is otherwise the only place a tool_use block's
			// ToolInput would live, and rehydrateHistory needs it to
			// reconstruct the exact message sent to the model after a
			// restart. See rehydrateHistory in loop_rehydrate.go.
			"input": string(tu.ToolInput),
		})
		results = append(results, provider.ToolResultBlock(tu.ToolUseID, res.Content, res.IsError))
	}
	return results
}

// parseSkillCommand recognizes "/skill" and "/skill <name>". ok is false
// for anything else (including a message that merely mentions "/skill" in
// the middle of a sentence).
func parseSkillCommand(text string) (arg string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "/skill" {
		return "", true
	}
	if rest, found := strings.CutPrefix(trimmed, "/skill "); found {
		return strings.TrimSpace(rest), true
	}
	return "", false
}

// listSkills answers "/skill" locally — no model call — with the same
// name/description index that's in the system prompt.
func (l *Loop) listSkills(sessionID, displayText string) error {
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText, "local": true})

	text := "No skills registered."
	if len(l.Skills) > 0 {
		var b strings.Builder
		b.WriteString("Available skills (/<name> to run one):\n")
		for _, s := range l.Skills {
			fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
		}
		text = b.String()
	}

	l.Store.Append(sessionID, events.TypeMessagePartDelta, map[string]any{"text": text})
	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": text})
	return nil
}

// showMemoryInfo answers "/memory" locally — no model call — with the
// auto-memory directory path and current MEMORY.md index content, the
// same information Claude Code's "/memory" command surfaces.
func (l *Loop) showMemoryInfo(sessionID, displayText string) error {
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText, "local": true})

	var text string
	if l.MemoryDir == "" {
		text = "Auto memory is disabled (config.json's \"auto_memory_enabled\": false)."
	} else {
		index := memory.LoadIndex(l.MemoryDir)
		var b strings.Builder
		fmt.Fprintf(&b, "Auto memory directory: %s\n", l.MemoryDir)
		fmt.Fprintf(&b, "Index file: %s\n\n", memory.IndexPath(l.MemoryDir))
		if index == "" {
			b.WriteString("No memory saved yet.")
		} else {
			b.WriteString(index)
		}
		text = b.String()
	}

	l.Store.Append(sessionID, events.TypeMessagePartDelta, map[string]any{"text": text})
	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": text})
	return nil
}

// parseConfigCommand recognizes "/config" and "/config <rest>". ok is
// false for anything else.
func parseConfigCommand(text string) (arg string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "/config" {
		return "", true
	}
	if rest, found := strings.CutPrefix(trimmed, "/config "); found {
		return strings.TrimSpace(rest), true
	}
	return "", false
}

// handleConfigCommand answers "/config" locally — no model call. With no
// argument it reports the current live settings; "/config <setting>
// on|off" toggles auto_compact or show_tps process-wide (every session on
// this daemon, not just the one issuing the command — see
// Loop.autoCompactEnabled/showTPS) and broadcasts an events.TypeConfigChanged
// event so this session's clients update their display immediately.
func (l *Loop) handleConfigCommand(sessionID, displayText, arg string) error {
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText, "local": true})

	fields := strings.Fields(arg)
	var text string

	switch {
	case arg == "":
		text = l.configSummary()

	case len(fields) == 2 && (fields[1] == "on" || fields[1] == "off"):
		enabled := fields[1] == "on"
		switch fields[0] {
		case "auto_compact":
			l.SetAutoCompactEnabled(enabled)
			text = fmt.Sprintf("auto_compact: %s", onOff(enabled))
		case "show_tps":
			l.SetShowTPS(enabled)
			text = fmt.Sprintf("show_tps: %s", onOff(enabled))
		case "auto_delegate":
			l.SetAutoDelegateEnabled(enabled)
			text = fmt.Sprintf("auto_delegate: %s", onOff(enabled))
			// Turning it on without an auto_delegate block configured
			// would silently do nothing, so say so rather than letting the
			// user think it took effect.
			if enabled && l.Config.AutoDelegate == nil {
				text += "\n(no auto_delegate block in config.json, so nothing will be delegated — see docs/USAGE.md)"
			}
		default:
			text = fmt.Sprintf("unknown setting %q. usage: /config, /config auto_compact on|off, /config show_tps on|off, /config auto_delegate on|off", fields[0])
		}
		if text != "" && knownSetting(fields[0]) {
			l.Store.Append(sessionID, events.TypeConfigChanged, map[string]any{
				"auto_compact_enabled": l.AutoCompactEnabled(),
				"show_tps":             l.ShowTPS(),
				"auto_delegate":        l.AutoDelegateEnabled(),
			})
		}

	default:
		text = "usage: /config, /config auto_compact on|off, /config show_tps on|off, /config auto_delegate on|off"
	}

	l.Store.Append(sessionID, events.TypeMessagePartDelta, map[string]any{"text": text})
	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": text})
	return nil
}

func knownSetting(name string) bool {
	switch name {
	case "auto_compact", "show_tps", "auto_delegate":
		return true
	}
	return false
}

func (l *Loop) configSummary() string {
	delegate := onOff(l.AutoDelegateEnabled())
	// The target agent is the useful part of this line — "on" alone
	// doesn't say where prompts are going.
	if cfg := l.Config.AutoDelegate; cfg != nil && cfg.Agent != "" {
		delegate += fmt.Sprintf(" (-> %s)", cfg.Agent)
	} else {
		delegate += " (not configured)"
	}
	return fmt.Sprintf("auto_compact: %s\nshow_tps: %s\nauto_delegate: %s",
		onOff(l.AutoCompactEnabled()), onOff(l.ShowTPS()), delegate)
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// parseCompactCommand recognizes "/compact" and "/compact <instructions>".
// ok is false for anything else.
func parseCompactCommand(text string) (instructions string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "/compact" {
		return "", true
	}
	if rest, found := strings.CutPrefix(trimmed, "/compact "); found {
		return strings.TrimSpace(rest), true
	}
	return "", false
}

// handleCompactCommand runs compaction on demand, regardless of
// AutoCompactEnabled or the usage threshold — unlike maybeAutoCompact,
// this always compacts when invoked. instructions, if given, replaces the
// default summarization prompt (e.g. "/compact focus on the auth
// decisions, drop exploratory dead ends").
func (l *Loop) handleCompactCommand(ctx context.Context, sessionID, agentName, displayText, instructions string) error {
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText, "local": true})

	profile, err := l.Config.ResolveProfile(agentName)
	if err != nil {
		l.Store.Append(sessionID, events.TypeError, map[string]any{"error": err.Error()})
		return nil
	}
	p, ok := l.Providers[profile.Provider]
	if !ok {
		l.Store.Append(sessionID, events.TypeError, map[string]any{
			"error": fmt.Sprintf("no provider client configured for %q", profile.Provider),
		})
		return nil
	}
	agentCfg := l.Config.Agents[agentName]
	systemPrompt := l.SystemPrompt
	if agentCfg.Prompt != "" {
		systemPrompt = systemPrompt + "\n\n" + agentCfg.Prompt
	}

	var text string
	if err := l.compactHistory(ctx, sessionID, p, profile, systemPrompt, instructions, true); err != nil {
		l.Store.Append(sessionID, events.TypeError, map[string]any{"error": fmt.Sprintf("compaction failed: %v", err)})
		return nil
	}
	text = "Conversation compacted."

	l.Store.Append(sessionID, events.TypeMessagePartDelta, map[string]any{"text": text})
	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": text})
	return nil
}

// handleCostCommand answers "/usage" locally — no model call — with a
// per-model breakdown of cumulative token usage for this session (input,
// output, total, number of API calls), plus a grand total. Tokens only,
// deliberately no dollar figures: this project has no per-model pricing
// table to keep in sync, and the raw counts are what the context-window
// math elsewhere in this file already uses.
func (l *Loop) handleCostCommand(sessionID, displayText string) error {
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText, "local": true})

	l.mu.Lock()
	totals := make(map[string]modelTotals, len(l.cumulativeUsage[sessionID]))
	for model, t := range l.cumulativeUsage[sessionID] {
		totals[model] = t
	}
	l.mu.Unlock()

	var text string
	if len(totals) == 0 {
		text = "No usage yet."
	} else {
		models := make([]string, 0, len(totals))
		for m := range totals {
			models = append(models, m)
		}
		sort.Strings(models)

		var b strings.Builder
		b.WriteString("Token usage by model:\n")
		var grandInput, grandOutput, grandCalls int
		for _, m := range models {
			t := totals[m]
			fmt.Fprintf(&b, "- %s: input %d · output %d · total %d (%d calls)\n", m, t.InputTokens, t.OutputTokens, t.InputTokens+t.OutputTokens, t.Calls)
			grandInput += t.InputTokens
			grandOutput += t.OutputTokens
			grandCalls += t.Calls
		}
		fmt.Fprintf(&b, "\nGrand total: input %d · output %d · total %d (%d calls)", grandInput, grandOutput, grandInput+grandOutput, grandCalls)
		text = b.String()
	}

	l.Store.Append(sessionID, events.TypeMessagePartDelta, map[string]any{"text": text})
	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": text})
	return nil
}

func (l *Loop) findSkill(name string) (skills.Skill, bool) {
	for _, s := range l.Skills {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	return skills.Skill{}, false
}

func (l *Loop) skillNames() string {
	names := make([]string, len(l.Skills))
	for i, s := range l.Skills {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

func (l *Loop) appendHistory(sessionID string, msg provider.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages[sessionID] = append(l.messages[sessionID], msg)
}

func (l *Loop) history(sessionID string) []provider.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]provider.Message, len(l.messages[sessionID]))
	copy(out, l.messages[sessionID])
	return out
}

// setHistory replaces sessionID's entire in-memory history — used only by
// auto-compaction to swap in a summary.
func (l *Loop) setHistory(sessionID string, msgs []provider.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages[sessionID] = msgs
}

// recordUsage stores usage as sessionID's latest known token usage (each
// call overwrites, since a provider's input_tokens already reflects the
// full history sent so far — not something to accumulate across calls)
// and appends an events.TypeUsage event so any subscribed client can
// update its context-window/TPS display.
func (l *Loop) recordUsage(sessionID, model string, usage streamUsage) {
	maxContext := modelinfo.MaxContextTokens(model)
	tps := 0.0
	if usage.elapsed > 0 {
		tps = float64(usage.outputTokens) / usage.elapsed.Seconds()
	}

	u := sessionUsage{
		InputTokens:  usage.inputTokens,
		OutputTokens: usage.outputTokens,
		MaxContext:   maxContext,
		TPS:          tps,
	}

	l.mu.Lock()
	l.usage[sessionID] = u
	if l.cumulativeUsage[sessionID] == nil {
		l.cumulativeUsage[sessionID] = map[string]modelTotals{}
	}
	mt := l.cumulativeUsage[sessionID][model]
	mt.InputTokens += usage.inputTokens
	mt.OutputTokens += usage.outputTokens
	mt.Calls++
	l.cumulativeUsage[sessionID][model] = mt
	l.mu.Unlock()

	percent := 0.0
	if maxContext > 0 {
		percent = float64(u.InputTokens+u.OutputTokens) / float64(maxContext) * 100
	}
	l.Store.Append(sessionID, events.TypeUsage, map[string]any{
		"input_tokens":  u.InputTokens,
		"output_tokens": u.OutputTokens,
		"max_context":   u.MaxContext,
		"percent":       percent,
		"tps":           tps,
		"show_tps":      l.ShowTPS(),
		"model":         model,
	})
}

func (l *Loop) getUsage(sessionID string) (sessionUsage, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	u, ok := l.usage[sessionID]
	return u, ok
}

func (l *Loop) clearUsage(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.usage, sessionID)
}

// maybeAutoCompact summarizes sessionID's history in place when
// AutoCompactEnabled is on and the last recorded usage crossed
// compactThresholdPercent — freeing up context space before the next
// user turn is appended. Best-effort: any failure (including the
// summarization call itself erroring) just leaves the full history intact
// rather than blocking the real turn.
func (l *Loop) maybeAutoCompact(ctx context.Context, sessionID string, p provider.Provider, profile config.Profile, systemPrompt string) {
	if !l.AutoCompactEnabled() {
		return
	}
	u, ok := l.getUsage(sessionID)
	if !ok || u.MaxContext <= 0 {
		return
	}
	percent := float64(u.InputTokens+u.OutputTokens) / float64(u.MaxContext) * 100
	if percent < compactThresholdPercent {
		return
	}
	_ = l.compactHistory(ctx, sessionID, p, profile, systemPrompt, "", false)
}

// compactHistory summarizes sessionID's history via the model and, on
// success, replaces the in-memory history with just that summary.
// instructions overrides the default summarization prompt (used by the
// manual "/compact <instructions>" command); empty means use
// compactionPrompt. manual marks the resulting "compacted" event so
// clients/logs can distinguish a user-triggered compaction from an
// automatic one.
func (l *Loop) compactHistory(ctx context.Context, sessionID string, p provider.Provider, profile config.Profile, systemPrompt, instructions string, manual bool) error {
	history := l.history(sessionID)
	if len(history) == 0 {
		return fmt.Errorf("no conversation history to compact")
	}
	if instructions == "" {
		instructions = compactionPrompt
	}

	summaryMessages := make([]provider.Message, len(history), len(history)+1)
	copy(summaryMessages, history)
	summaryMessages = append(summaryMessages, provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Block{provider.TextBlock(instructions)},
	})

	stream, err := p.Chat(ctx, provider.ChatRequest{
		Model:     profile.Model,
		System:    systemPrompt,
		Messages:  summaryMessages,
		MaxTokens: defaultMaxTokens, // a long session's summary can easily overflow a smaller cap
	})
	if err != nil {
		return fmt.Errorf("compaction request: %w", err)
	}
	summary, usage, err := drainText(ctx, stream)
	if err != nil {
		return fmt.Errorf("compaction request: %w", err)
	}
	// The summarization call is billed like any other — fold it into
	// /usage's totals even though it never appears in the transcript.
	if usage.hasUsage {
		l.addCumulativeUsage(sessionID, profile.Model, usage.inputTokens, usage.outputTokens)
	}
	if summary == "" {
		return fmt.Errorf("model returned an empty summary")
	}

	l.setHistory(sessionID, []provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.Block{provider.TextBlock("[Previous conversation was summarized]\n\n" + summary)},
	}})
	l.clearUsage(sessionID)
	// "summary" (not just its length) and the compaction call's own usage
	// are what rehydrateHistory/rehydrateSession need to reconstruct this
	// exact post-compaction state after a restart — see loop_rehydrate.go.
	compactedData := map[string]any{"summary_length": len(summary), "manual": manual, "summary": summary}
	if usage.hasUsage {
		compactedData["model"] = profile.Model
		compactedData["input_tokens"] = usage.inputTokens
		compactedData["output_tokens"] = usage.outputTokens
	}
	l.Store.Append(sessionID, events.TypeCompacted, compactedData)
	return nil
}

// drainText concatenates every text delta from stream and returns the
// final text plus any token usage the provider reported — used for the
// internal compaction call, which must NOT go through consumeStream (that
// would write message.part.delta/end events into the visible transcript,
// making an internal summarization call look like a normal assistant
// reply).
func drainText(ctx context.Context, stream <-chan provider.StreamEvent) (string, streamUsage, error) {
	var text strings.Builder
	var usage streamUsage
	for {
		select {
		case ev, ok := <-stream:
			if !ok {
				return text.String(), usage, nil
			}
			switch ev.Type {
			case provider.EventTextDelta:
				text.WriteString(ev.TextDelta)
			case provider.EventUsage:
				usage.hasUsage = true
				usage.inputTokens = ev.InputTokens
				usage.outputTokens = ev.OutputTokens
			case provider.EventError:
				return "", usage, ev.Err
			}
		case <-ctx.Done():
			return "", usage, ctx.Err()
		}
	}
}

// addCumulativeUsage folds one off-transcript model call (e.g. the
// compaction summarization) into /usage's running totals, without touching
// the latest-usage snapshot or emitting a usage event.
func (l *Loop) addCumulativeUsage(sessionID, model string, inputTokens, outputTokens int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cumulativeUsage[sessionID] == nil {
		l.cumulativeUsage[sessionID] = map[string]modelTotals{}
	}
	mt := l.cumulativeUsage[sessionID][model]
	mt.InputTokens += inputTokens
	mt.OutputTokens += outputTokens
	mt.Calls++
	l.cumulativeUsage[sessionID][model] = mt
}
