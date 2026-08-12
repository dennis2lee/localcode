package agent

import (
	"context"
	"fmt"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
)

// compactThresholdPercent is the context-window fill percentage that
// triggers auto-compaction (when Loop.AutoCompactEnabled is true).
const compactThresholdPercent = 80.0

// compactionPrompt asks the model to summarize the conversation so far in
// place of running any tools — deliberately sent as a bare Chat call (see
// drainText), not through the normal turn machinery, so it never appears
// in the visible transcript as an ordinary assistant reply.
const compactionPrompt = "Summarize our conversation so far concisely, preserving important facts, decisions, file paths, and outstanding tasks needed for continuity. Output ONLY the summary, with no preamble."

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

	// Trimmed to fit before anything else.
	//
	// Compaction is what rescues a session that has run out of context, and
	// it used to do that by sending the whole history — the one thing
	// already known not to fit. So /compact answered "compaction failed:
	// ... maximum context length is N tokens" in precisely the situation it
	// exists for, and auto-compaction failed silently the same way.
	//
	// Whatever had to be dropped is said out loud in the instructions, so
	// the model summarizes what it was given rather than asserting the
	// conversation began there.
	window := contextWindow(profile)
	budget := window - defaultMaxTokens - contextHeadroom
	kept, dropped, err := fitHistory(systemPrompt, history, budget)
	if err != nil {
		return fmt.Errorf("compaction request: %w", err)
	}
	if dropped > 0 {
		instructions = fmt.Sprintf(
			"%s\n\n(Note: the earliest %d messages of this conversation were too long to include here and are not shown. Summarize what you can see, and say that earlier context was dropped.)",
			instructions, dropped)
	}

	summaryMessages := make([]provider.Message, len(kept), len(kept)+1)
	copy(summaryMessages, kept)
	summaryMessages = append(summaryMessages, provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Block{provider.TextBlock(instructions)},
	})

	// Normalized like any other request: history routinely ends with a
	// user-role message (tool results are one), and appending the
	// instructions after it made two in a row — so on Bedrock the
	// compaction call itself failed, which is the one call that must not,
	// since it is what rescues a session that has run out of context.
	stream, err := p.Chat(ctx, provider.ChatRequest{
		Model:     profile.Model,
		System:    systemPrompt,
		Messages:  sendableHistory(summaryMessages),
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
	// are what rehydrateHistory/RehydrateSession need to reconstruct this
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
