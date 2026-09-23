package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/tools"
	"localcode/internal/trace"
)

// Under a working prompt cache the repeatedly sent history is not in
// input_tokens at all: the provider serves it from the cache and reports
// it as a cache read, and the first time a prefix is cached it reports a
// cache write. /usage summed input and output alone, so a cached session
// read as having sent a few thousand tokens when it had sent hundreds of
// thousands, the part /usage says it counts. The two cache figures are
// billed apart from input and from each other, so they are carried as
// their own columns through every place the totals are kept.

// cachedCall is a reply whose provider served most of the prompt from
// its cache, as this repo's own Anthropic fixture reports it.
func cachedCall(text string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: text},
		{Type: provider.EventUsage, InputTokens: 12, OutputTokens: 30, CacheReadTokens: 4096, CacheWriteTokens: 128},
		{Type: provider.EventMessageStop, StopReason: "end_turn"},
	}
}

func lastLocalReply(t *testing.T, loop *Loop, sid string) string {
	t.Helper()
	evs, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == events.TypeMessagePartEnd {
			s, _ := evs[i].Data["text"].(string)
			return s
		}
	}
	t.Fatal("no reply in the log")
	return ""
}

// A turn's cache read and write reach /usage, the usage event, and a
// restart's totals, all three the same.
func TestUsageCountsWhatTheCacheServed(t *testing.T) {
	p := &scriptedProvider{turns: [][]provider.StreamEvent{cachedCall("one"), cachedCall("two")}}
	loop, sid := scriptedLoop(t, p, tools.NewRegistry(nil))
	for _, prompt := range []string{"first", "second"} {
		if err := loop.SendMessage(context.Background(), sid, "general-purpose", prompt); err != nil {
			t.Fatalf("turn: %v", err)
		}
	}

	if err := loop.handleCostCommand(sid, "/usage"); err != nil {
		t.Fatalf("/usage: %v", err)
	}
	report := lastLocalReply(t, loop, sid)
	for _, want := range []string{
		"input 24 · cache read 8192 · cache write 256 · output 60 · total 8532 (2 calls)",
		"Grand total: input 24 · cache read 8192 · cache write 256 · output 60 · total 8532 (2 calls)",
		"bills them at its own cache rates",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("/usage does not say %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "cache read or write") {
		t.Errorf("/usage shows an unsplit cache column for calls that reported the split:\n%s", report)
	}

	evs, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var seen int
	for _, e := range evs {
		if e.Type != events.TypeUsage {
			continue
		}
		seen++
		if dataInt(e.Data, "cache_read_tokens") != 4096 || dataInt(e.Data, "cache_write_tokens") != 128 || dataInt(e.Data, "cached_input_tokens") != 4224 {
			t.Errorf("usage event does not carry the cache split: %v", e.Data)
		}
	}
	if seen != 2 {
		t.Fatalf("%d usage events, want 2", seen)
	}

	_, _, cum := rehydrateUsage(evs)
	live := cumulativeOf(loop, sid)
	if cum["m"] != live["m"] {
		t.Errorf("a restart totals %+v where the live session totals %+v", cum["m"], live["m"])
	}
}

// The summarizing call of a compaction sends the same system prompt and
// history a turn does, so a working cache serves most of it; its cache
// figures reach /usage and the compacted event as a turn's do.
func TestACompactionsCacheIsCounted(t *testing.T) {
	loop, store, profile := replayLoop(t, replayProvider{events: cachedCall("a summary")}, 200000)
	const sid = "s1"
	loop.setHistory(sid, shortConversation())
	if err := loop.compactHistory(context.Background(), sid, replayProvider{events: cachedCall("a summary")}, profile, "", nil, "", CompactManual); err != nil {
		t.Fatalf("compaction: %v", err)
	}
	live := cumulativeOf(loop, sid)
	if got := live["m"]; got.CacheReadTokens != 4096 || got.CacheWriteTokens != 128 || got.InputTokens != 12 {
		t.Errorf("the compaction call's totals = %+v, want its cache read and write counted", got)
	}
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	_, _, cum := rehydrateUsage(evs)
	if cum["m"] != live["m"] {
		t.Errorf("a restart totals the compaction at %+v, live %+v", cum["m"], live["m"])
	}
	totals, _, _ := loop.usageAcross(usageWindow{name: "every conversation"})
	if totals["m"] != live["m"] {
		t.Errorf("/usage all totals the compaction at %+v, live %+v", totals["m"], live["m"])
	}
}

// A log written before the split was recorded names only
// cached_input_tokens. It was sent, so it is counted, and it is shown as
// what it is: cached, without a claim about which part was read and which
// written. A log with no cache figures at all reads as it always did.
func TestAnUnsplitCachedFigureIsCountedAsItIs(t *testing.T) {
	cum := map[string]modelTotals{}
	addModelTotals(cum, "old", callTokensOf(map[string]any{"input_tokens": 12, "output_tokens": 30, "cached_input_tokens": 4224}))
	addModelTotals(cum, "new", callTokensOf(map[string]any{"input_tokens": 12, "output_tokens": 30, "cached_input_tokens": 4224,
		"cache_read_tokens": 4096, "cache_write_tokens": 128}))
	addModelTotals(cum, "plain", callTokensOf(map[string]any{"input_tokens": 150, "output_tokens": 40}))

	if got := cum["old"]; got.CacheUnsplitTokens != 4224 || got.CacheReadTokens != 0 || got.CacheWriteTokens != 0 {
		t.Errorf("an unsplit cached figure = %+v, want all of it counted as cached", got)
	}
	if got := cum["new"]; got.CacheUnsplitTokens != 0 || got.CacheReadTokens != 4096 || got.CacheWriteTokens != 128 {
		t.Errorf("a split cached figure = %+v, want the split and nothing unsplit", got)
	}
	report := usageReport("Token usage by model:\n", cum)
	for _, want := range []string{
		"- old: input 12 · cache read or write 4224 · output 30 · total 4266 (1 call)",
		"- new: input 12 · cache read 4096 · cache write 128 · output 30 · total 4266 (1 call)",
		"- plain: input 150 · output 40 · total 190 (1 call)",
		"without saying which of the two it was",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not say %q:\n%s", want, report)
		}
	}

	plain := usageReport("Token usage by model:\n", map[string]modelTotals{"plain": cum["plain"]})
	if strings.Contains(plain, "cache") || strings.Contains(plain, "cache rates") {
		t.Errorf("a provider with no cache is told about one:\n%s", plain)
	}
}

