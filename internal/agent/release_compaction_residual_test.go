package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// What the notice under a cut-off reply prices /compact against, held to
// what a compaction actually stores. Three things came apart there: the
// residual left out the notes and the framing a compaction writes around
// the summary, nothing held the stored summary to the length the residual
// took as its worst, and the condition "if its summary comes out short"
// was offered where only a summary of nothing would have done.

// replayProvider answers every call with the same events, over a
// channel like a real backend and without the network.
type replayProvider struct{ events []provider.StreamEvent }

func (p replayProvider) Chat(context.Context, provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, len(p.events))
	for _, ev := range p.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

// summaryOf is a summarization call that comes back with text.
func summaryOf(text string) replayProvider {
	return replayProvider{events: []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: text},
		{Type: provider.EventUsage, InputTokens: 100, OutputTokens: 200},
		{Type: provider.EventMessageStop, StopReason: "end_turn"},
	}}
}

// replayLoop is a loop whose one profile talks to p, with one session
// "s1" open on a window of the given size.
func replayLoop(t *testing.T, p provider.Provider, window int) (*Loop, *session.Store, config.Profile) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	profile := config.Profile{Provider: "local", Model: "m", ContextWindow: window}
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1/v1"}},
		Profiles:       map[string]config.Profile{"p": profile},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "p"}},
		DefaultProfile: "p",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": p}, cfg)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return loop, store, profile
}

// carriedHistory is a conversation a compaction has the most to say
// about: two images it will drop, and two assets its messages carried.
func carriedHistory() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{
			{Type: provider.BlockText, Text: "the file, spliced in", Sources: []provider.BlockSource{{ID: "file.internal/agent/turn.go"}}},
			provider.ImageBlock("image/png", []byte("not really a png")),
		}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("read it")}},
		{Role: provider.RoleUser, Content: []provider.Block{
			{Type: provider.BlockText, Text: "and the skill", Sources: []provider.BlockSource{{ID: "skill.body.deploy"}}},
			provider.ImageBlock("image/png", []byte("nor this")),
		}},
	}
}

// The residual is what a compaction leaves behind, and a compaction
// leaves more than the system prompt and the summary: the header, a note
// about the images it dropped, a note about the assets its messages
// carried, and the framing of the block they share. With all of it left
// out the residual sat 85 tokens under what was stored, which is exactly
// the boundary the notice reasons about.
func TestTheResidualPricesWhatACompactionStores(t *testing.T) {
	summary := strings.Repeat("s", 4000)
	loop, _, profile := replayLoop(t, summaryOf(summary), 200000)
	const sid = "s1"
	history := carriedHistory()
	loop.setHistory(sid, history)
	ctx := config.WithSmartAgent(context.Background(), true)
	system := strings.Repeat("p", 8000)
	run := modelRun{profileName: "p", profile: profile, system: system, maxTokens: 4096}

	sizing := loop.sizeRequest(ctx, sid, run, history)
	if want := compactionKeeps(history, true); sizing.keeps != want {
		t.Errorf("sizeRequest keeps %d, want the %d a compaction of this history stores", sizing.keeps, want)
	}
	if bare := compactionKeeps(nil, false); sizing.keeps <= bare {
		t.Errorf("a history with images and carried assets keeps %d, no more than an empty one's %d: the notes are not priced", sizing.keeps, bare)
	}

	if err := loop.compactHistory(ctx, sid, summaryOf(summary), profile, system, nil, "", CompactManual); err != nil {
		t.Fatalf("compaction: %v", err)
	}
	stored := estimateTokens(system, loop.history(sid))
	residual := sizing.afterCompaction(estimateTokens(summary, nil))
	if residual < stored {
		t.Errorf("afterCompaction prices a %d-token summary at %d, but the compaction stored %d", estimateTokens(summary, nil), residual, stored)
	}
	// Rounded up, not padded: within the rounding of three figures.
	if residual > stored+3 {
		t.Errorf("afterCompaction prices the compaction at %d, %d over what it stored", residual, residual-stored)
	}
}

