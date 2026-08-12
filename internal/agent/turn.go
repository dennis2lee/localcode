package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"localcode/internal/events"
	"localcode/internal/hooks"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

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

	// Tokens-per-second describes the turn about to run, not the one
	// before it, so the accumulator starts empty here.
	l.startTurnRate(sessionID)

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

	// One rescue per turn. A second overflow after a successful compaction
	// means the trouble is not the length of the history, and retrying
	// forever would spend the whole window discovering that.
	rescued := false

	for {
		history := l.history(sessionID)
		messages := sendableHistory(history)

		req := provider.ChatRequest{
			Model:    profile.Model,
			System:   systemPrompt,
			Messages: messages,
			Tools:    l.Tools.SpecsFor(agentCfg.Tools),
			// Sized against what is left of the window rather than taken
			// from config as-is. A max_tokens larger than the room
			// remaining is refused by the server as one total that does
			// not fit — see context_budget.go for the arithmetic and the
			// error it produces.
			MaxTokens:   clampMaxTokens(maxTokens, contextWindow(profile), l.inputEstimate(sessionID, systemPrompt, messages)),
			Temperature: profile.Temperature,
		}

		stream, err := p.Chat(ctx, req)
		if err != nil {
			// Too long is a recoverable condition, not an error to hand
			// back: compacting is exactly what the session needs and what
			// the user would have to do by hand anyway. Reported in the
			// transcript either way, so a turn that took a detour through
			// a summary does not look like it simply took a while.
			if isContextOverflow(err) && !rescued {
				rescued = true
				l.Store.Append(sessionID, events.TypeError, map[string]any{
					"error":     "the conversation no longer fits in this model's context window; summarizing it and retrying",
					"recovered": true,
				})
				if cerr := l.compactHistory(ctx, sessionID, p, profile, systemPrompt, "", false); cerr == nil {
					continue
				} else {
					l.Store.Append(sessionID, events.TypeError, map[string]any{
						"error": fmt.Sprintf("could not summarize to recover space: %v", cerr),
					})
				}
			}
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

		// Nothing is appended for a reply that produced nothing. A turn
		// cancelled before the first token gets here with no blocks —
		// the provider's stream goroutine returns on ctx.Done and closes
		// the channel without a terminal event, which reads as a clean
		// finish — and an assistant message with empty content is
		// rejected by name on the next request, for the rest of the
		// session's life. sendableHistory would drop it anyway; not
		// recording it keeps the in-memory history honest, and matches
		// what rehydrateHistory already reconstructs from the log.
		if len(assistantBlocks) > 0 {
			l.appendHistory(sessionID, provider.Message{Role: provider.RoleAssistant, Content: assistantBlocks})
		}

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
		resultBlocks = append(resultBlocks, l.takeInjected(sessionID)...)
		l.appendHistory(sessionID, provider.Message{Role: provider.RoleUser, Content: resultBlocks})
	}
}

// injectedPreface tells the model where the text that follows came from.
// Without it the message arrives in the same user turn as the tool results
// and reads like part of a tool's output.
const injectedPreface = "[The user sent this while you were working — take it into account from here on]\n\n"

// takeInjected collects anything the user typed since this turn started
// and returns it as blocks to travel with the tool results.
//
// Travelling with them, rather than forming a message of its own, because
// the tool results are themselves a user message, and two user messages
// back to back is not a shape every provider accepts — Bedrock's Converse
// API rejects it outright.
//
// Nothing happens here unless PendingInput is wired up (the daemon does
// it); a Loop built without one simply has no mid-turn input.
func (l *Loop) takeInjected(sessionID string) []provider.Block {
	if l.PendingInput == nil {
		return nil
	}
	var out []provider.Block
	for {
		text, ok := l.PendingInput(sessionID)
		if !ok {
			return out
		}
		// Recorded only now, when it actually reaches the model. Writing
		// it at the moment it was typed would put a line in the transcript
		// that the model had not yet been told about — and if the turn
		// ended before the next tool call, it would be answered as an
		// ordinary next message and recorded a second time.
		l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "injected": true})
		out = append(out, provider.TextBlock(injectedPreface+text))
	}
}

// streamUsage carries the token usage seen while draining one stream, plus
// enough timing information to compute tokens-per-second.
//
// elapsed is generation time, not wall-clock time for the request: it runs
// from the first piece of output to the last. Everything before the first
// token — prefill of a long prompt, and on a local server the wait behind
// whatever else is queued — is real waiting, but it is not generation, and
// dividing the output tokens by it reported a rate several times below
// what the model was actually doing. That gap is widest on exactly the
// long-context turns where someone looks at the number.
type streamUsage struct {
	hasUsage     bool
	inputTokens  int
	outputTokens int
	elapsed      time.Duration
}