// cumulativeOf copies a session's running totals under the lock.
func cumulativeOf(l *Loop, sid string) map[string]modelTotals {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]modelTotals{}
	for m, t := range l.cumulativeUsage[sid] {
		out[m] = t
	}
	return out
}

// The note under /usage says what the cache columns are whenever any of
// them appears, whichever one it is, and the line about the unsplit
// figure only when that one does. A report with no cache says nothing
// about one.
func TestTheCacheNoteFollowsEveryCacheColumn(t *testing.T) {
	for _, c := range []struct {
		name          string
		t             modelTotals
		note, unsplit bool
	}{
		{"a cache write alone", modelTotals{InputTokens: 12, CacheWriteTokens: 4096, OutputTokens: 30, Calls: 1}, true, false},
		{"a cache read alone", modelTotals{InputTokens: 12, CacheReadTokens: 4096, OutputTokens: 30, Calls: 1}, true, false},
		{"an old log's unsplit figure alone", modelTotals{InputTokens: 12, CacheUnsplitTokens: 4224, OutputTokens: 30, Calls: 1}, true, true},
		{"no cache", modelTotals{InputTokens: 12, OutputTokens: 30, Calls: 1}, false, false},
	} {
		report := usageReport("Token usage by model:\n", map[string]modelTotals{"m": c.t})
		if got := strings.Contains(report, cacheNote); got != c.note {
			t.Errorf("%s: cache note shown = %v, want %v:\n%s", c.name, got, c.note, report)
		}
		if got := strings.Contains(report, cacheUnsplitNote); got != c.unsplit {
			t.Errorf("%s: unsplit note shown = %v, want %v:\n%s", c.name, got, c.unsplit, report)
		}
		summary := usageSummaryOf("x", map[string]modelTotals{"m": c.t}, 1, 0)
		if got := summary.Note != ""; got != c.note {
			t.Errorf("%s: the API summary's note = %q, want one = %v", c.name, summary.Note, c.note)
		}
		if c.unsplit && !strings.Contains(summary.Note, cacheUnsplitNote) {
			t.Errorf("%s: the API summary's note leaves out the unsplit line: %q", c.name, summary.Note)
		}
	}
}

