package agent

import (
	"time"

	"localcode/internal/events"
	"localcode/internal/modelinfo"
)

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

// turnRate accumulates output tokens and generation time across every
// model call made within one turn, so tokens-per-second can be reported
// over the turn rather than over whichever call happened to finish last.
// Reset at the start of each turn — see startTurnRate.
type turnRate struct {
	tokens int
	dur    time.Duration
}

// startTurnRate clears the per-turn rate accumulator. Called once when a
// turn begins, so the figure describes the turn in progress rather than
// averaging in every turn before it.
func (l *Loop) startTurnRate(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.turnRate, sessionID)
}

// recordUsage stores usage as sessionID's latest known token usage (each
// call overwrites, since a provider's input_tokens already reflects the
// full history sent so far — not something to accumulate across calls)
// and appends an events.TypeUsage event so any subscribed client can
// update its context-window/TPS display.
func (l *Loop) recordUsage(sessionID, model string, usage streamUsage) {
	maxContext := modelinfo.MaxContextTokens(model)

	// Rate over the whole turn so far, not over this one model call.
	//
	// A turn that uses tools is several calls, and the last one is
	// routinely a five-token "done." that took a moment: reporting each
	// call on its own made the number leap between 40 and 3 for no reason
	// the person watching could see. Totals divided by total generation
	// time is both steadier and the figure actually being asked for —
	// how fast is this model producing text for me.
	// A call whose generation time could not be measured (see
	// consumeStream) contributes neither tokens nor time: folding in
	// tokens that took "no time" would spike the average rather than
	// leave it alone.
	l.mu.Lock()
	r := l.turnRate[sessionID]
	if usage.elapsed > 0 {
		r.tokens += usage.outputTokens
		r.dur += usage.elapsed
		l.turnRate[sessionID] = r
	}
	l.mu.Unlock()

	tps := 0.0
	if r.dur > 0 {
		tps = float64(r.tokens) / r.dur.Seconds()
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
		// Explicitly false so it clears the flag set by the live estimates
		// broadcast during the stream — a client merges usage events, and
		// a missing key would leave the "~" on an exact figure.
		"estimated": false,
		"show_tps":  l.ShowTPS(),
		"model":     model,
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
