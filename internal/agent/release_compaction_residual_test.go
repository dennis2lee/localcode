package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	// Rounded up, not padded: at most the two tokens the rounding of
	// three figures can add.
	if residual > stored+2 {
		t.Errorf("afterCompaction prices the compaction at %d, %d over what it stored", residual, residual-stored)
	}
}

// The stored block is floored once over all its bytes; the residual adds
// three figures floored apart, and with the bytes each carries past a
// multiple of four it came out a token under what was stored in three
// of every sixteen alignments. Every alignment of the system prompt and
// the summary, with and without the notes, against a real compaction.
func TestTheResidualIsNeverUnderWhatIsStoredInAnyAlignment(t *testing.T) {
	for _, carried := range []bool{false, true} {
		for sys := 8000; sys < 8004; sys++ {
			for sum := 4000; sum < 4004; sum++ {
				system := strings.Repeat("p", sys)
				summary := strings.Repeat("s", sum)
				loop, _, profile := replayLoop(t, summaryOf(summary), 200000)
				const sid = "s1"
				history := []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("a conversation")}}}
				if carried {
					history = carriedHistory()
				}
				loop.setHistory(sid, history)
				ctx := config.WithSmartAgent(context.Background(), carried)
				run := modelRun{profileName: "p", profile: profile, system: system, maxTokens: 4096}
				sizing := loop.sizeRequest(ctx, sid, run, history)
				if err := loop.compactHistory(ctx, sid, summaryOf(summary), profile, system, nil, "", CompactManual); err != nil {
					t.Fatalf("compaction: %v", err)
				}
				stored := estimateTokens(system, loop.history(sid))
				residual := sizing.afterCompaction(estimateTokens(summary, nil))
				if residual < stored || residual > stored+2 {
					t.Errorf("system %d bytes, summary %d bytes, carried %v: afterCompaction prices %d, the compaction stored %d",
						sys, sum, carried, residual, stored)
				}
			}
		}
	}
}

// The same rounding, seen from the keyboard: the notice names the longest
// summary that helps, and a compaction whose summary comes out at exactly
// that length has to leave the next request more than was sent. It did
// not, when the summary carried three bytes past its token figure.
func TestTheHedgedLengthSendsMore(t *testing.T) {
	const window = 8192
	system := strings.Repeat("p", 8003)
	history := []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(strings.Repeat("h", 7981))}}}
	loop, _, profile := replayLoop(t, summaryOf(""), window)
	const sid = "s1"
	loop.setHistory(sid, history)
	ctx := context.Background()
	run := modelRun{profileName: "p", profile: profile, system: system, maxTokens: 4096}

	sizing := loop.sizeRequest(ctx, sid, run, history)
	if sizing.sent >= sizing.wanted {
		t.Fatalf("precondition: sent %d was not shrunk under wanted %d", sizing.sent, sizing.wanted)
	}
	notice := cutOffNotice(sizing, "p", "m")
	m := regexp.MustCompile(`if the compaction's summary comes out at (\d+) tokens? or fewer`).FindStringSubmatch(notice)
	if m == nil {
		t.Fatalf("no condition in the notice:\n%s", notice)
	}
	at, _ := strconv.Atoi(m[1])
	for _, carry := range []int{0, 3} {
		summary := strings.Repeat("s", at*4+carry)
		if got := estimateTokens(summary, nil); got != at {
			t.Fatalf("precondition: the summary measures %d, not %d", got, at)
		}
		loop.setHistory(sid, history)
		if err := loop.compactHistory(ctx, sid, summaryOf(summary), profile, system, nil, "", CompactManual); err != nil {
			t.Fatalf("compaction: %v", err)
		}
		next := loop.sizeRequest(ctx, sid, run, loop.history(sid))
		if next.sent <= sizing.sent {
			t.Errorf("the notice conditioned /compact on a summary of %d tokens; one of %d bytes came out at %d and the next request asks for %d, no more than the %d cut off:\n%s",
				at, len(summary), estimateTokens(summary, nil), next.sent, sizing.sent, notice)
		}
	}
}