// tpsTickInterval is how often a live tokens-per-second estimate goes out
// while a model is still generating. A var so a test can shorten it.
var tpsTickInterval = time.Second

// consumeStream drains one model response, mirroring each piece into the
// session's event log, and returns the assistant's content blocks, any
// tool_use blocks it requested, and whatever token usage the provider
// reported (see provider.EventUsage — not every provider/request reports
// it, hence streamUsage.hasUsage).
func (l *Loop) consumeStream(sessionID string, stream <-chan provider.StreamEvent) (blocks []provider.Block, toolUses []provider.Block, stopReason string, usage streamUsage, err error) {
	var text strings.Builder
	toolNames := map[string]string{}
	toolInputs := map[string]*strings.Builder{}

	// Generation timing, and the live rate estimate built on top of it.
	// deltas counts stream deltas, not tokens — the authoritative token
	// count only arrives with the provider's usage report at the end of
	// the stream, and the whole point of a live figure is to exist before
	// then. For the local servers this is aimed at (llama.cpp, Ollama,
	// vLLM) a delta is one token, so the estimate is close; for providers
	// that batch several tokens per delta it reads low. It is shown with
	// a "~" for that reason, and is replaced by the real number the
	// moment the stream ends.
	var genStart, lastTick time.Time
	deltas := 0
	generated := func() {
		if genStart.IsZero() {
			genStart = time.Now()
			lastTick = genStart
		}
		deltas++
		if time.Since(lastTick) < tpsTickInterval {
			return
		}
		lastTick = time.Now()
		if secs := time.Since(genStart).Seconds(); secs > 0 {
			l.Store.Broadcast(sessionID, events.TypeUsage, map[string]any{
				"tps":       float64(deltas) / secs,
				"estimated": true,
				"show_tps":  l.ShowTPS(),
			})
		}
	}

	for ev := range stream {
		switch ev.Type {
		case provider.EventTextDelta:
			text.WriteString(ev.TextDelta)
			l.Store.Append(sessionID, events.TypeMessagePartDelta, map[string]any{"text": ev.TextDelta})
			generated()

		case provider.EventToolUseStart:
			toolNames[ev.ToolUseID] = ev.ToolName
			toolInputs[ev.ToolUseID] = &strings.Builder{}

		case provider.EventToolUseInputDelta:
			if b, ok := toolInputs[ev.ToolUseID]; ok {
				b.WriteString(ev.InputDelta)
			}
			generated()

		case provider.EventToolUseEnd:
			input := ev.ToolInput
			if len(input) == 0 {
				if b, ok := toolInputs[ev.ToolUseID]; ok && b.Len() > 0 {
					input = json.RawMessage(b.String())
				} else {
					input = json.RawMessage("{}")
				}
			}
			// tool.start is emitted here rather than at ToolUseStart so it
			// can carry the arguments: they stream in one fragment at a
			// time and are only complete now. This still lands before
			// message.part.end, which is what rehydrateHistory pairs it
			// against, and it is closer to when the tool actually runs —
			// nothing executes until the whole stream has been drained.
			l.Store.Append(sessionID, events.TypeToolStart, map[string]any{
				"tool_use_id": ev.ToolUseID,
				"name":        toolNames[ev.ToolUseID],
				"input":       string(input),
			})
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
	// A rate needs a span to measure across, and one delta is a point.
	// Providers that deliver a short reply in a single chunk would
	// otherwise divide the whole output by the microseconds between
	// receiving it and the stream closing, and report six-figure
	// tokens-per-second. Leaving elapsed at zero says "not measurable
	// here", and recordUsage keeps the last figure it did measure.
	if deltas >= 2 {
		usage.elapsed = time.Since(genStart)
	}

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
		// A model can ask for several tools in one step, and Esc during the
		// first should not be followed by the other four running anyway.
		// Each still gets a result block, because a tool_use with no
		// tool_result is a history the provider rejects — and this history
		// is what a later /compact or a restart replays.
		if err := ctx.Err(); err != nil {
			res = tools.Result{Content: "not run: the turn was cancelled", IsError: true}
			l.Store.Append(sessionID, events.TypeToolEnd, map[string]any{
				"tool_use_id": tu.ToolUseID,
				"content":     res.Content,
				"is_error":    true,
				"input":       string(tu.ToolInput),
			})
			results = append(results, provider.ToolResultBlock(tu.ToolUseID, res.Content, true))
			continue
		}
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
