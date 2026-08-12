package agent

import (
	"errors"
	"fmt"
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

// charsPerToken is the ratio estimateTokens works in, named here because
// the truncation below has to convert a token budget back into bytes.
const charsPerToken = 4

// toolResultWindowShare is the largest share of the context window one
// tool result is allowed to occupy: a quarter.
//
// A tool result is the one thing in a conversation whose size nobody
// chose. read_file on a 5MB file put 5MB into the history, and bash
// returns whatever the command printed — so a single `cat` of a log could
// exceed the whole window by itself, in one message, which no amount of
// summarizing or dropping older messages can fix. That was the failure
// with no way out: the history could not be made to fit because the thing
// that did not fit was one message inside it.
//
// A quarter leaves room for the conversation the result is supposed to be
// part of. Above that, the result is not context, it is the whole context.
const toolResultWindowShare = 4

// truncateMiddle cuts text down to maxBytes, keeping the start and the end
// and saying in the middle what it removed.
//
// Both ends, because which one matters depends on the tool: the head of a
// file is its imports and structure, and the tail of a command is its
// error and exit status. Keeping only one end reliably discards the point
// of about half of all calls.
//
// note is addressed to the model, since the model is who has to decide
// what to do about it.
func truncateMiddle(text string, maxBytes int, note string) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	marker := fmt.Sprintf("\n\n... [%d bytes omitted — %s] ...\n\n", len(text)-maxBytes, note)
	if len(marker) >= maxBytes {
		// Pathological budget: no room for both ends and the explanation.
		// The explanation wins, because a silent truncation is worse than
		// a short one.
		return marker
	}
	room := maxBytes - len(marker)
	head := room * 3 / 5
	tail := room - head
	return text[:head] + marker + text[len(text)-tail:]
}

// capToolResult limits one tool result to a share of the window.
//
// Applied to what is stored as well as what is sent, so the transcript and
// the model see the same thing — and so the event log, the SSE stream to
// every attached client, and the replay after a restart are not each
// carrying a copy of a file that nothing was ever going to be able to use.
func capToolResult(content string, window int) string {
	if window <= 0 {
		window = modelinfo.DefaultMaxContextTokens
	}
	maxBytes := (window / toolResultWindowShare) * charsPerToken
	return truncateMiddle(content, maxBytes,
		"this result was too large for the model's context window; read the file in ranges, or narrow the command, to see the rest")
}

// forceFit makes a history fit budget, whatever it takes, and reports
// whether it had to change anything.
//
// This is the last line of defence, and unlike fitHistory it cannot fail:
// it drops whole messages from the front first, and when what remains
// still does not fit — one enormous message, the case dropping cannot
// solve — it truncates the text inside the messages that are left.
//
// Something being cut is not in question by the time this runs; the
// request has already been refused. The choice is between cutting and a
// session that answers nothing ever again, and a session that keeps
// working with a gap in it, clearly marked, is worth more than a correct
// refusal.
func forceFit(system string, msgs []provider.Message, budget int) ([]provider.Message, bool) {
	if budget <= 0 || len(msgs) == 0 {
		return msgs, false
	}
	if estimateTokens(system, msgs) <= budget {
		return msgs, false
	}

	// Drop from the front while there is more than one message left. The
	// last message is kept whatever its size — it is the request being
	// answered — and truncated below if it is what overflowed.
	start := 0
	for start < len(msgs)-1 && estimateTokens(system, msgs[start:]) > budget {
		start++
	}
	for start < len(msgs)-1 && startsWithToolResult(msgs[start]) {
		start++
	}
	kept := append([]provider.Message(nil), msgs[start:]...)

	if estimateTokens(system, kept) <= budget {
		return kept, true
	}

	// Still too big: the content itself is. Truncate the largest block in
	// the largest message and repeat, so a conversation of ordinary
	// messages plus one monster loses only the monster.
	for range 64 {
		if estimateTokens(system, kept) <= budget {
			break
		}
		mi, bi, size := -1, -1, 0
		for i := range kept {
			for j, b := range kept[i].Content {
				if n := len(b.Text) + len(b.ToolResultContent); n > size {
					mi, bi, size = i, j, n
				}
			}
		}
		if mi < 0 || size == 0 {
			break
		}
		// Copy before writing: history blocks are shared with the session's
		// stored history, and this must not edit it in place.
		blocks := append([]provider.Block(nil), kept[mi].Content...)
		target := size / 2
		if target < 512 {
			target = 512
		}
		const note = "cut to fit the model's context window"
		blocks[bi].Text = truncateMiddle(blocks[bi].Text, target, note)
		blocks[bi].ToolResultContent = truncateMiddle(blocks[bi].ToolResultContent, target, note)
		kept[mi].Content = blocks
	}
	return kept, true
}

func startsWithToolResult(m provider.Message) bool {
	for _, b := range m.Content {
		if b.Type == provider.BlockToolResult {
			return true
		}
	}
	return false
}
