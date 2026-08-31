package agent

import (
	"encoding/json"

	"localcode/internal/events"
	"localcode/internal/provider"
)

// RehydrateAll restores every session's in-memory conversation history and
// token usage totals from its persisted event log — the piece that used to
// be missing after a daemon restart: the event log survived on disk, but
// Loop.messages/usage/cumulativeUsage are process memory and started
// empty, so a resumed session looked fine in the transcript (event replay)
// but the model had amnesia on the very next turn. Call once at startup,
// after sessions have been loaded via session.LoadAllFromDisk.
func (l *Loop) RehydrateAll() {
	for _, s := range l.Store.AllSessions() {
		// An archived conversation is not replayed. Archiving releases the
		// history it was holding, and rebuilding it at every restart would
		// hand it straight back: a shelf that costs the same as the shelf
		// being empty is not one. Retrieving replays it.
		//
		// Only the conversation itself. Its task children are visible:false
		// and carry no flag of their own, so they are still replayed;
		// narrowing that would mean a parent walk per session at startup.
		if s.ArchivedAt != nil {
			continue
		}
		l.RehydrateSession(s.ID)
	}
}

// RehydrateSession restores one session's model history and usage totals
// from its event log. Used at startup by RehydrateAll, and by a fork,
// which writes a copy of another session's log and then needs the model
// to actually remember it.
//
// Best-effort: a session with no events, or one whose log can't be read,
// is simply left with empty state (same as a brand-new session) rather
// than erroring — a stale or corrupt log for one session shouldn't block
// the daemon from starting.
func (l *Loop) RehydrateSession(sessionID string) {
	evs, err := l.Store.Events(sessionID, 0)
	if err != nil || len(evs) == 0 {
		return
	}

	if history := rehydrateHistory(evs); len(history) > 0 {
		l.setHistory(sessionID, history)
	}

	latest, haveUsage, cum := rehydrateUsage(evs)

	l.mu.Lock()
	if haveUsage {
		l.usage[sessionID] = latest
	}
	if len(cum) > 0 {
		l.cumulativeUsage[sessionID] = cum
	}
	l.mu.Unlock()
}

