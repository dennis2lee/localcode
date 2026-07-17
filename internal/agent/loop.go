// Package agent implements the core agent loop: send a user message, stream
// the model's response into the session's event log, execute any requested
// tool calls, and repeat until the model stops asking for tools.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/skills"
	"localcode/internal/tools"
)

const defaultSystemPrompt = "You are a helpful coding assistant with access to file and shell tools. Use them when needed; otherwise answer directly."

const defaultMaxTokens = 4096

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

	mu       sync.Mutex
	messages map[string][]provider.Message // sessionID -> history
}

func New(store *session.Store, reg *tools.Registry, providers map[string]provider.Provider, cfg *config.Config) *Loop {
	return &Loop{
		Store:        store,
		Tools:        reg,
		Providers:    providers,
		Config:       cfg,
		SystemPrompt: defaultSystemPrompt,
		messages:     map[string][]provider.Message{},
	}
}

// SendMessage appends a user turn to sessionID's history and drives the
// agent loop (model call -> optional tool calls -> model call -> ...) until
// the model produces a final answer. agentName selects which model profile
// to use, per the config's agents map.
func (l *Loop) SendMessage(ctx context.Context, sessionID, agentName, text string) error {
	// /skill lists available skills locally (no model call); /skill <name>
	// splices that skill's full body into what the model sees, so it
	// starts following it immediately instead of the user hoping the
	// model decides to call the Skill tool on its own. Either way the
	// displayed transcript keeps the short "/skill ..." the user typed.
	if arg, ok := parseSkillCommand(text); ok {
		if arg == "" {
			return l.listSkills(sessionID, text)
		}
		sk, found := l.findSkill(arg)
		if !found {
			l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text})
			l.Store.Append(sessionID, events.TypeError, map[string]any{
				"error": fmt.Sprintf("unknown skill %q. Available: %s", arg, l.skillNames()),
			})
			return nil
		}
		return l.sendWithModelText(ctx, sessionID, agentName, text,
			fmt.Sprintf("Follow the %q skill's instructions below to help with my request.\n\n---\n%s\n---", sk.Name, sk.Body))
	}

	return l.sendWithModelText(ctx, sessionID, agentName, text, text)
}

// sendWithModelText drives one full agent turn. displayText is what gets
// recorded as the message.user event (what the user actually typed);
// modelText is what the model receives as the user turn's content — they
// differ for /skill <name>, where the model needs the full skill body but
// the transcript should stay readable.
func (l *Loop) sendWithModelText(ctx context.Context, sessionID, agentName, displayText, modelText string) error {
	profile, err := l.Config.ResolveProfile(agentName)
	if err != nil {
		return fmt.Errorf("resolve profile for agent %q: %w", agentName, err)
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
	agentCfg := l.Config.Agents[agentName]
	systemPrompt := l.SystemPrompt
	if agentCfg.Prompt != "" {
		systemPrompt = systemPrompt + "\n\n" + agentCfg.Prompt
	}

	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText})

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

		assistantBlocks, toolUses, stopReason, err := l.consumeStream(sessionID, stream)
		if err != nil {
			return err
		}

		l.appendHistory(sessionID, provider.Message{Role: provider.RoleAssistant, Content: assistantBlocks})

		if stopReason != "tool_use" || len(toolUses) == 0 {
			return nil
		}

		resultBlocks := l.runTools(ctx, sessionID, toolUses, agentCfg.Tools)
		l.appendHistory(sessionID, provider.Message{Role: provider.RoleUser, Content: resultBlocks})
	}
}

// consumeStream drains one model response, mirroring each piece into the
// session's event log, and returns the assistant's content blocks plus any
// tool_use blocks it requested.
func (l *Loop) consumeStream(sessionID string, stream <-chan provider.StreamEvent) (blocks []provider.Block, toolUses []provider.Block, stopReason string, err error) {
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

		case provider.EventError:
			l.Store.Append(sessionID, events.TypeError, map[string]any{"error": ev.Err.Error()})
			return nil, nil, "", fmt.Errorf("provider stream error: %w", ev.Err)
		}
	}

	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": text.String()})

	if text.Len() > 0 {
		blocks = append(blocks, provider.TextBlock(text.String()))
	}
	blocks = append(blocks, toolUses...)
	return blocks, toolUses, stopReason, nil
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
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText})

	text := "등록된 skill이 없습니다."
	if len(l.Skills) > 0 {
		var b strings.Builder
		b.WriteString("사용 가능한 skill (/skill <이름> 으로 로드):\n")
		for _, s := range l.Skills {
			fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
		}
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