// The worst case the notice prices is the longest summary a compaction
// keeps, and a compaction has to keep no more than that. A server that
// ignores max_tokens used to have its whole answer stored, so "/compact
// makes room now", promised at the worst, promised what the next request
// would not do. What is stored is held to longestSummary, cut at a rune
// boundary with a note saying so, and an honest summary at the bound is
// kept whole.
func TestASummaryPastTheBoundIsCutToIt(t *testing.T) {
	const sid = "s1"
	before := []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("something worth summarizing")}}}

	long := strings.Repeat("w", 9*defaultMaxTokens*4)
	loop, store, profile := replayLoop(t, summaryOf(long), 200000)
	loop.setHistory(sid, before)
	if err := loop.compactHistory(context.Background(), sid, summaryOf(long), profile, "", nil, "", CompactManual); err != nil {
		t.Fatalf("compaction with a long summary: %v", err)
	}
	hist := loop.history(sid)
	if len(hist) != 1 {
		t.Fatalf("compacted history holds %d messages, want 1", len(hist))
	}
	got := estimateTokens("", hist)
	if want := compactionKeeps(before, false) + longestSummary; got > want {
		t.Errorf("a %d-token summary is stored at %d tokens, above the %d afterCompaction prices at the most", estimateTokens(long, nil), got, want)
	}
	text := hist[0].Content[0].Text
	if !strings.HasSuffix(text, summaryCutNote) {
		t.Errorf("a cut summary does not say it was cut; ends %q", text[max(0, len(text)-60):])
	}
	// The record agrees with the history: a restart rebuilds the cut
	// summary, not the one the server sent.
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if rebuilt := estimateTokens("", rehydrateHistory(evs)); rebuilt != got {
		t.Errorf("a restart rebuilds %d tokens where the live history holds %d", rebuilt, got)
	}

	// An honest summary at the bound is kept whole. The bound is the
	// cap the call ran under and a quarter more, because the cap is in
	// the server's tokens and the bound in this side's, which reads
	// English prose at up to a quarter more; a bound at the cap itself
	// would cut summaries from a server that honoured it.
	whole := strings.Repeat("w", (defaultMaxTokens+defaultMaxTokens/4)*4)
	if estimateTokens(whole, nil) != longestSummary {
		t.Fatalf("precondition: the bound is %d tokens, not the cap and a quarter (%d)", longestSummary, estimateTokens(whole, nil))
	}
	usage, err := os.ReadFile(filepath.Join("..", "..", "docs", "USAGE.md"))
	if err != nil {
		t.Fatalf("USAGE.md: %v", err)
	}
	if figure := fmt.Sprintf("at most %d tokens of the summary", longestSummary); !strings.Contains(string(usage), figure) {
		t.Errorf("USAGE.md does not state the bound as %q", figure)
	}
	if err := loop.compactHistory(context.Background(), sid, summaryOf(whole), profile, "", nil, "", CompactManual); err != nil {
		t.Fatalf("compaction with a summary at the bound: %v", err)
	}
	if text := loop.history(sid)[0].Content[0].Text; text != summaryHeader+whole {
		t.Errorf("a summary of exactly %d tokens was not stored whole (%d bytes, cut=%v)", longestSummary, len(text), strings.HasSuffix(text, summaryCutNote))
	}

	// One that has to be cut is cut between runes, not through one.
	korean := strings.Repeat("요약", longestSummary*2)
	if err := loop.compactHistory(context.Background(), sid, summaryOf(korean), profile, "", nil, "", CompactManual); err != nil {
		t.Fatalf("compaction with a Korean summary: %v", err)
	}
	if text := loop.history(sid)[0].Content[0].Text; !utf8.ValidString(text) || !strings.HasSuffix(text, summaryCutNote) {
		t.Errorf("a Korean summary was cut through a rune (valid=%v, noted=%v)", utf8.ValidString(text), strings.HasSuffix(text, summaryCutNote))
	}
}

