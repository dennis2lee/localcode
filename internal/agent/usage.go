package agent

import (
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

// recordUsage stores usage as sessionID's latest known token usage (each
// call overwrites, since a provider's input_tokens already reflects the
// full history sent so far — not something to accumulate across calls)
// and appends an events.TypeUsage event so any subscribed client can
// update its context-window/TPS display.
func (l *Loop) recordUsage(sessionID, model string, usage streamUsage) {
	maxContext := modelinfo.MaxContextTokens(model)
	tps := 0.0
	if usage.elapsed > 0 {
		tps = float64(usage.outputTokens) / usage.elapsed.Seconds()
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
		"show_tps":      l.ShowTPS(),
		"model":         model,
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