// The record and the history agreed about a compaction that carried
// nothing, and disagreed about one that named carried assets: the note
// was stored beside the summary live and recorded on the event only by
// name, and a restart rebuilt the summary without it.
func TestARestartRebuildsTheCarriedAssetNote(t *testing.T) {
	summary := strings.Repeat("s", 4000)
	loop, store, profile := replayLoop(t, summaryOf(summary), 200000)
	const sid = "s1"
	loop.setHistory(sid, carriedHistory())
	ctx := config.WithSmartAgent(context.Background(), true)
	if err := loop.compactHistory(ctx, sid, summaryOf(summary), profile, "", nil, "", CompactManual); err != nil {
		t.Fatalf("compaction: %v", err)
	}
	live := loop.history(sid)
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	rebuilt := rehydrateHistory(evs)
	if len(live) != 1 || len(rebuilt) != 1 || live[0].Content[0].Text != rebuilt[0].Content[0].Text {
		t.Errorf("a restart rebuilds a different summary message than the live one holds:\nlive:    %q\nrebuilt: %q", live[0].Content[0].Text, rebuilt[0].Content[0].Text)
	}
	if !strings.Contains(rebuilt[0].Content[0].Text, "spliced file internal/agent/turn.go") {
		t.Errorf("the rebuilt summary does not name what it replaced")
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
	// Exactly the header, the first bytes of the model's text that fit
	// beside the note, and the note: a stored text of the note alone
	// would pass a check of the suffix and the bound.
	text := hist[0].Content[0].Text
	if want := summaryHeader + long[:longestSummary*4-len(summaryCutNote)] + summaryCutNote; text != want {
		t.Errorf("a cut summary keeps %d bytes, want %d: header, the text that fits, and the note", len(text), len(want))
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
	for _, figure := range []string{
		fmt.Sprintf("at most %d tokens of the summary", longestSummary),
		fmt.Sprintf("the %d the request allowed", defaultMaxTokens),
	} {
		if !strings.Contains(string(usage), figure) {
			t.Errorf("USAGE.md does not state %q", figure)
		}
	}
	if err := loop.compactHistory(context.Background(), sid, summaryOf(whole), profile, "", nil, "", CompactManual); err != nil {
		t.Fatalf("compaction with a summary at the bound: %v", err)
	}
	if text := loop.history(sid)[0].Content[0].Text; text != summaryHeader+whole {
		t.Errorf("a summary of exactly %d tokens was not stored whole (%d bytes, cut=%v)", longestSummary, len(text), strings.HasSuffix(text, summaryCutNote))
	}

	// One that has to be cut is cut between runes, not through one. The
	// bound less the note happens to be a multiple of three, so a
	// three-byte repeat alone lands on a boundary without the walk-back;
	// padded by one and two bytes it does not.
	for pad := 0; pad < 3; pad++ {
		korean := strings.Repeat("a", pad) + strings.Repeat("요약", longestSummary*2)
		if err := loop.compactHistory(context.Background(), sid, summaryOf(korean), profile, "", nil, "", CompactManual); err != nil {
			t.Fatalf("compaction with a Korean summary: %v", err)
		}
		text := loop.history(sid)[0].Content[0].Text
		body := strings.TrimSuffix(strings.TrimPrefix(text, summaryHeader), summaryCutNote)
		switch {
		case !utf8.ValidString(text):
			t.Errorf("pad %d: a Korean summary was cut through a rune", pad)
		case body == text || !strings.HasPrefix(korean, body):
			t.Errorf("pad %d: the kept text is not a prefix of the summary, or the note is missing", pad)
		case len(text)-len(summaryHeader) > longestSummary*4:
			t.Errorf("pad %d: %d bytes stored, over the %d the bound allows", pad, len(text)-len(summaryHeader), longestSummary*4)
		}
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
	if room := fmt.Sprintf("the %d the shortest compaction keeps, it leaves room for %d", keeps+shortestSummary, minOutputTokens); !strings.Contains(got, room) {
		t.Errorf("does not say what the shortest compaction keeps and leaves (%q):\n%s", room, got)
	}

	// With 500 tokens more, /compact is offered on the condition that
	// the summary comes out at 500 tokens: 500 helps and 501 does not.
	s.system -= 500
	got = cutOffNotice(s, "p", "m")
	if !strings.Contains(got, "/compact makes room, if the compaction's summary comes out at 500 tokens or fewer") {
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

// The most common failure in a tool loop: the model asks for a tool and
// the stream dies before any text. The call's tool.start was on the
// record and nothing closed the reply, so a replay waited for the call's
// result and folded the next turn's iterations into it, while the live
// turn had appended nothing. The record closes the reply now, and a
// real turn against a provider that dies that way rebuilds into the
// history the live session holds.
func TestAStreamThatDiesOnAToolCallRebuildsTheSameHistory(t *testing.T) {
	ran := false
	reg := tools.NewRegistry(nil)
	reg.Register(echoTool{ran: &ran})
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventToolUseStart, ToolUseID: "t1", ToolName: "echo"},
			{Type: provider.EventToolUseEnd, ToolUseID: "t1", ToolInput: json.RawMessage(`{}`)},
			{Type: provider.EventError, Err: errors.New("the wire went quiet")},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "reading"},
			{Type: provider.EventToolUseStart, ToolUseID: "t2", ToolName: "echo"},
			{Type: provider.EventToolUseEnd, ToolUseID: "t2", ToolInput: json.RawMessage(`{}`)},
			{Type: provider.EventMessageStop, StopReason: "tool_use"},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "done"},
			{Type: provider.EventMessageStop, StopReason: "end_turn"},
		},
	}}
	loop, sid := scriptedLoop(t, p, reg)
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "go on"); err == nil {
		t.Fatal("a stream that died on a tool call was reported as a turn")
	}
	if ran {
		t.Fatal("the tool a failed reply asked for was run")
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "again"); err != nil {
		t.Fatalf("the next turn: %v", err)
	}
	evs, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	closed := false
	for _, e := range evs {
		if e.Type == events.TypeMessagePartEnd && isTrue(e.Data["failed"]) {
			closed = true
		}
	}
	if !closed {
		t.Errorf("the reply that died on a tool call was not closed on the record")
	}
	live := historyShape(loop.history(sid))
	loop.RehydrateSession(sid)
	if rebuilt := historyShape(loop.history(sid)); rebuilt != live {
		t.Errorf("live history:\n%s\n\nrebuilt after a restart:\n%s", live, rebuilt)
	}
	if want := "user: go on\nuser: again\nassistant: reading tool_use t2\nuser: tool_result t2\nassistant: done"; live != want {
		t.Errorf("live history:\n%s\nwant:\n%s", live, want)
	}
}

// A conversation read through a #S reference: a failed half reply is on
// the record, and session_read offered it as the conversation's last
// answer and listed it as a model message, with nothing saying the
// stream had died on it.
func TestSessionReadSaysWhenAReplyFailed(t *testing.T) {
	evs := []events.Event{
		ev(events.TypeUserMessage, map[string]any{"text": "go on"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "the answer is fo", "failed": true}),
		ev(events.TypeError, map[string]any{"error": "read stream: unexpected EOF"}),
	}
	s := session.Session{ID: "s2", Title: "t"}
	sum := (&SessionReadTool{}).summary(s, evs)
	if strings.Contains(sum, "Its last answer:") || !strings.Contains(sum, "0 repl") {
		t.Errorf("the summary offers a failed half reply as the last answer:\n%s", sum)
	}
	tr := (&SessionReadTool{}).transcript(s, evs, 0, 0)
	if !strings.Contains(tr, "model (the stream failed here; this was not sent back): the answer is fo") {
		t.Errorf("the transcript lists a failed half reply as a model message with no mark:\n%s", tr)
	}
}
