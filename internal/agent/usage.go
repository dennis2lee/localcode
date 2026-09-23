package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"localcode/internal/events"
	"localcode/internal/provider"
)

// sessionUsage is the latest known token usage for one session, used to
// compute the context-window-fill percentage and drive auto-compaction.
type sessionUsage struct {
	InputTokens  int
	OutputTokens int
	MaxContext   int
	TPS          float64
	// Measured is what estimateTokens made of the messages InputTokens
	// counts plus the reply OutputTokens counts, taken when the count
	// arrived. It is the only way to tell the two answers apart later:
	// without it, a larger character sum could mean something was
	// appended since, or could mean four-characters-to-a-token simply
	// overshoots this content. See Loop.inputEstimate.
	Measured int
	// MeasuredImages is how many image blocks those messages held, so
	// the images appended after the count can be told from the ones it
	// covered. See Loop.compactionMeasure.
	MeasuredImages int
	// CachedInputTokens is the part of the prompt the provider served
	// from its cache and reported apart from InputTokens. See
	// promptTokens.
	CachedInputTokens int
}

// promptTokens is the whole prompt the provider read for this call: what
// it counted fresh, plus what it served from its own cache.
//
// InputTokens alone is not that number. Where a prompt cache is working,
// the provider reports the cached prefix separately and InputTokens
// covers only the fresh suffix: this repo's own fixture has
// input_tokens 12 beside cache_read_input_tokens 4096. The two are kept
// apart because they are priced apart, and everything about the window
// wants them together, because the window holds all of it. Read apart,
// a working cache made a nearly full conversation look empty: the gauge
// read near zero, auto-compaction never fired, and the next request was
// sized as though the prefix were not there.
func (u sessionUsage) promptTokens() int {
	return u.InputTokens + u.CachedInputTokens
}

// modelTotals accumulates token usage across every provider.Chat call a
// session has made against one model. Unlike sessionUsage (the latest
// snapshot, used for context-window-fill %), this is a running sum: each
// API call is billed for its own full request (history included), so
// summing every call's tokens is the correct "how much has this session
// used" figure — see /usage.
//
// Four kinds of token, kept apart because they are billed apart. Under a
// working prompt cache the repeatedly sent history is not in InputTokens
// at all: the provider serves it from the cache and reports it as a
// cache read, at a fraction of the input rate, and the first time a
// prefix is cached it is reported as a cache write, above the input
// rate. A total of input and output alone left out most of what a cached
// session sent, which is the part /usage says it counts.
type modelTotals struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	// CacheUnsplitTokens is cached prompt a log recorded as one figure,
	// without saying which part was read from the cache and which was
	// written to it: usage events carried only cached_input_tokens before
	// the two were recorded apart. Counted, because it was sent, and shown
	// as "cache read or write" rather than guessed into either column.
	CacheUnsplitTokens int
	Calls              int
}

// callTokens is what one model call reported, in the four kinds
// modelTotals keeps, plus a cached figure recorded without its split.
type callTokens struct {
	input, output, cacheRead, cacheWrite, cached int
}

func (t modelTotals) add(c callTokens) modelTotals {
	t.InputTokens += c.input
	t.OutputTokens += c.output
	t.CacheReadTokens += c.cacheRead
	t.CacheWriteTokens += c.cacheWrite
	t.CacheUnsplitTokens += c.cached
	t.Calls++
	return t
}

// total is every token the calls processed: what they were sent, fresh
// or from the cache, and what they wrote back.
func (t modelTotals) total() int {
	return t.InputTokens + t.CacheReadTokens + t.CacheWriteTokens + t.CacheUnsplitTokens + t.OutputTokens
}

func (t modelTotals) cached() bool {
	return t.CacheReadTokens > 0 || t.CacheWriteTokens > 0 || t.CacheUnsplitTokens > 0
}

// measurement is what estimateTokens made of the messages a count
// covered, and how many image blocks were among them.
type measurement struct{ tokens, images int }

func measure(system string, msgs []provider.Message) measurement {
	return measurement{tokens: estimateTokens(system, msgs), images: countImages(msgs)}
}

