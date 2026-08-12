package agent

import (
	"errors"
	"strings"

	"localcode/internal/config"
	"localcode/internal/modelinfo"
	"localcode/internal/provider"
)

// This file is about one failure, which looks like this:
//
//	Error: openai-compat endpoint returned 400: This model's maximum
//	context length is 131072 tokens. However, you requested 64000 output
//	tokens and your prompt contains at least 67073 input tokens, for a
//	total of at least 131073 tokens.
//
// Note the arithmetic. 67073 input tokens is 51% of the window, and the
// context meter said 53% — nowhere near the 80% that triggers
// auto-compaction. Nothing was going to save this session, because the
// thing that overflowed was not the history: it was the history *plus the
// output the request reserved room for*. A max_tokens of 64000 against a
// 131072-token window means every turn is refused from 67k of history
// onwards, and the meter reads half full while it happens.
//
// So there are two jobs here. Ask for an amount of output that fits
// (clampMaxTokens), and when a request is refused anyway — a wrong window
// size, a provider counting differently — recover instead of ending the
// turn (isContextOverflow, and the retry in sendWithModelText).

// contextHeadroom is held back from the window when sizing a request.
//
// Every token count on this side is an estimate: the tokenizer is the
// server's, tools and the system prompt are counted differently by
// different providers, and a chat template adds framing of its own. The
// margin is what keeps a small underestimate from turning into a refused
// request, at the price of a slightly shorter reply.
const contextHeadroom = 2048

// minOutputTokens is the floor clampMaxTokens will not go below.
//
// A reply capped at a few dozen tokens is not a reply, and a session that
// has genuinely no room left should be compacted rather than answered in
// fragments — so the floor is deliberately high enough that hitting it
// means "compact", not "carry on".
const minOutputTokens = 1024

// contextWindow is the model's total input+output budget: what the config
// says, or a guess from the model name.
func contextWindow(profile config.Profile) int {
	if profile.ContextWindow > 0 {
		return profile.ContextWindow
	}
	return modelinfo.MaxContextTokens(profile.Model)
}

// clampMaxTokens returns how much output to ask for so that the request as
// a whole fits inside the window.
//
// Returns want unchanged when it already fits, or when there is no usable
// window figure — this must never be the reason a request gets *smaller*
// than what was configured for no reason.
func clampMaxTokens(want, window, inputTokens int) int {
	if window <= 0 || want <= 0 {
		return want
	}
	room := window - inputTokens - contextHeadroom
	if room >= want {
		return want
	}
	if room < minOutputTokens {
		return minOutputTokens
	}
	return room
}

// estimateTokens approximates how many tokens a request's input will cost.
//
// Four characters per token is the usual rule of thumb for English and is
// generous for code; it is wrong in both directions for CJK, where a
// character is often a token on its own. It does not need to be right — it
// needs to be close enough to size a reply against, and it is only
// consulted when the provider has not yet reported a real number for this
// session (see Loop.inputEstimate).
func estimateTokens(system string, msgs []provider.Message) int {
	n := len(system)
	for _, m := range msgs {
		for _, b := range m.Content {
			n += len(b.Text) + len(b.ToolInput) + len(b.ToolResultContent)
			// Per-block framing: role, type, ids. Small, but there can be
			// thousands of blocks in a long session.
			n += 16
		}
	}
	return n / 4
}

// inputEstimate is how many input tokens the next request will cost.
//
// The provider's own count from the last exchange, when there is one:
// it is the same tokenizer that will refuse the next request, so it beats
// any estimate made here. Falls back to counting characters for the first
// turn of a session, where there is nothing to go on yet.
func (l *Loop) inputEstimate(sessionID, system string, msgs []provider.Message) int {
	if u, ok := l.getUsage(sessionID); ok && u.InputTokens > 0 {
		// Plus this turn's additions, which the last report predates.
		return u.InputTokens + u.OutputTokens
	}
	return estimateTokens(system, msgs)
}

// overflowPhrases are how the providers say "this did not fit".
//
// There is no status code or error type for it — Anthropic, Bedrock and
// every openai-compatible server phrase it differently, and the local
// servers wrap it in whatever their proxy says. Matching on text is
// unpleasant and it is what there is; a phrase that stops matching costs a
// missed recovery, not a wrong one, because every one of these is checked
// against an error that has already failed.
var overflowPhrases = []string{
	"context length",
	"context window",
	"contextwindowexceeded",
	"maximum context",
	"prompt is too long",
	"too many tokens",
	"exceed context limit",
	"reduce the length of the",
	"input length and `max_tokens`",
}

// isContextOverflow reports whether err is the provider refusing a request
// for being too big — as opposed to any of the other things a 400 can be.
func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, p := range overflowPhrases {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// errNoRoom is returned when a history cannot be trimmed to fit at all.
var errNoRoom = errors.New("the conversation does not fit in this model's context window even after trimming")

// fitHistory drops whole messages from the front of a history until an
// estimate of what is left fits in budget, and reports how many it
// dropped.
//
// Used only by compaction, and that is the point: compaction is what
// rescues a session that has run out of room, and it worked by sending the
// entire history — the very thing that did not fit — to be summarized. So
// the one operation that could save an overflowing session was the one
// operation guaranteed to be refused by it.
//
// From the front, because the recent end is what a summary most needs to
// carry forward, and because anything old enough to be dropped here has
// usually been through a previous compaction already. The caller says so
// in the summarization prompt rather than letting the model believe the
// conversation began where the trim starts.
//
// A trailing tool_result whose tool_use has been dropped is not left
// behind: providers reject that pairing outright, and a rescue path that
// produces an invalid request has rescued nothing.
func fitHistory(system string, msgs []provider.Message, budget int) ([]provider.Message, int, error) {
	if budget <= 0 {
		return msgs, 0, errNoRoom
	}
	start := 0
	for start < len(msgs) {
		if estimateTokens(system, msgs[start:]) <= budget {
			break
		}
		start++
	}
	if start >= len(msgs) {
		return nil, 0, errNoRoom
	}
	// Never open on orphaned tool results: their tool_use blocks are in a
	// message that has just been dropped.
	for start < len(msgs) && startsWithToolResult(msgs[start]) {
		start++
	}
	if start >= len(msgs) {
		return nil, 0, errNoRoom
	}
	return msgs[start:], start, nil
}

func startsWithToolResult(m provider.Message) bool {
	for _, b := range m.Content {
		if b.Type == provider.BlockToolResult {
			return true
		}
	}
	return false
}