// "/compact makes room, if its summary comes out short" was priced with a
// summary of nothing, and compactHistory rejects a summary of nothing as
// a failed compaction. Where only that would have helped, the person was
// told to compact, waited through the summarization call, and got no
// more room. The condition now names the longest summary that helps, and
// is not offered when no summary a compaction can return would.
func TestTheHedgeNamesTheSummaryLengthThatHelps(t *testing.T) {
	const window = 8192
	keeps := compactionKeeps(nil, false)
	s := requestSizing{
		wanted: 4096,
		sent:   minOutputTokens,
		input:  window,
		window: window,
		source: windowGuessed,
		keeps:  keeps,
		// The shortest compaction leaves exactly the floor: only a
		// summary of nothing would leave more.
		system: window - contextHeadroom - keeps - shortestSummary - minOutputTokens,
	}
	if s.sent != clampMaxTokens(s.wanted, s.window, s.input) {
		t.Fatalf("precondition: sent %d is not the clamped %d", s.sent, clampMaxTokens(s.wanted, s.window, s.input))
	}
	got := cutOffNotice(s, "p", "m")
	if strings.Contains(got, "/compact makes room") {
		t.Errorf("offers a compaction that only an empty summary would satisfy:\n%s", got)
	}
	if room := fmt.Sprintf("even the shortest compaction leaves room for %d", minOutputTokens); !strings.Contains(got, room) {
		t.Errorf("does not say what the shortest compaction leaves (%q):\n%s", room, got)
	}

	// With 500 tokens more, /compact is offered on the condition that
	// the summary comes out at 500 tokens: 500 helps and 501 does not.
	s.system -= 500
	got = cutOffNotice(s, "p", "m")
	if !strings.Contains(got, "/compact makes room, if its summary comes out at 500 tokens or fewer") {
		t.Errorf("does not name the summary length that helps:\n%s", got)
	}
	if clampMaxTokens(s.wanted, s.window, s.afterCompaction(500)) <= s.sent || clampMaxTokens(s.wanted, s.window, s.afterCompaction(501)) > s.sent {
		t.Errorf("precondition: 500 sends %d and 501 sends %d against %d", clampMaxTokens(s.wanted, s.window, s.afterCompaction(500)), clampMaxTokens(s.wanted, s.window, s.afterCompaction(501)), s.sent)
	}

	// With room for the longest summary a compaction keeps, it is
	// promised outright.
	s.window, s.input, s.system = 32768, 32768, 0
	got = cutOffNotice(s, "p", "m")
	if !strings.Contains(got, "/compact makes room now") {
		t.Errorf("does not promise a compaction that helps at any length:\n%s", got)
	}
}

// A reply whose stream dies half-way is closed on the record and kept
// out of the history: a failed response is not a turn. A restart used to
// rebuild it into the history from that record, so the half answer the
// live session refused to send went out on the next request of every
// restarted session. The record now says the reply failed, and a
// rebuilt history agrees with the live one.
func TestAFailedReplyStaysOutOfTheHistoryAfterARestart(t *testing.T) {
	dies := replayProvider{events: []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: "half an answer"},
		{Type: provider.EventError, Err: errors.New("connection reset")},
	}}
	loop, store, _ := replayLoop(t, dies, 200000)
	const sid = "s1"
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "go on"); err == nil {
		t.Fatalf("a stream that died was reported as a turn")
	}
	for _, m := range loop.history(sid) {
		if m.Role == provider.RoleAssistant {
			t.Errorf("the live history kept the failed reply: %q", m.Content[0].Text)
		}
	}
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	closed := false
	for _, e := range evs {
		if e.Type != events.TypeMessagePartEnd {
			continue
		}
		closed = true
		if !isTrue(e.Data["failed"]) {
			t.Errorf("the failed reply's record does not say it failed: %v", e.Data)
		}
	}
	if !closed {
		t.Fatalf("the failed reply was not closed on the record")
	}
	for _, m := range rehydrateHistory(evs) {
		if m.Role == provider.RoleAssistant {
			t.Errorf("a restart rebuilt the failed reply into the history: %q", m.Content[0].Text)
		}
	}
}
