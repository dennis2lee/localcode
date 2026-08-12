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

// maxCompactAttempts bounds how many times the summarization request is
// shrunk and re-sent after being refused for being too long. Each attempt
// aims at two thirds of the last measured size, so this is a deep cut, not
// a long wait — and a bound rather than a loop, because a server refusing
// for some other reason it happens to phrase like an overflow must not
// turn into an endless retry.
//
// Six, because the requests being retried are already small: two thirds
// compounded six times is a fifteenth of where it started, which covers a
// tokenizer disagreeing by 4x with room to spare. Stopping at four left a
// stress case still being refused on the last attempt.
const maxCompactAttempts = 6

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
	window := l.contextWindow(ctx, profile)
	budget := window - defaultMaxTokens - contextHeadroom

	// And shrunk again for each refusal, rather than giving up on the
	// first.
	//
	// Sizing this from the estimate and sending it once meant /compact
	// failed for being too long — the one failure it cannot have, since
	// being too long is why someone ran it. The estimate is about four
	// times too generous for Korean and Japanese, so the server refusing
	// what this side measures as small is not an edge case there, it is
	// the normal case. Each retry aims at two thirds of what the history
	// actually measures, so it converges on a size the server accepts
	// instead of arguing with it.
	var summary string
	var usage streamUsage
	for attempt := 0; ; attempt++ {
		kept, dropped, ferr := fitHistory(systemPrompt, history, budget)
		if ferr != nil {
			// Dropping whole messages cannot get there: one of them is
			// too big by itself. forceFit cuts into them, which is worse
			// than dropping and better than not compacting at all.
			kept, _ = forceFit(systemPrompt, history, budget)
			dropped = len(history) - len(kept)
		}

		// Built fresh each attempt: appending to instructions in place
		// would stack a note per retry.
		prompt := instructions
		if dropped > 0 {
			prompt = fmt.Sprintf(
				"%s\n\n(Note: the earliest %d messages of this conversation were too long to include here and are not shown. Summarize what you can see, and say that earlier context was dropped.)",
				instructions, dropped)
		}

		summaryMessages := make([]provider.Message, len(kept), len(kept)+1)
		copy(summaryMessages, kept)
		summaryMessages = append(summaryMessages, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Block{provider.TextBlock(prompt)},
		})

		// Normalized like any other request: history routinely ends with a
		// user-role message (tool results are one), and appending the
		// instructions after it made two in a row — so on Bedrock the
		// compaction call itself failed, which is the one call that must
		// not, since it is what rescues a session that has run out of
		// context.
		stream, err := p.Chat(ctx, provider.ChatRequest{
			Model:     profile.Model,
			System:    systemPrompt,
			Messages:  sendableHistory(summaryMessages),
			MaxTokens: defaultMaxTokens, // a long session's summary can easily overflow a smaller cap
		})
		if err == nil {
			summary, usage, err = drainText(ctx, stream)
		}
		if err == nil {
			break
		}
		if !isContextOverflow(err) || attempt >= maxCompactAttempts || budget <= 0 {
			return fmt.Errorf("compaction request: %w", err)
		}
		budget = shrinkBudget(budget, systemPrompt, kept)
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
