package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"localcode/internal/events"
	"localcode/internal/provider"
)

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
	// What the provider's prompt cache did for this request, where it
	// says. Not folded into inputTokens: they are priced differently, and
	// the read count is the only way to tell a working cache breakpoint
	// from one that is silently doing nothing.
	cacheRead  int
	cacheWrite int
	elapsed    time.Duration
}

// tpsTickInterval is how often a live tokens-per-second estimate goes out
// while a model is still generating. A var so a test can shorten it.
var tpsTickInterval = time.Second

// consumeStream drains one model response, mirroring each piece into the
// session's event log, and returns the assistant's content blocks, any
// tool_use blocks it requested, and whatever token usage the provider
// reported (see provider.EventUsage — not every provider/request reports
// it, hence streamUsage.hasUsage).
//
// fold says how the clients are to draw this stream's reasoning; see
// foldsThinking. It is decided once per request, by the caller, because
// the model is the caller's to know.
func (l *Loop) consumeStream(sessionID string, stream <-chan provider.StreamEvent, fold bool) (blocks []provider.Block, toolUses []provider.Block, stopReason string, usage streamUsage, err error) {
	var text strings.Builder
	toolNames := map[string]string{}
	toolInputs := map[string]*strings.Builder{}
	// The model's reasoning, kept for the message it belongs to. It goes
	// in front of the answer and the tool calls, which is the order the
	// API requires of a continuation.
	var thinking []provider.Block
	// When the reasoning block being streamed began, for the time its
	// label reports, and what it has said so far. Zero and empty between
	// blocks.
	var thinkingSince time.Time
	var thinkingText strings.Builder
	// closeThinking ends the reasoning block being streamed, and reports
	// its time, or -1 when no block was open. A fold block (see
	// foldsThinking) is written to the log here as one thinking.block, so
	// a client that reloads or reconnects draws it folded again rather
	// than losing it: its text, whole, and its time. Called at the
	// block's end, and when the stream stops without one (a provider
	// error, a stop) so what was shown live is what a reload shows.
	//
	// Written to the record and never to the history: rehydrateHistory
	// has no case for it, so a restarted session sends the model no more
	// reasoning than a live one does. Whitespace alone is not a block;
	// neither client draws one.
	closeThinking := func(endText string) int {
		if thinkingSince.IsZero() {
			return -1
		}
		elapsed := int(time.Since(thinkingSince).Milliseconds())
		said := thinkingText.String()
		if said == "" {
			said = endText
		}
		if fold && strings.TrimSpace(said) != "" {
			l.Store.Append(sessionID, events.TypeThinkingBlock, map[string]any{
				"text":       said,
				"elapsed_ms": elapsed,
			})
		}
		thinkingSince = time.Time{}
		thinkingText.Reset()
		return elapsed
	}

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

		case provider.EventThinkingDelta:
			// Broadcast, not appended: a delta is worth watching while it
			// happens, and the record keeps a fold block whole when it
			// ends rather than in fragments (see closeThinking). The API
			// does not want reasoning back on a later turn either, and
			// the block that does have to go back is carried in memory
			// for exactly as long as that is true — see EventThinkingEnd
			// and toAnthropicMessages.
			if thinkingSince.IsZero() {
				thinkingSince = time.Now()
			}
			thinkingText.WriteString(ev.ThinkingDelta)
			l.Store.Broadcast(sessionID, events.TypeThinkingDelta, map[string]any{
				"text":          ev.ThinkingDelta,
				"fold":          fold,
				"show_thinking": l.ShowThinking(),
			})
			generated()

		case provider.EventThinkingEnd:
			// First in the message, before any text or tool_use: the API
			// requires that order, and a continuation whose thinking
			// arrives after the tool call it explains is refused.
			thinking = append(thinking, provider.Block{
				Type: provider.BlockThinking, Text: ev.ThinkingDelta, Signature: ev.Signature,
			})
			// The logged block first and the broadcast end after it: a
			// client folds the live block on thinking.block, with the
			// whole text, and the end then finds nothing open. A client
			// from before thinking.block ignores it and folds on the end,
			// as it always did.
			end := map[string]any{"fold": fold}
			if elapsed := closeThinking(ev.ThinkingDelta); elapsed >= 0 {
				end["elapsed_ms"] = elapsed
			}
			l.Store.Broadcast(sessionID, events.TypeThinkingEnd, end)

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
			usage.cacheRead = ev.CacheReadTokens
			usage.cacheWrite = ev.CacheWriteTokens

		case provider.EventError:
			// The answer so far is closed as a message before the error
			// is recorded, in that order because that is the order it
			// happened in.
			//
			// Without it the reply reaches the log as deltas and no
			// message.part.end, and an unterminated reply is one the
			// replay filter later deletes: collapseFinishedDeltas drops
			// every delta lying before the last part.end in the range, so
			// the moment any later message completes, the half-written
			// answer stops being drawn by anything. The bytes stay in the
			// log and no client ever reads them again.
			//
			// This is the record, which is a different question from the
			// history — that stays as it was, since a failed response is
			// not a turn and must not be sent back as one. The model did
			// say these words, and the session is where that is kept.
			//
			// Marked failed so the record and the history stay two
			// questions after a restart too. rehydrateHistory rebuilds
			// the history from these events, and a part.end with nothing
			// to tell it apart from a finished reply was rebuilt as one:
			// the half answer this turn refused to send back went out on
			// the next request of every restarted session whose log held
			// one.
			//
			// Closed whenever anything of the reply reached the record,
			// not only text: a stream that died after the model's tool
			// call and before any text has that call's tool.start on the
			// record already, and with no part.end to close it a replay
			// kept waiting for the call's result and folded the next
			// turn's iterations into it.
			// A reasoning block the error cut off is recorded as far as
			// it got, as it was shown.
			closeThinking("")
			if text.Len() > 0 || len(toolUses) > 0 {
				l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": text.String(), "failed": true})
			}
			l.Store.Append(sessionID, events.TypeError, map[string]any{"error": ev.Err.Error()})
			// Whatever had already been said comes back with the error.
			// Nothing is appended to the history from it — a failed
			// response is not a turn — but the caller has to be able to
			// tell a stream that died silently from one that died
			// half-way through an answer, because only the first can be
			// asked again somewhere else. See firstOutput.
			if text.Len() > 0 {
				blocks = append(blocks, provider.TextBlock(text.String()))
			}
			return blocks, toolUses, stopReason, usage, fmt.Errorf("provider stream error: %w", ev.Err)
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

	// A stream that closed with a reasoning block still open, a stop
	// pressed mid-thought among them, records it as far as it got.
	closeThinking("")
	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": text.String()})

	blocks = append(blocks, thinking...)
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
// runTools executes one batch of tool calls and returns the result blocks,
// plus whether any of them was refused rather than run — a deny rule, a
// blocking hook, or the person at the keyboard clicking Deny. See
// keepGoing for what that second value decides.

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// drainText concatenates every text delta from stream and returns the
// final text plus any token usage the provider reported — used for the
// internal compaction call, which must NOT go through consumeStream (that
// would write message.part.delta/end events into the visible transcript,
// making an internal summarization call look like a normal assistant
// reply).
func drainText(ctx context.Context, stream <-chan provider.StreamEvent) (string, streamUsage, string, error) {
	var text strings.Builder
	var usage streamUsage
	// Kept, because a utility call ends for the same reasons a
	// conversational one does and the difference matters most here: a
	// summary cut off at max_tokens is a summary that lost the end of
	// the conversation, and a trace that did not record why the call
	// stopped could not tell that from a short answer. It was dropped
	// on this path while the turn loop kept it, which made the one
	// call whose truncation is silent also the one nobody could see.
	var stop string
	for {
		select {
		case ev, ok := <-stream:
			if !ok {
				return text.String(), usage, stop, nil
			}
			switch ev.Type {
			case provider.EventTextDelta:
				text.WriteString(ev.TextDelta)
			case provider.EventUsage:
				// The cache fields too: the summarization call sends the
				// same system prompt and history a turn does, so a working
				// cache serves most of it, and those tokens are billed.
				usage.hasUsage = true
				usage.inputTokens = ev.InputTokens
				usage.outputTokens = ev.OutputTokens
				usage.cacheRead = ev.CacheReadTokens
				usage.cacheWrite = ev.CacheWriteTokens
			case provider.EventMessageStop:
				if ev.StopReason != "" {
					stop = ev.StopReason
				}
			case provider.EventError:
				return "", usage, stop, ev.Err
			}
		case <-ctx.Done():
			return "", usage, stop, ctx.Err()
		}
	}
}