// rehydrateHistory replays a session's event log into the same
// []provider.Message shape sendWithModelText/compactHistory would have
// built live, so a restarted daemon can continue the conversation with
// full context instead of the model seeing an empty history.
//
// The event log records the transcript at a finer grain than the message
// list (individual text deltas, tool start/end pairs) — this reassembles
// one Message per "turn" the same way the original turn loop produced
// them: a user message, then for each provider.Chat iteration inside that
// turn, one assistant message (text + any tool_use blocks) followed by one
// user message of tool_result blocks, repeating until the iteration that
// ended the turn (no tool use).
//
// A TypeCompacted event with a "summary" resets history to just the
// summary message, exactly like compactHistory does live. Older logs
// written before compaction persisted its summary text (pre-v0.12) have no
// "summary" field — that marker is simply skipped, leaving the fuller
// pre-compaction history rehydrated instead of the shorter summary. Wastes
// some context on the next turn, but never a hard failure.
func rehydrateHistory(evs []events.Event) []provider.Message {
	var out []provider.Message

	// Per-iteration accumulator, reset by flush.
	var pendingText string
	var textSet bool
	var pendingToolOrder []string              // tool_use_ids started this iteration, in order
	toolName := map[string]string{}            // tool_use_id -> name, persists across the whole log
	toolInputs := map[string]string{}          // tool_use_id -> raw JSON input, filled at tool.end
	toolResults := map[string]provider.Block{} // tool_use_id -> tool_result block, filled at tool.end
	var toolsDone []string                     // tool_use_ids whose tool.end has arrived this iteration, in order
	var pendingInjected []string               // messages typed mid-turn, riding out with this iteration's tool results

	flush := func() {
		if !textSet && len(toolsDone) == 0 {
			return
		}
		var content []provider.Block
		if pendingText != "" {
			content = append(content, provider.TextBlock(pendingText))
		}
		var resultBlocks []provider.Block
		for _, id := range toolsDone {
			input := toolInputs[id]
			if input == "" {
				input = "{}"
			}
			content = append(content, provider.Block{
				Type:      provider.BlockToolUse,
				ToolUseID: id,
				ToolName:  toolName[id],
				ToolInput: json.RawMessage(input),
			})
			resultBlocks = append(resultBlocks, toolResults[id])
		}
		if len(content) > 0 {
			out = append(out, provider.Message{Role: provider.RoleAssistant, Content: content})
		}
		// Anything the user typed mid-turn went to the model as trailing
		// text in this same tool_result message (see takeInjected), so it
		// has to be put back there — not as a user message of its own,
		// which would leave two user messages in a row.
		for _, text := range pendingInjected {
			resultBlocks = append(resultBlocks, provider.TextBlock(injectedPreface+text))
		}
		if len(resultBlocks) > 0 {
			out = append(out, provider.Message{Role: provider.RoleUser, Content: resultBlocks})
		}
		pendingText, textSet = "", false
		pendingToolOrder = nil
		toolsDone = nil
		pendingInjected = nil
	}

	resetPending := func() {
		pendingText, textSet = "", false
		pendingToolOrder = nil
		toolsDone = nil
		pendingInjected = nil
	}

	// A "local" user message (/usage, /compact, /config, /memory, a
	// blocked/unknown command, ...) never reached the model, so it isn't
	// part of history — and neither is the message.part.end that follows
	// it, which is just that command's own display-only echo of its
	// answer, not something the model said. skipNextReply suppresses
	// exactly that one paired reply.
	var skipNextReply bool

	for _, ev := range evs {
		switch ev.Type {
		case events.TypeCompacted:
			if summary := dataString(ev.Data, "summary"); summary != "" {
				out = []provider.Message{{
					Role: provider.RoleUser,
					// The same header compactHistory writes, so a restart
					// rehydrates the summary with its provenance rather
					// than as bare user text.
					Content: []provider.Block{provider.TextBlock(summaryHeader + summary)},
				}}
				resetPending()
			}

		case events.TypeUserMessage:
			// An injected message is not a turn boundary: it went to the
			// model as trailing text inside the tool_result message of
			// the iteration it landed in, so flushing here would put it
			// in a message of its own and leave two user messages in a
			// row — a shape Bedrock rejects outright.
			//
			// It is recorded when the model is handed it, which is just
			// after the last tool.end of that iteration — so by now the
			// message it belongs to has usually already been emitted, and
			// the text goes onto the end of it. pendingInjected is the
			// fallback for the other order.
			if isTrue(ev.Data["injected"]) {
				text := injectedPreface + dataString(ev.Data, "text")
				if n := len(out); n > 0 && out[n-1].Role == provider.RoleUser && len(toolsDone) == 0 {
					out[n-1].Content = append(out[n-1].Content, provider.TextBlock(text))
				} else {
					pendingInjected = append(pendingInjected, dataString(ev.Data, "text"))
				}
				continue
			}
			flush()
			if isTrue(ev.Data["local"]) {
				skipNextReply = true
				continue
			}
			skipNextReply = false
			text := dataString(ev.Data, "model_text")
			if text == "" {
				text = dataString(ev.Data, "text")
			}
			// The source tag comes back with the message. Without it a
			// restarted daemon would send a skill body, a command
			// expansion or a carry-on notice as if the person had typed
			// it, and the manifest would say so.
			out = append(out, provider.Message{Role: provider.RoleUser, Content: []provider.Block{{
				Type: provider.BlockText, Text: text,
				Source:  dataString(ev.Data, "source"),
				Sources: dataSpans(ev.Data, "sources"),
			}}})

		case events.TypeToolStart:
			id := dataString(ev.Data, "tool_use_id")
			toolName[id] = dataString(ev.Data, "name")
			pendingToolOrder = append(pendingToolOrder, id)

		case events.TypeMessagePartEnd:
			if skipNextReply {
				skipNextReply = false
				resetPending()
				continue
			}
			pendingText = dataString(ev.Data, "text")
			textSet = true
			if len(pendingToolOrder) == 0 {
				// No tool use requested this iteration — this is the
				// turn's final assistant reply, flush immediately.
				flush()
			}

		case events.TypeToolEnd:
			id := dataString(ev.Data, "tool_use_id")
			input := dataString(ev.Data, "input")
			if input == "" {
				input = "{}"
			}
			toolInputs[id] = input
			isError, _ := ev.Data["is_error"].(bool)
			block := provider.ToolResultBlock(id, dataString(ev.Data, "content"), isError)
			// Whose material the result carried, and where. Restored so
			// a rebuilt history describes the same sources the live one
			// did; without it the manifest after a restart would name
			// one anonymous child answer where there were four.
			block.Sources = append(block.Sources, dataSources(ev.Data, "sources")...)
			toolResults[id] = block
			toolsDone = append(toolsDone, id)
			if len(toolsDone) == len(pendingToolOrder) {
				flush()
			}
		}
	}
	flush()

	return out
}