// tokensOf is what a streamed call reported.
func tokensOf(u streamUsage) callTokens {
	return callTokens{input: u.inputTokens, output: u.outputTokens, cacheRead: u.cacheRead, cacheWrite: u.cacheWrite}
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
// maxContext is the model's total input+output budget, resolved by the
// caller from the profile — not looked up here. The lookup guesses from
// the model name, and a profile is allowed to say what the name cannot:
// that this local server's "my-model" holds 32k, or that a proxy trims a
// window down. Resolving it in one place is what keeps the meter, the
// auto-compaction trigger, and the size of the next request from
// disagreeing about how much room there is.
func (l *Loop) recordUsage(sessionID, model string, maxContext int, measured measurement, usage streamUsage) {

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
		InputTokens:       usage.inputTokens,
		OutputTokens:      usage.outputTokens,
		MaxContext:        maxContext,
		TPS:               tps,
		Measured:          measured.tokens,
		MeasuredImages:    measured.images,
		CachedInputTokens: usage.cacheRead + usage.cacheWrite,
	}

	l.mu.Lock()
	l.usage[sessionID] = u
	if l.cumulativeUsage[sessionID] == nil {
		l.cumulativeUsage[sessionID] = map[string]modelTotals{}
	}
	l.cumulativeUsage[sessionID][model] = l.cumulativeUsage[sessionID][model].add(tokensOf(usage))
	l.mu.Unlock()

	percent := 0.0
	if maxContext > 0 {
		percent = float64(u.promptTokens()+u.OutputTokens) / float64(maxContext) * 100
	}
	l.Store.Append(sessionID, events.TypeUsage, map[string]any{
		"input_tokens":  u.InputTokens,
		"output_tokens": u.OutputTokens,
		"max_context":   u.MaxContext,
		"percent":       percent,
		"tps":           tps,
		// What this side made of the same messages, so a session read
		// back from the log can still tell a count that covers
		// everything from one that predates a tool result. A log written
		// before this key existed reads as zero, which inputEstimate
		// treats as "no measurement" rather than as a measurement of
		// nothing.
		"measured": u.Measured,
		// And how many images were among them, so a restored session can
		// tell the images appended after the count from the ones it
		// covered, as a live one does.
		"measured_images": u.MeasuredImages,
		// The cached prefix, so a session read back from the log knows
		// how much of its window is in use. Kept out of input_tokens,
		// which clients show as what was billed at the full rate.
		"cached_input_tokens": u.CachedInputTokens,
		// The same prefix split the way it is billed, read from the
		// cache or written to it, for /usage and the usage window.
		"cache_read_tokens":  usage.cacheRead,
		"cache_write_tokens": usage.cacheWrite,
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

// addCumulativeUsage folds one off-transcript model call (e.g. the
// compaction summarization) into /usage's running totals, without touching
// the latest-usage snapshot or emitting a usage event.
func (l *Loop) addCumulativeUsage(sessionID, model string, usage streamUsage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cumulativeUsage[sessionID] == nil {
		l.cumulativeUsage[sessionID] = map[string]modelTotals{}
	}
	l.cumulativeUsage[sessionID][model] = l.cumulativeUsage[sessionID][model].add(tokensOf(usage))
}

// usageLine is one model's figures as /usage prints them. The cache
// columns appear only where there is something in them, so a provider
// with no prompt cache reads as it always has.
func usageLine(t modelTotals) string {
	var b strings.Builder
	fmt.Fprintf(&b, "input %d", t.InputTokens)
	if t.CacheReadTokens > 0 {
		fmt.Fprintf(&b, " · cache read %d", t.CacheReadTokens)
	}
	if t.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, " · cache write %d", t.CacheWriteTokens)
	}
	if t.CacheUnsplitTokens > 0 {
		fmt.Fprintf(&b, " · cache read or write %d", t.CacheUnsplitTokens)
	}
	fmt.Fprintf(&b, " · output %d · total %d (%s)", t.OutputTokens, t.total(), calls(t.Calls))
	return b.String()
}

func calls(n int) string {
	if n == 1 {
		return "1 call"
	}
	return fmt.Sprintf("%d calls", n)
}

// cacheNote says what the cache figures are, wherever they are shown.
const cacheNote = "Cache read and cache write are prompt the provider served from its prompt cache or wrote to it. " +
	"They are counted apart from input because the provider bills them at its own cache rates, " +
	"which on Anthropic's models are below the input rate for a read and above it for a write."

// cacheUnsplitNote says what the figure recorded without its split is.
const cacheUnsplitNote = "Cache read or write is cached prompt a log recorded as one figure, without saying which of the two it was."

// usageReport is the per-model lines, the grand total, and, where any
// cache column appeared, what those columns are.
func usageReport(heading string, totals map[string]modelTotals) string {
	models := make([]string, 0, len(totals))
	for m := range totals {
		models = append(models, m)
	}
	sort.Strings(models)

	var b strings.Builder
	b.WriteString(heading)
	var grand modelTotals
	for _, m := range models {
		t := totals[m]
		name := m
		if name == "" {
			name = "(model not recorded)"
		}
		fmt.Fprintf(&b, "- %s: %s\n", name, usageLine(t))
		grand.InputTokens += t.InputTokens
		grand.OutputTokens += t.OutputTokens
		grand.CacheReadTokens += t.CacheReadTokens
		grand.CacheWriteTokens += t.CacheWriteTokens
		grand.CacheUnsplitTokens += t.CacheUnsplitTokens
		grand.Calls += t.Calls
	}
	fmt.Fprintf(&b, "\nGrand total: %s", usageLine(grand))
	if grand.cached() {
		b.WriteString("\n\n" + cacheNote)
		if grand.CacheUnsplitTokens > 0 {
			b.WriteString(" " + cacheUnsplitNote)
		}
	}
	return b.String()
}