// /usage all and the usage window read the same logs, with the cache
// columns, and agree with the live session.
func TestUsageAllCountsWhatTheCacheServed(t *testing.T) {
	p := &scriptedProvider{turns: [][]provider.StreamEvent{cachedCall("one")}}
	loop, sid := scriptedLoop(t, p, tools.NewRegistry(nil))
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "first"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	live := cumulativeOf(loop, sid)["m"]
	totals, _, _ := loop.usageAcross(usageWindow{name: "every conversation"})
	if totals["m"] != live {
		t.Errorf("/usage all totals %+v, the live session %+v", totals["m"], live)
	}
	summary, ok := loop.UsageAcross("all", time.Now())
	if !ok {
		t.Fatal("UsageAcross refused all")
	}
	if got := summary.Models["m"]; got != figuresOf(live) {
		t.Errorf("the usage window's summary %+v, the live session %+v", got, figuresOf(live))
	}
}

// The compaction's trace records carry the cache figures its call
// reported, as a turn's model span does, and as /usage counts them.
func TestTheCompactionsTraceCarriesItsCache(t *testing.T) {
	p := replayProvider{events: cachedCall("a summary")}
	loop, _, profile := replayLoop(t, p, 200000)
	loop.SetSmartAgentEnabled(true)
	w := withTracing(t, loop)
	const sid = "s1"
	loop.setHistory(sid, shortConversation())
	if err := loop.compactHistory(context.Background(), sid, p, profile, "", nil, "", CompactManual); err != nil {
		t.Fatalf("compaction: %v", err)
	}
	live := cumulativeOf(loop, sid)["m"]
	var model, lifecycle bool
	for _, rec := range w.Recent(200, sid, "") {
		switch {
		case rec.Span == trace.SpanModel && strings.Contains(rec.Detail, "compaction attempt"):
			model = true
		case rec.Span == trace.SpanCompact:
			lifecycle = true
		default:
			continue
		}
		if rec.CacheReadTokens != live.CacheReadTokens || rec.CacheWriteTokens != live.CacheWriteTokens {
			t.Errorf("%s record: cache read %d, write %d; /usage counted read %d, write %d",
				rec.Span, rec.CacheReadTokens, rec.CacheWriteTokens, live.CacheReadTokens, live.CacheWriteTokens)
		}
	}
	if !model || !lifecycle {
		t.Fatalf("records found: model span %v, lifecycle %v", model, lifecycle)
	}
}

// A summarizing call that came back empty leaves no compacted event, the
// call's only record in the log. Counted live, it was spend in this
// process's /usage that a restart and /usage all left out; it is left
// out of all of them alike.
func TestAnEmptySummaryIsCountedAlikeEverywhere(t *testing.T) {
	p := replayProvider{events: cachedCall("")}
	loop, store, profile := replayLoop(t, p, 200000)
	const sid = "s1"
	loop.setHistory(sid, shortConversation())
	if err := loop.compactHistory(context.Background(), sid, p, profile, "", nil, "", CompactManual); err == nil {
		t.Fatal("an empty summary was accepted")
	}
	live := cumulativeOf(loop, sid)
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	_, _, cum := rehydrateUsage(evs)
	all, _, _ := loop.usageAcross(usageWindow{name: "every conversation"})
	if live["m"] != cum["m"] || live["m"] != all["m"] {
		t.Errorf("one summarizing call: live /usage %+v, after a restart %+v, /usage all %+v", live["m"], cum["m"], all["m"])
	}
}

// A run's usage is its conversation's and every session's below it: a
// sub-agent's calls are made in a session of its own.
func TestTheUsageOfATreeCountsItsSubAgents(t *testing.T) {
	p := &scriptedProvider{turns: [][]provider.StreamEvent{cachedCall("one")}}
	loop, sid := scriptedLoop(t, p, tools.NewRegistry(nil))
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "first"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if _, err := loop.Store.CreateSession("child", sid, "general-purpose", false); err != nil {
		t.Fatal(err)
	}
	loop.Store.Append("child", events.TypeUsage, map[string]any{"model": "sub", "input_tokens": 50, "output_tokens": 7})
	if _, err := loop.Store.CreateSession("grandchild", "child", "general-purpose", false); err != nil {
		t.Fatal(err)
	}
	loop.Store.Append("grandchild", events.TypeUsage, map[string]any{"model": "sub", "input_tokens": 5, "output_tokens": 1})
	if _, err := loop.Store.CreateSession("unrelated", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	loop.Store.Append("unrelated", events.TypeUsage, map[string]any{"model": "sub", "input_tokens": 999, "output_tokens": 999})

	total := loop.UsageOfTree(sid).Total()
	want := UsageFigures{InputTokens: 12 + 50 + 5, OutputTokens: 30 + 7 + 1, CacheReadTokens: 4096, CacheWriteTokens: 128, Calls: 3}
	if total != want {
		t.Errorf("the tree's usage = %+v, want %+v (the conversation, its sub-agent and theirs, and nothing else)", total, want)
	}
}