// rehydrateUsage replays TypeUsage/TypeCompacted events to reconstruct the
// latest-known usage snapshot (for context-% and auto-compact triggering)
// and the cumulative per-model totals /usage reports — the same two things
// recordUsage/addCumulativeUsage maintain live, sourced from what's already
// in the log instead.
func rehydrateUsage(evs []events.Event) (latest sessionUsage, haveUsage bool, cum map[string]modelTotals) {
	cum = map[string]modelTotals{}
	for _, ev := range evs {
		switch ev.Type {
		case events.TypeUsage:
			haveUsage = true
			latest = sessionUsage{
				InputTokens:  dataInt(ev.Data, "input_tokens"),
				OutputTokens: dataInt(ev.Data, "output_tokens"),
				MaxContext:   dataInt(ev.Data, "max_context"),
				TPS:          dataFloat(ev.Data, "tps"),
			}
			addModelTotals(cum, dataString(ev.Data, "model"), latest.InputTokens, latest.OutputTokens)

		case events.TypeCompacted:
			// clearUsage() runs live right after a successful compaction,
			// so the snapshot shouldn't carry forward past this point —
			// but cumulative totals never get cleared by compaction, and
			// the compaction call itself is billed too (if it reported
			// usage).
			haveUsage = false
			if model := dataString(ev.Data, "model"); model != "" {
				addModelTotals(cum, model, dataInt(ev.Data, "input_tokens"), dataInt(ev.Data, "output_tokens"))
			}
		}
	}
	if len(cum) == 0 {
		cum = nil
	}
	return latest, haveUsage, cum
}

func addModelTotals(cum map[string]modelTotals, model string, inputTokens, outputTokens int) {
	if model == "" {
		return
	}
	mt := cum[model]
	mt.InputTokens += inputTokens
	mt.OutputTokens += outputTokens
	mt.Calls++
	cum[model] = mt
}

func isTrue(v any) bool {
	b, _ := v.(bool)
	return b
}

func dataString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	s, _ := data[key].(string)
	return s
}

// dataInt handles both same-process event data (real Go int, since
// Store.Append keeps the map as-is in memory) and disk-restored data (all
// numbers become float64 once round-tripped through JSON).
func dataInt(data map[string]any, key string) int {
	if data == nil {
		return 0
	}
	switch v := data[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}

func dataFloat(data map[string]any, key string) float64 {
	if data == nil {
		return 0
	}
	switch v := data[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		return 0
	}
}

// dataSources reads the recorded source spans out of an event's data,
// which arrive as []any of maps after a round trip through JSON. A span
// that will not parse is skipped rather than guessed at: an entry with
// the wrong offsets would describe the wrong words.
func dataSources(data map[string]any, key string) []provider.BlockSource {
	raw, ok := data[key].([]any)
	if !ok {
		return nil
	}
	var out []provider.BlockSource
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["ID"].(string)
		if id == "" {
			id, _ = m["id"].(string)
		}
		from, fok := numberField(m, "From", "from")
		to, tok := numberField(m, "To", "to")
		if id == "" || !fok || !tok {
			continue
		}
		out = append(out, provider.BlockSource{ID: "child.result." + id, From: from, To: to})
	}
	return out
}

// dataSpans reads recorded spans whose ids are already complete, which
// is how an expanded command's own segments are written.
func dataSpans(data map[string]any, key string) []provider.BlockSource {
	raw, ok := data[key].([]any)
	if !ok {
		if already, ok := data[key].([]provider.BlockSource); ok {
			return already
		}
		return nil
	}
	var out []provider.BlockSource
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		if id == "" {
			id, _ = m["ID"].(string)
		}
		from, fok := numberField(m, "from", "From")
		to, tok := numberField(m, "to", "To")
		if id == "" || !fok || !tok {
			continue
		}
		out = append(out, provider.BlockSource{ID: id, From: from, To: to})
	}
	return out
}

// numberField reads an integer that JSON round-tripped into a float64.
func numberField(m map[string]any, names ...string) (int, bool) {
	for _, name := range names {
		if f, ok := m[name].(float64); ok {
			return int(f), true
		}
		if i, ok := m[name].(int); ok {
			return i, true
		}
	}
	return 0, false
}
