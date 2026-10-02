package agent

import (
	"localcode/internal/events"
	"localcode/internal/provider"
)

// injectedPreface tells the model where the text that follows came from.
// Without it the message arrives in the same user turn as the tool results
// and reads like part of a tool's output.
// maxRescues bounds how many times one turn will recover from a refused
// request before giving up. The first is a summary; the rest are forced
// trims, each aiming at two thirds of what the last one measured, so five
// of them reach about a fifth of where the conversation started. Bounded
// rather than open-ended because a server that phrases some other refusal
// like an overflow must not become an endless retry.
const maxRescues = 5

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
		l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{
			"text": text, "injected": true, "source": "injected.user",
		})
		// Tagged, because of where it lands. This is the person typing,
		// which is the most instruction-authoritative thing a request
		// carries, and it travels inside a user-role message otherwise
		// made entirely of tool results, which is the least. The role
		// says the same word for both; the tag is what tells them
		// apart, and the preface is localcode's own framing rather than
		// the person's, so only what they typed is inside the span.
		out = append(out, injectedUserBlock(text))
	}
}

// injectedUserBlock is what the person typed while a turn was running,
// as the model is handed it: localcode's preface, then their words, the
// span of their words tagged as theirs. Built here for the live turn and
// for rehydrateHistory, so a restored session carries the same tag: a
// rebuilt bare text block made the person's instruction read as
// unattributed text inside tool output, and dropped the line a compaction
// writes about it.
func injectedUserBlock(text string) provider.Block {
	body := injectedPreface + text
	return provider.Block{
		Type: provider.BlockText, Text: body, Source: "injected.user",
		Sources: []provider.BlockSource{{
			ID: "injected.user", From: len(injectedPreface), To: len(body),
		}},
	}
}
