package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/modelinfo"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

func TestClampMaxTokensLeavesRoomForTheInput(t *testing.T) {
	// The reported case: a 131072-token window, 67073 tokens of history,
	// and a configured max_tokens of 64000. 67073+64000 is one token over,
	// and the server refuses the whole request.
	got := clampMaxTokens(64000, 131072, 67073)
	if got+67073+contextHeadroom > 131072 {
		t.Errorf("clamped max_tokens %d still overflows: %d+%d > %d", got, got, 67073, 131072)
	}
	if got >= 64000 {
		t.Errorf("clamped max_tokens = %d, expected it to be reduced", got)
	}
}

func TestClampMaxTokensLeavesAFittingRequestAlone(t *testing.T) {
	if got := clampMaxTokens(4096, 200000, 1000); got != 4096 {
		t.Errorf("clamped a request that already fits: %d, want 4096", got)
	}
	// No window figure means no opinion — never make a request smaller
	// than configured just because the model is unrecognised.
	if got := clampMaxTokens(4096, 0, 1000); got != 4096 {
		t.Errorf("clamped without a window figure: %d, want 4096", got)
	}
}

func TestClampMaxTokensStopsAtTheFloor(t *testing.T) {
	// A history that has eaten the window: an answer of a few tokens is
	// not an answer, so the floor holds and the overflow recovery takes
	// over instead.
	if got := clampMaxTokens(4096, 8192, 8000); got != minOutputTokens {
		t.Errorf("clamped to %d, want the floor %d", got, minOutputTokens)
	}
	// The floor stops a request shrinking. It is not a reason to send
	// more than the profile asked for.
	if got := clampMaxTokens(512, 8192, 8000); got != 512 {
		t.Errorf("a profile asking for 512 sent %d on a full window, want 512", got)
	}
}

// The provider's count describes the messages it was asked about, and
// nothing else.
//
// Two halves, and each is there because the other cannot do its job.
// The count comes from the tokenizer that will refuse the next request,
// so nothing here beats it for the messages it covers. But a tool
// result appended after it is invisible to it, and a tool result is
// capped at a quarter of the window, so one of them can outweigh the
// whole conversation the count was taken over.
func TestTheInputEstimateSeesWhatTheCountCouldNot(t *testing.T) {
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: 32768}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	const sid = "s1"

	// The messages that were sent, as the count describes them. The
	// count is deliberately far above what characters suggest, which is
	// the case the ratio gets wrong: whitespace-padded code counts
	// fewer tokens per character than prose, and prose in Korean counts
	// many more.
	sent := []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("read the file")}}}
	// The reply is Korean: 400 syllables the provider counted as 400
	// tokens, which a character count would price at a third of that.
	// The count is the truth for it, and the measurement covers it.
	reply := provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("가", 400))}}
	withReply := append(append([]provider.Message(nil), sent...), reply)
	measured := estimateTokens("", withReply)
	loop.mu.Lock()
	loop.usage[sid] = sessionUsage{InputTokens: 5000, OutputTokens: 400, Measured: measured}
	loop.mu.Unlock()

	// Nothing appended since: the count stands, exactly, for the
	// messages and for the reply, and the character sum is not
	// consulted for either.
	if got := loop.inputEstimate(sid, "", withReply); got != 5400 {
		t.Errorf("with nothing appended since the count, the estimate is %d, want the count for the messages and the reply, 5400", got)
	}

	// A tool result on top of it, which is the addition that can
	// outweigh everything the count covered, and the one thing that is
	// estimated.
	withTool := append(append([]provider.Message(nil), withReply...),
		provider.Message{Role: provider.RoleUser, Content: []provider.Block{
			provider.ToolResultBlock("t1", strings.Repeat("x", 4*26000), false)}})
	wantTool := 5400 + estimateTokens("", withTool) - measured
	if wantTool-5400 < 26000 {
		t.Fatalf("precondition: the addition measures %d tokens, want the tool result to dominate", wantTool-5400)
	}
	if got := loop.inputEstimate(sid, "", withTool); got != wantTool {
		t.Errorf("a tool result appended after the count left the estimate at %d, want %d", got, wantTool)
	}

	// A history that measures less than the count covered cannot make
	// the estimate smaller than the count: the difference is floored,
	// because every place that shrinks a history drops the count and a
	// negative delta would mean the count is stale in the other direction.
	loop.mu.Lock()
	loop.usage[sid] = sessionUsage{InputTokens: 5000, OutputTokens: 400, Measured: measured + 10_000}
	loop.mu.Unlock()
	if got := loop.inputEstimate(sid, "", withReply); got != 5400 {
		t.Errorf("a measurement above the conversation pulled the estimate to %d, want the count itself, 5400", got)
	}
	loop.mu.Lock()
	loop.usage[sid] = sessionUsage{InputTokens: 5000, OutputTokens: 400, Measured: measured}
	loop.mu.Unlock()

	// A prompt served wholly from the cache counts nothing fresh, and is
	// still a count: input_tokens 0 beside a cached prefix is not "no
	// usage yet".
	loop.mu.Lock()
	loop.usage[sid] = sessionUsage{InputTokens: 0, CachedInputTokens: 5000, OutputTokens: 400, Measured: measured}
	loop.mu.Unlock()
	if got := loop.inputEstimate(sid, "", withReply); got != 5400 {
		t.Errorf("a wholly cached prompt gave %d, want the cached count plus the reply, 5400: input_tokens 0 was read as no count", got)
	}
	loop.mu.Lock()
	loop.usage[sid] = sessionUsage{InputTokens: 5000, OutputTokens: 400, Measured: measured}
	loop.mu.Unlock()

	// An image appended after the count is an addition too, and not
	// one a character count can see.
	withImage := append(append([]provider.Message(nil), withReply...),
		provider.Message{Role: provider.RoleUser, Content: []provider.Block{provider.ImageBlock("image/png", make([]byte, 1<<20))}})
	if got := loop.inputEstimate(sid, "", withImage); got < 5400+imageTokenEstimate {
		t.Errorf("an image appended after the count moved the estimate to %d, want at least %d", got, 5400+imageTokenEstimate)
	}

	// A count with no measurement behind it is one this version did not
	// write: every session restored from a log older than that key has
	// one, until its next turn. Adding the conversation to it would
	// count the part they share twice and clamp the reply to the floor,
	// so those take the larger of the two instead.
	loop.mu.Lock()
	loop.usage[sid] = sessionUsage{InputTokens: 5000, OutputTokens: 100}
	loop.mu.Unlock()
	if got, want := loop.inputEstimate(sid, "", sent), 5100; got != want {
		t.Errorf("an unmeasured count on a short conversation gave %d, want the count plus the reply, %d", got, want)
	}
	if got, want := loop.inputEstimate(sid, "", withTool), estimateTokens("", withTool); got != want {
		t.Errorf("an unmeasured count on a long conversation gave %d, want what the conversation measures, %d", got, want)
	}
}

// A count describes messages. A history that no longer holds them has
// to drop it.
//
// Every place that replaces a history clears the count, except that
// collapsing a debate did not: it swaps the rounds for a summary and
// left the count describing rounds that are no longer sent. The next
// request was then sized against a conversation that had gone.
func TestCollapsingADebateDropsTheCountItInvalidates(t *testing.T) {
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: 16384}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	const sid = "s1"
	const task = "decide the retry policy"

	loop.setHistory(sid, []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("hello")}},
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(task)}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("round one deliberation. ", 400))}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("final answer text")}},
	})
	// The count covered the rounds, because the rounds were sent.
	sentMeasure := estimateTokens("", loop.history(sid))
	loop.mu.Lock()
	loop.usage[sid] = sessionUsage{InputTokens: 12900, OutputTokens: 100, Measured: sentMeasure}
	loop.mu.Unlock()

	if collapsed, _ := loop.collapseDebate(debateRun{sessionID: sid, historyMark: 1, task: task}); !collapsed {
		t.Fatal("precondition: the debate did not collapse")
	}
	msgs := loop.history(sid)
	measures := estimateTokens("", msgs)
	if measures >= 1000 {
		t.Fatalf("precondition: the collapsed conversation measures %d tokens, want it much smaller than the count", measures)
	}
	if got := loop.inputEstimate(sid, "", msgs); got != measures {
		t.Errorf("after the collapse the estimate is %d, want the %d the collapsed conversation measures: the count describes rounds that are no longer sent", got, measures)
	}
}

// A prompt cache does not make a conversation smaller.
//
// Where the cache is working the provider reports the prefix it served
// apart from what it counted fresh, and InputTokens covers only the
// suffix: this repo's fixture has input_tokens 12 beside
// cache_read_input_tokens 4096. Everything about the window wants them
// together. Read apart, a nearly full conversation looked almost empty:
// the next request was sized as though the prefix were not there, the
// gauge read near zero, and auto-compaction never fired.
func TestACachedPrefixStillFillsTheWindow(t *testing.T) {
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: 32768}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	const sid = "s1"
	sent := []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("carry on")}}}
	loop.setHistory(sid, sent)

	// What a cached turn reports: almost nothing fresh, the conversation
	// itself served from the cache.
	loop.recordUsage(sid, "m", 32768, measurement{tokens: estimateTokens("", sent)},
		streamUsage{hasUsage: true, inputTokens: 12, outputTokens: 9, cacheRead: 4096, cacheWrite: 128})

	u, ok := loop.getUsage(sid)
	if !ok {
		t.Fatal("no usage recorded")
	}
	if got, want := u.promptTokens(), 12+4096+128; got != want {
		t.Errorf("the prompt measured %d tokens, want %d: the cached prefix is part of what the window holds", got, want)
	}
	if got, want := loop.inputEstimate(sid, "", sent), u.promptTokens()+u.OutputTokens; got != want {
		t.Errorf("the next request is sized against %d tokens, want the %d the prompt actually carried plus the reply", got, want)
	}

	// And it survives the log, or a restored session sizes against the
	// suffix alone for ever.
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	const rsid = "s2"
	if _, err := store.CreateSession(rsid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	logged := New(store, tools.NewRegistry(nil), map[string]provider.Provider{}, &config.Config{})
	logged.recordUsage(rsid, "m", 32768, measurement{tokens: 5},
		streamUsage{hasUsage: true, inputTokens: 12, outputTokens: 9, cacheRead: 4096, cacheWrite: 128})
	evs, err := store.Events(rsid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	back, have, _ := rehydrateUsage(evs)
	if !have {
		t.Fatal("no usage event was written")
	}
	if got, want := back.promptTokens(), 12+4096+128; got != want {
		t.Errorf("read back a prompt of %d tokens, want %d", got, want)
	}
	// The billed figure stays what was billed at the full rate.
	if back.InputTokens != 12 {
		t.Errorf("read back InputTokens = %d, want the 12 that were counted fresh", back.InputTokens)
	}
	// And the percent both clients draw their gauge from is of the whole
	// prompt, not the suffix: (12+4096+128+9)/32768.
	var percent float64
	for _, e := range evs {
		if e.Type == events.TypeUsage {
			percent, _ = e.Data["percent"].(float64)
		}
	}
	if want := float64(12+4096+128+9) / 32768 * 100; percent < want-0.01 || percent > want+0.01 {
		t.Errorf("the usage event says %.2f%% of the window is in use, want %.2f%%: the gauge would read near empty on a cached session", percent, want)
	}
}

// countingProvider answers nothing and remembers that it was asked, so a
// test can tell "declined to compact" from "tried and failed".
type countingProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *countingProvider) Chat(context.Context, provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return nil, errors.New("not today")
}

func (p *countingProvider) asked() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// Auto-compaction decides from the same count, and a cached prefix used
// to be invisible to it: a session that was 90% cached read as under the
// threshold and never compacted, which is the one moment compaction is
// for.
func TestAutoCompactionCountsTheCachedPrefix(t *testing.T) {
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: 32768}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)
	const sid = "s1"
	// Something to compact, put in place before the count: replacing the
	// history drops the count, which is the right thing everywhere but
	// in a test that is about to set one by hand.
	loop.setHistory(sid, []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("a long conversation")}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("that went on")}},
	})

	// Under the threshold whichever way it is counted: not asked.
	quiet := &countingProvider{}
	setTestUsage(loop, sid, sessionUsage{InputTokens: 1_000, CachedInputTokens: 2_000, OutputTokens: 100, MaxContext: 32768})
	loop.maybeAutoCompact(context.Background(), sid, quiet, profile, "", nil)
	if quiet.asked() != 0 {
		t.Errorf("compaction was attempted at %d%% of the window", (1000+2000+100)*100/32768)
	}

	// Over the threshold only once the cached prefix is counted: asked.
	busy := &countingProvider{}
	setTestUsage(loop, sid, sessionUsage{InputTokens: 1_000, CachedInputTokens: 20_000, OutputTokens: 100, MaxContext: 32768})
	loop.maybeAutoCompact(context.Background(), sid, busy, profile, "", nil)
	if busy.asked() == 0 {
		t.Errorf("a window %d%% full was not compacted: the cached prefix was not counted", (1000+20000+100)*100/32768)
	}
}

// The invariant behind the case above, held where it cannot be
// forgotten: replacing a history drops the count, whatever the reason
// for replacing it.
//
// It used to be a separate call beside each replacement, and the one
// that collapses a debate did not have it. This walks the reasons rather
// than the call sites, so the next replacement is covered by the thing
// every replacement goes through rather than by remembering.
func TestReplacingAHistoryDropsTheCount(t *testing.T) {
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: 16384}
	for _, replacement := range []struct {
		name string
		with []provider.Message
	}{
		{"a compaction, leaving a summary", []provider.Message{
			{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("summary of what came before")}}}},
		{"a clear, leaving nothing", nil},
		{"a trim, leaving the end of it", []provider.Message{
			{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("the last thing said")}}}},
	} {
		t.Run(replacement.name, func(t *testing.T) {
			loop := probeTestLoop(t, &probeCounter{found: false}, profile)
			const sid = "s1"
			before := []provider.Message{{Role: provider.RoleUser,
				Content: []provider.Block{provider.TextBlock(strings.Repeat("a long conversation. ", 500))}}}
			loop.setHistory(sid, before)
			loop.mu.Lock()
			loop.usage[sid] = sessionUsage{InputTokens: 12900, OutputTokens: 100, Measured: estimateTokens("", before)}
			loop.mu.Unlock()
			if got := loop.inputEstimate(sid, "", before); got != 12900+100 {
				t.Fatalf("precondition: the count is not in force, estimate = %d", got)
			}

			loop.setHistory(sid, replacement.with)
			measures := estimateTokens("", replacement.with)
			if got := loop.inputEstimate(sid, "", replacement.with); got != measures {
				t.Errorf("after %s the estimate is %d, want the %d the new history measures", replacement.name, got, measures)
			}
		})
	}
}

// The measurement has to survive a restart, or every session read back
// from disk takes the fallback above for ever rather than until its next
// turn.
func TestTheMeasurementSurvivesBeingReadBackFromTheLog(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{}, &config.Config{})

	loop.recordUsage(sid, "m", 32768, measurement{tokens: 4321}, streamUsage{hasUsage: true, inputTokens: 5000, outputTokens: 100})

	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	latest, have, _ := rehydrateUsage(evs)
	if !have {
		t.Fatal("no usage event was written")
	}
	if latest.Measured != 4321 {
		t.Errorf("read back Measured = %d, want 4321: a restored session cannot tell a count that covers everything from one that predates a tool result", latest.Measured)
	}
	if latest.InputTokens != 5000 || latest.OutputTokens != 100 {
		t.Errorf("read back %d in / %d out, want 5000 / 100", latest.InputTokens, latest.OutputTokens)
	}
}

func TestIsContextOverflowRecognizesTheProviders(t *testing.T) {
	overflow := []string{
		`openai-compat endpoint returned 400: {"error":{"message":"litellm.ContextWindowExceededError: This model's maximum context length is 131072 tokens. However, you requested 64000 output tokens and your prompt contains at least 67073 input tokens"}}`,
		"prompt is too long: 210000 tokens > 200000 maximum",
		"ValidationException: input length and `max_tokens` exceed context limit",
		"Please reduce the length of the messages or completion",
	}
	for _, msg := range overflow {
		if !isContextOverflow(errors.New(msg)) {
			t.Errorf("not recognised as an overflow: %q", msg)
		}
	}
	other := []string{
		"openai-compat endpoint returned 401: invalid api key",
		"dial tcp 127.0.0.1:1234: connect: connection refused",
		"model not found",
	}
	for _, msg := range other {
		if isContextOverflow(errors.New(msg)) {
			t.Errorf("wrongly recognised as an overflow: %q", msg)
		}
	}
}

func TestFitHistoryDropsFromTheFrontAndKeepsTheEnd(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(strings.Repeat("a", 4000))}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("b", 4000))}},
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("the recent bit")}},
	}
	kept, dropped, err := fitHistory("", msgs, 1000)
	if err != nil {
		t.Fatalf("fitHistory: %v", err)
	}
	if dropped != 2 {
		t.Errorf("dropped %d messages, want 2", dropped)
	}
	if len(kept) != 1 || kept[0].Content[0].Text != "the recent bit" {
		t.Errorf("kept the wrong end: %+v", kept)
	}
}

// A trim must not open on a tool_result whose tool_use it just dropped:
// providers reject that pairing, so a rescue that produced one would have
// rescued nothing.
func TestFitHistoryDoesNotOpenOnAnOrphanedToolResult(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(strings.Repeat("a", 8000))}},
		{Role: provider.RoleAssistant, Content: []provider.Block{{
			Type: provider.BlockToolUse, ToolUseID: "t1", ToolName: "bash", ToolInput: json.RawMessage(`{}`),
		}}},
		{Role: provider.RoleUser, Content: []provider.Block{provider.ToolResultBlock("t1", "ok", false)}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("done")}},
	}
	kept, _, err := fitHistory("", msgs, 100)
	if err != nil {
		t.Fatalf("fitHistory: %v", err)
	}
	if startsWithToolResult(kept[0]) {
		t.Error("trimmed history begins with an orphaned tool_result")
	}
}

// /compact has to work on a conversation that is already too big — that
// is the only situation anyone types it in.
//
// It used to send the whole history to be summarized, so the command that
// exists to rescue an overflowing session was refused by that session with
// "compaction failed: ... maximum context length is N tokens". Auto-
// compaction failed the same way, without even saying so.
func TestCompactWorksOnAHistoryThatNoLongerFits(t *testing.T) {
	const limitChars = 4000
	var refusals, accepted int
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		json.NewDecoder(r.Body).Decode(&raw)
		encoded, _ := json.Marshal(raw["messages"])

		mu.Lock()
		tooBig := len(encoded) > limitChars
		if tooBig {
			refusals++
		} else {
			accepted++
		}
		mu.Unlock()

		if tooBig {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 1024 tokens."}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"a summary"}}]}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	// A window small enough that fitHistory has to throw most of the
	// conversation away to get under it.
	profile := config.Profile{Provider: "local", Model: "small-model", ContextWindow: defaultMaxTokens + contextHeadroom + 250}
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL}},
		Profiles:       map[string]config.Profile{"small": profile},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "small"}},
		DefaultProfile: "small",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"local": provider.NewOpenAICompat(srv.URL, ""),
	}, cfg)

	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := range 20 {
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Block{provider.TextBlock(fmt.Sprintf("%d %s", i, strings.Repeat("y", 400)))},
		})
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Block{provider.TextBlock("ok")},
		})
	}

	if err := loop.compactHistory(context.Background(), sid, loop.Providers["local"], profile, "", compactSystemBlocks(""), "", CompactManual); err != nil {
		t.Fatalf("compaction failed on an over-long history: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if refusals != 0 {
		t.Errorf("the summarization request was refused %d times; it should have been trimmed to fit", refusals)
	}
	if accepted == 0 {
		t.Error("no summarization request was made")
	}
}

// overflowThenSucceedServer refuses the first request the way a
// context-window overflow is actually reported, and answers every request
// after that. It records each request so the test can see that the retry
// carried a shorter history than the attempt that failed.
func overflowThenSucceedServer(t *testing.T) (*httptest.Server, func() []int) {
	t.Helper()
	var mu sync.Mutex
	var inputLens []int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)

		mu.Lock()
		n := len(inputLens)
		inputLens = append(inputLens, len(body.Messages))
		mu.Unlock()

		if n == 0 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 8192 tokens. However, you requested 4096 output tokens and your prompt contains at least 9000 input tokens."}}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, c := range []string{
			`{"choices":[{"delta":{"content":"recovered"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))

	return srv, func() []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), inputLens...)
	}
}

// A turn refused for being too long must compact and retry, not end.
//
// Before this, the 400 went straight into the transcript and the turn was
// over — with the meter reading half full, because what overflowed was the
// history plus the output the request reserved room for. Every later
// message failed the same way, so the session was finished.
func TestTurnRecoversFromAContextOverflow(t *testing.T) {
	srv, requests := overflowThenSucceedServer(t)
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL},
		},
		Profiles: map[string]config.Profile{
			"small": {Provider: "local", Model: "small-model", ContextWindow: 8192},
		},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "small"}},
		DefaultProfile: "small",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"local": provider.NewOpenAICompat(srv.URL, ""),
	}, cfg)

	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	// Enough history that there is something to summarize.
	for i := range 6 {
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Block{provider.TextBlock(fmt.Sprintf("message %d: %s", i, strings.Repeat("x", 200)))},
		})
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Block{provider.TextBlock("ok")},
		})
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "carry on"); err != nil {
		t.Fatalf("SendMessage returned an error instead of recovering: %v", err)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var text strings.Builder
	sawRecovery, sawCompaction := false, false
	for _, ev := range all {
		switch ev.Type {
		case "message.part.delta":
			if s, ok := ev.Data["text"].(string); ok {
				text.WriteString(s)
			}
		case "error":
			if ev.Data["recovered"] == true {
				sawRecovery = true
			}
		case "compacted":
			sawCompaction = true
		}
	}
	if !sawRecovery {
		t.Error("no event told the user the conversation was being summarized to recover")
	}
	if !sawCompaction {
		t.Error("the history was never compacted")
	}
	if got := text.String(); !strings.Contains(got, "recovered") {
		t.Errorf("final text = %q, want the answer from after the retry", got)
	}

	reqs := requests()
	if len(reqs) < 3 {
		t.Fatalf("expected refuse + summarize + retry, got %d requests", len(reqs))
	}
	if reqs[len(reqs)-1] >= reqs[0] {
		t.Errorf("the retry carried %d messages, no shorter than the %d that were refused", reqs[len(reqs)-1], reqs[0])
	}
}

// A configured context_window has to reach every consumer of it, not just
// the one that sizes the request.
//
// Three things read "how much room is there": the meter under the prompt,
// the 80% auto-compaction trigger, and the cap on the next reply. They
// used to disagree — the first two guessed from the model name while only
// the third read the config — so setting context_window fixed the request
// and left the meter reporting a window that wasn't there.
func TestConfiguredContextWindowReachesTheMeter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":1000,"completion_tokens":100}}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	// A name the lookup table would guess 1,000,000 for, configured to the
	// 32k this server actually serves.
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL}},
		Profiles: map[string]config.Profile{
			"p": {Provider: "local", Model: "claude-opus-5", ContextWindow: 32768},
		},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "p"}},
		DefaultProfile: "p",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"local": provider.NewOpenAICompat(srv.URL, ""),
	}, cfg)

	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	all, _ := store.Events(sid, 0)
	var usage map[string]any
	for _, ev := range all {
		if ev.Type == "usage" && ev.Data["max_context"] != nil {
			usage = ev.Data
		}
	}
	if usage == nil {
		t.Fatal("no usage event carrying a window")
	}
	if got := usage["max_context"]; got != 32768 {
		t.Errorf("the meter reports a window of %v, not the configured 32768", got)
	}
	// 1100 of 32768 is 3.4%; of the guessed 1,000,000 it would read 0.1%.
	if got := usage["percent"].(float64); got < 3.3 || got > 3.5 {
		t.Errorf("percent = %v, want ~3.4 (1100 of 32768)", got)
	}
}

// A tool result is the one part of a conversation whose size nobody chose.
// read_file on a large file put the whole file into the history, and bash
// returns whatever the command printed — so `cat` on a log could exceed
// the entire window in a single message. Nothing downstream could fix
// that: summarizing sends the same message, and dropping older messages
// cannot drop the one that just arrived.
func TestAnEnormousToolResultCannotOverflowTheWindowOnItsOwn(t *testing.T) {
	const window = 8192
	huge := strings.Repeat("x", window*charsPerToken*4) // four windows of text

	capped := capToolResult(huge, window)

	if len(capped) >= len(huge) {
		t.Fatalf("result was not capped: %d bytes in, %d out", len(huge), len(capped))
	}
	if estimateTokens("", []provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.Block{provider.ToolResultBlock("t1", capped, false)},
	}}) > window {
		t.Error("one tool result still fills the whole window by itself")
	}
	if !strings.Contains(capped, "bytes omitted") {
		t.Error("the truncation is silent; the model cannot know it is looking at part of a file")
	}
	// Both ends survive: the head of a file is its structure, the tail of
	// a command is its error and exit status.
	if !strings.HasPrefix(capped, "x") || !strings.HasSuffix(capped, "x") {
		t.Error("truncation should keep the start and the end, not just one")
	}
}

// Output that fits is left exactly alone — the cap must not be something
// that quietly edits ordinary tool output.
func TestOrdinaryToolOutputIsUntouched(t *testing.T) {
	out := "PASS\nok  	localcode/internal/agent	1.790s\n"
	if got := capToolResult(out, 200000); got != out {
		t.Errorf("ordinary output was modified:\n%q\n%q", out, got)
	}
}

// forceFit is the last line of defence and it is not allowed to fail. The
// case fitHistory cannot solve is a single message bigger than the whole
// window: there is nothing to drop, because the thing that does not fit is
// what is left after dropping everything.
func TestForceFitCutsIntoASingleOversizedMessage(t *testing.T) {
	budget := 1000
	msgs := []provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.Block{provider.TextBlock(strings.Repeat("y", budget*charsPerToken*10))},
	}}

	got, changed := forceFit("", msgs, budget)

	if !changed {
		t.Fatal("forceFit reported nothing to do for a message ten times the budget")
	}
	if n := estimateTokens("", got); n > budget {
		t.Errorf("still %d tokens against a budget of %d — forceFit did not fit", n, budget)
	}
	if len(got) != 1 {
		t.Fatalf("kept %d messages, want the one that was there", len(got))
	}
	if !strings.Contains(got[0].Content[0].Text, "cut to fit") {
		t.Error("the cut is not visible in the text it cut")
	}
	// The caller's slice must not have been edited underneath it: these
	// blocks are shared with the session's stored history.
	if len(msgs[0].Content[0].Text) != budget*charsPerToken*10 {
		t.Error("forceFit modified the history it was given in place")
	}
}

// The ordinary case still prefers dropping whole messages over cutting
// into any of them.
func TestForceFitDropsWholeMessagesBeforeCutting(t *testing.T) {
	var msgs []provider.Message
	for i := range 20 {
		msgs = append(msgs, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Block{provider.TextBlock(fmt.Sprintf("%d %s", i, strings.Repeat("z", 400)))},
		})
	}

	got, changed := forceFit("", msgs, 500)

	if !changed {
		t.Fatal("nothing was trimmed")
	}
	if n := estimateTokens("", got); n > 500 {
		t.Errorf("%d tokens against a budget of 500", n)
	}
	if len(got) >= len(msgs) {
		t.Errorf("kept %d of %d messages; nothing was dropped", len(got), len(msgs))
	}
	last := got[len(got)-1].Content[0].Text
	if !strings.HasPrefix(last, "19 ") || strings.Contains(last, "cut to fit") {
		t.Errorf("the newest message should survive whole, got %.20q", last)
	}
}

// overflowAtServer refuses the requests whose indices are named and
// answers the rest, so a test can put the refusals exactly where the
// escalation has to survive them — in particular, letting the
// summarization call through so the second overflow is the one that
// arrives *after* a successful summary.
func overflowAtServer(t *testing.T, failAt map[int]bool) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := n
		n++
		mu.Unlock()

		if failAt[i] {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your prompt contains at least 90000 input tokens."}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, c := range []string{
			`{"choices":[{"delta":{"content":"answered anyway"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))

	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// A second overflow after a successful summary used to be the end of the
// session: the rescue was one-shot, so the 400 went into the transcript
// and every later message failed the same way. Summarizing again is the
// wrong answer — if the first summary did not fit, the trouble is not the
// length of the conversation — so the second attempt stops negotiating and
// forces the history to fit.
func TestASecondOverflowIsTrimmedRatherThanEndingTheSession(t *testing.T) {
	// request 0: refused. request 1: the summary, which succeeds.
	// request 2: the retry, refused again. request 3: after the forced
	// trim, succeeds.
	srv, count := overflowAtServer(t, map[int]bool{0: true, 2: true})
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL},
		},
		Profiles: map[string]config.Profile{
			"small": {Provider: "local", Model: "small-model", ContextWindow: 8192},
		},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "small"}},
		DefaultProfile: "small",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"local": provider.NewOpenAICompat(srv.URL, ""),
	}, cfg)

	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := range 10 {
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Block{provider.TextBlock(fmt.Sprintf("message %d: %s", i, strings.Repeat("x", 400)))},
		})
		loop.appendHistory(sid, provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Block{provider.TextBlock("ok")},
		})
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "carry on"); err != nil {
		t.Fatalf("the turn ended in an error instead of trimming its way through: %v", err)
	}
	if n := count(); n < 4 {
		t.Errorf("only %d requests; the escalation never reached the forced trim", n)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var text strings.Builder
	sawDrop, sawFatal := false, false
	for _, ev := range all {
		switch ev.Type {
		case "message.part.delta":
			if s, ok := ev.Data["text"].(string); ok {
				text.WriteString(s)
			}
		case "error":
			if ev.Data["recovered"] == true {
				if msg, _ := ev.Data["error"].(string); strings.Contains(msg, "oldest part") {
					sawDrop = true
				}
			} else {
				sawFatal = true
			}
		}
	}
	if sawFatal {
		t.Error("an unrecovered error reached the transcript; the turn was supposed to survive")
	}
	if !sawDrop {
		t.Error("the user was never told that part of the conversation had been dropped")
	}
	if got := text.String(); !strings.Contains(got, "answered anyway") {
		t.Errorf("final text = %q, want the answer from after the trim", got)
	}
}

// probeCounter is a provider that answers the window question and counts
// how many times it was asked.
type probeCounter struct {
	window int
	found  bool
	calls  int
	mu     sync.Mutex
}

func (p *probeCounter) ContextWindow(context.Context, string, string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.window, p.found
}

func (p *probeCounter) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func probeTestLoop(t *testing.T, p *probeCounter, profile config.Profile) *Loop {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1/v1"}},
		Profiles:       map[string]config.Profile{"p": profile},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "p"}},
		DefaultProfile: "p",
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": nil}, cfg)
	loop.ProbeContextWindow = p.ContextWindow
	return loop
}

// The name cannot answer this for a local server, and the server can. A
// model whose name matches nothing used to be handed the 128k default,
// which on a server started at 8k is 16x too high — every request sized
// against a limit that is not there.
func TestTheServersAnswerBeatsGuessingFromTheName(t *testing.T) {
	p := &probeCounter{window: 8192, found: true}
	profile := config.Profile{Provider: "local", Model: "muse-glimmer-30b"}
	loop := probeTestLoop(t, p, profile)

	if guess := contextWindow(profile); guess != modelinfo.DefaultMaxContextTokens {
		t.Fatalf("precondition: the name should be unrecognised, guessed %d", guess)
	}
	if got := loop.contextWindow(context.Background(), profile); got != 8192 {
		t.Errorf("window = %d, want the 8192 the server reported", got)
	}
}

// Configuration is someone stating a fact about their own setup. Nothing
// discovered at runtime gets to overrule it quietly.
func TestConfiguredWindowIsNotOverruledByTheServer(t *testing.T) {
	p := &probeCounter{window: 8192, found: true}
	profile := config.Profile{Provider: "local", Model: "muse-glimmer-30b", ContextWindow: 32768}
	loop := probeTestLoop(t, p, profile)

	if got := loop.contextWindow(context.Background(), profile); got != 32768 {
		t.Errorf("window = %d, want the configured 32768", got)
	}
	if p.count() != 0 {
		t.Error("the server was asked a question that was already answered in config")
	}
}

// Asked once, then remembered — including when the answer was "I don't
// say", or every turn pays for a round trip that will never succeed.
func TestTheWindowIsProbedOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		found bool
	}{{"server answers", true}, {"server does not answer", false}} {
		t.Run(tc.name, func(t *testing.T) {
			p := &probeCounter{window: 8192, found: tc.found}
			profile := config.Profile{Provider: "local", Model: "muse-glimmer-30b"}
			loop := probeTestLoop(t, p, profile)

			for range 5 {
				loop.contextWindow(context.Background(), profile)
			}
			if p.count() != 1 {
				t.Errorf("asked %d times, want once", p.count())
			}
		})
	}
}

// A server that says nothing leaves everything exactly as it was.
func TestAnUnhelpfulServerFallsBackToTheNameGuess(t *testing.T) {
	p := &probeCounter{found: false}
	profile := config.Profile{Provider: "local", Model: "claude-sonnet-5"}
	loop := probeTestLoop(t, p, profile)

	if got := loop.contextWindow(context.Background(), profile); got != 1000000 {
		t.Errorf("window = %d, want the name-based 1000000", got)
	}
}

// Where the window figure came from, which is what nobody could see.
//
// A reply cut off at an absurd length on an on-prem model prompted the
// question "did it actually ask the server?" — and nothing localcode
// printed could answer it. A server that reports nothing and a server
// that reports its real window produce the same number of the same size;
// only the source says whether 128000 was measured or made up.
func TestTheWindowSaysWhereItCameFrom(t *testing.T) {
	cases := []struct {
		name    string
		probe   *probeCounter
		profile config.Profile
		want    windowSource
	}{
		{
			name:    "stated in config",
			probe:   &probeCounter{window: 8192, found: true},
			profile: config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: 32768},
			want:    windowFromConfig,
		},
		{
			name:    "reported by the server",
			probe:   &probeCounter{window: 8192, found: true},
			profile: config.Profile{Provider: "local", Model: "DSA-Flash-CODE"},
			want:    windowFromServer,
		},
		{
			// The case the report was about: a proxy on :4000 whose
			// /v1/models names three models and says nothing about any
			// window, so the figure is the name's guess.
			name:    "guessed, because the server said nothing",
			probe:   &probeCounter{found: false},
			profile: config.Profile{Provider: "local", Model: "DSA-Flash-CODE"},
			want:    windowGuessed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loop := probeTestLoop(t, tc.probe, tc.profile)
			_, got := loop.resolveContextWindow(context.Background(), tc.profile)
			if got != tc.want {
				t.Errorf("source = %q, want %q", got, tc.want)
			}
			// Asked twice, so the cached answer has to carry its source
			// too: the second call is the one every later turn makes.
			_, again := loop.resolveContextWindow(context.Background(), tc.profile)
			if again != tc.want {
				t.Errorf("source from the cache = %q, want %q", again, tc.want)
			}
		})
	}
}

// Two limits end a reply at its length cap, and they want opposite advice.
//
// The notice gave the profile's advice to both. A reply shrunk to the
// 1024-token floor by a nearly full window read as "your max_tokens is
// 1024" on a profile that set none, and told the person to raise it —
// which changes nothing, because the next request is shrunk the same way.
// It also named the profile by its model id, which is not a key anybody
// can find in their config.json.
func TestTheCutOffNoticeBlamesTheRightLimit(t *testing.T) {
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: 32768}

	t.Run("the window squeezed it", func(t *testing.T) {
		loop := probeTestLoop(t, &probeCounter{found: false}, profile)
		run := modelRun{profileName: "itg-flash", profile: profile, maxTokens: 4096}
		// A conversation that has filled the window to within the floor.
		big := strings.Repeat("x", 4*31000)
		msgs := []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(big)}}}

		got := cutOffNotice(loop.sizeRequest(context.Background(), "s1", run, msgs), run.profileName, run.profile.Model)

		if strings.Contains(got, "raise max_tokens on") {
			t.Errorf("told to raise max_tokens when the window was the limit:\n%s", got)
		}
		if !strings.Contains(got, "context window was nearly full") {
			t.Errorf("does not say the window was the limit:\n%s", got)
		}
		if !strings.Contains(got, "will not help") {
			t.Errorf("does not warn that raising max_tokens will not help:\n%s", got)
		}
	})

	t.Run("the profile's max_tokens capped it", func(t *testing.T) {
		loop := probeTestLoop(t, &probeCounter{found: false}, profile)
		run := modelRun{profileName: "itg-flash", profile: profile, maxTokens: 4096}
		msgs := []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("short")}}}

		got := cutOffNotice(loop.sizeRequest(context.Background(), "s1", run, msgs), run.profileName, run.profile.Model)

		// This profile sets no max_tokens, so 4096 is the built-in
		// default, and there is no figure in config.json to raise:
		// only one to add. Naming the default as the profile's own
		// limit was the shape of the bug this notice was rewritten for.
		if !strings.Contains(got, "the default max_tokens of 4096") || !strings.Contains(got, "sets none") {
			t.Errorf("a cap that is the default is not named as the default:\n%s", got)
		}
		if !strings.Contains(got, "set max_tokens on that profile") || strings.Contains(got, "raise max_tokens") {
			t.Errorf("tells the person to raise a max_tokens the profile does not have:\n%s", got)
		}

		// A profile that sets one is told to raise it.
		explicit := profile
		explicit.MaxTokens = 4096
		loopSet := probeTestLoop(t, &probeCounter{found: false}, explicit)
		runSet := modelRun{profileName: "itg-flash", profile: explicit, maxTokens: 4096}
		got = cutOffNotice(loopSet.sizeRequest(context.Background(), "s1", runSet, msgs), runSet.profileName, runSet.profile.Model)
		if !strings.Contains(got, "max_tokens limit of 4096") || !strings.Contains(got, "raise max_tokens on that profile") {
			t.Errorf("a profile that set 4096 is not told to raise it:\n%s", got)
		}
	})

	t.Run("a profile limit below the floor, on a full window", func(t *testing.T) {
		// Both limits apply. What was sent is the profile's 512, and a
		// raised max_tokens would get the floor and no more.
		small := profile
		small.MaxTokens = 512
		loop := probeTestLoop(t, &probeCounter{found: false}, small)
		run := modelRun{profileName: "itg-flash", profile: small, maxTokens: 512}
		big := strings.Repeat("x", 4*31000)
		msgs := []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(big)}}}

		sizing := loop.sizeRequest(context.Background(), "s1", run, msgs)
		if sizing.sent != 512 {
			t.Errorf("the request asked for %d, want the profile's 512", sizing.sent)
		}
		got := cutOffNotice(sizing, run.profileName, run.profile.Model)
		if !strings.Contains(got, "max_tokens limit of 512") {
			t.Errorf("does not name the profile's own limit:\n%s", got)
		}
		if !strings.Contains(got, fmt.Sprintf("at most %d until /compact", minOutputTokens)) {
			t.Errorf("does not say how far raising max_tokens can go on a full window:\n%s", got)
		}
	})

	t.Run("a profile limit below the floor, with room to spare", func(t *testing.T) {
		small := profile
		small.MaxTokens = 512
		loop := probeTestLoop(t, &probeCounter{found: false}, small)
		run := modelRun{profileName: "itg-flash", profile: small, maxTokens: 512}
		msgs := []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("short")}}}

		got := cutOffNotice(loop.sizeRequest(context.Background(), "s1", run, msgs), run.profileName, run.profile.Model)
		if strings.Contains(got, "nearly full") {
			t.Errorf("mentions the window when it had room:\n%s", got)
		}
	})

	// Each case is checked against what following the advice would do
	// to the next request, as clampMaxTokens would size it, before the
	// words are: raising max_tokens a long way, compacting to an empty
	// conversation (the most /compact could free), and both. Advice to
	// make a move is only true when that move sends more.
	//
	// The next request carries the reply that was just cut off, so
	// raising is priced at input+sent. Priced at input alone, a profile
	// of 3000 on an 8192 window was told to raise, and raising made the
	// next reply shorter than the one it was complaining about.
	advice := []struct {
		name                  string
		window, wanted, input int
		raise, compact, both  bool
		says, doesNotSay      string
	}{
		{"a profile with room to spare", 32768, 4096, 1000, true, false, true, "raise max_tokens on that profile", "nearly full"},
		// The reply is the thing that closes the window. Room was 5644
		// against a 3000-token reply, so raising looked free until that
		// reply joined the history and left 2644.
		{"room for more than was sent, but not for two of it", 8192, 3000, 500, false, false, true, "needs both /compact and a higher max_tokens", "for longer answers"},
		{"a window that shrank the request", 32768, 4096, 28000, false, true, true, "/compact makes room now", "raise max_tokens on that profile"},
		{"the profile at the floor, on a full window", 32768, 1024, 31000, false, false, true, "needs both /compact and a higher max_tokens", "for longer answers"},
		{"a window with exactly the profile's figure left", 32768, 4096, 32768 - contextHeadroom - 4096, false, false, true, "needs both /compact and a higher max_tokens", "for longer answers"},
		{"a profile below the floor, room between it and the floor", 32768, 512, 32768 - contextHeadroom - 800, true, false, true, "at most 1024 until /compact", "needs both"},
		{"a profile below the floor, room exactly at the floor", 32768, 512, 32768 - contextHeadroom - minOutputTokens, true, false, true, "at most 1024 until /compact", "needs both"},
		{"a profile below the floor, on a full window", 32768, 512, 31000, true, false, true, "at most 1024 until /compact", "needs both"},
		{"a window no request can grow in, shrunk", 3072, 4096, 500, false, false, false, "cannot give a reply more", "/compact"},
		{"a window no request can grow in, at the floor", 3072, 1024, 500, false, false, false, "cannot give a reply more", "needs both"},
		// Both sends more than was sent here, but no more than raising
		// alone: /compact adds nothing, and must not be offered.
		{"a window no request can grow in, below the floor", 3072, 512, 500, true, false, true, "gets at most 1024", "/compact"},
		{"a 2048-token window", 2048, 4096, 300, false, false, false, "cannot give a reply more", "/compact"},
		{"no window figure", 0, 4096, 1000, true, false, true, "raise max_tokens on that profile", "nearly full"},
	}
	for _, c := range advice {
		t.Run(c.name, func(t *testing.T) {
			s := requestSizing{wanted: c.wanted, input: c.input, window: c.window, source: windowFromConfig,
				sent: clampMaxTokens(c.wanted, c.window, c.input)}
			for _, move := range []struct {
				name      string
				sends     int
				wantHelps bool
			}{
				{"raising max_tokens", clampMaxTokens(c.wanted*64, c.window, c.input+clampMaxTokens(c.wanted, c.window, c.input)), c.raise},
				{"/compact", clampMaxTokens(c.wanted, c.window, 0), c.compact},
				{"both", clampMaxTokens(c.wanted*64, c.window, 0), c.both},
			} {
				if helps := move.sends > s.sent; helps != move.wantHelps {
					t.Fatalf("precondition: %s sends %d after %d, want helps=%v", move.name, move.sends, s.sent, move.wantHelps)
				}
			}
			got := cutOffNotice(s, "itg-flash", "DSA-Flash-CODE")
			if !strings.Contains(got, c.says) {
				t.Errorf("does not say %q:\n%s", c.says, got)
			}
			if strings.Contains(got, c.doesNotSay) {
				t.Errorf("says %q:\n%s", c.doesNotSay, got)
			}
		})
	}

	// Where the window figure came from changes what the notice tells
	// somebody to do about it, and every case above pins one source, so
	// the two branches that read it were unasserted: swapping their
	// suffixes passed the whole table.
	t.Run("what the notice says about a window it was not told", func(t *testing.T) {
		sources := []struct {
			source windowSource
			says   string
		}{
			{windowFromConfig, "raise context_window"},
			{windowGuessed, "set context_window"},
			{windowFromServer, "set context_window"},
		}
		for _, src := range sources {
			// A window no request can grow in, which is the branch that
			// has nothing to suggest but a larger window.
			s := requestSizing{wanted: 4096, input: 500, window: 3072, source: src.source,
				sent: clampMaxTokens(4096, 3072, 500)}
			got := cutOffNotice(s, "itg-flash", "DSA-Flash-CODE")
			if !strings.Contains(got, src.says) {
				t.Errorf("a window %s does not say %q:\n%s", src.source, src.says, got)
			}
			if !strings.Contains(got, "config.json") {
				t.Errorf("a window %s says where to set it but not in what:\n%s", src.source, got)
			}
		}

		// And the branch where the window shrank the request, which
		// suggests a larger window only when it was not told one.
		for _, src := range sources {
			s := requestSizing{wanted: 4096, input: 28000, window: 32768, source: src.source,
				sent: clampMaxTokens(4096, 32768, 28000)}
			got := cutOffNotice(s, "itg-flash", "DSA-Flash-CODE")
			if want := src.source != windowFromConfig; strings.Contains(got, "set context_window") != want {
				t.Errorf("a window %s: telling them to set context_window = %v, want %v:\n%s",
					src.source, !want, want, got)
			}
		}
	})

	// The same question asked of the whole space rather than of the
	// cases somebody thought of. Every round of review on this function
	// found another combination where a move it recommended sent no more
	// than the reply that had just been cut off, so the invariant is
	// worth stating once over everything: whatever the notice tells
	// somebody to do, doing it has to change the next request.
	t.Run("every move the notice recommends sends more than was sent", func(t *testing.T) {
		windows := []int{0, 2048, 3072, 4096, 8192, 32768, 131072}
		// 8192 is there for a profile whose cap is the whole of a
		// llama.cpp window, the case where /compact was promised on a
		// first turn and would have sent less.
		wants := []int{1, 512, 1023, 1024, 1025, 3000, 4096, 8192, 64000}
		inputs := []int{0, 100, 500, 1000, 5000, 26600, 31000, 32000, 200000}
		sources := []windowSource{windowFromConfig, windowFromServer, windowGuessed}
		// What a compaction stores beside the summary: the header alone,
		// and the header with both its notes, for a history that carried
		// images and assets.
		keepses := []int{compactionKeeps(nil, false), compactionKeeps(carriedHistory(), true)}
		// The system prompt is what a compaction cannot remove, so it
		// decides what /compact can free: none, a Smart Agent prompt's
		// worth, a long one, and the sizes that put the shortest and the
		// longest compaction exactly at the floor, one token under it
		// and one over. That boundary is where "if its summary comes
		// out short" was offered for a summary of nothing, which is a
		// failed compaction, not a short one.
		systemsFor := func(window, keeps int) []int {
			systems := []int{0, 440, 2000}
			for _, summary := range []int{shortestSummary, longestSummary} {
				at := window - contextHeadroom - keeps - summary - minOutputTokens
				for _, d := range []int{-1, 0, 1} {
					if at+d >= 0 {
						systems = append(systems, at+d)
					}
				}
			}
			return systems
		}
		hedgeAt := regexp.MustCompile(`if the compaction's summary comes out at (\d+) tokens? or fewer`)
		roomFor := regexp.MustCompile(`leaves room for (\d+)`)
		// A profile that set no max_tokens has none to raise, only one
		// to set, in every branch that names the key.
		defaulteds := []bool{false, true}
		checked, want := 0, 0
		for _, window := range windows {
			for _, keeps := range keepses {
				systems := systemsFor(window, keeps)
				want += len(wants) * len(inputs) * len(sources) * len(systems) * len(defaulteds)
				for _, wanted := range wants {
					for _, input := range inputs {
						for _, source := range sources {
							for _, system := range systems {
								for _, defaulted := range defaulteds {
									s := requestSizing{wanted: wanted, input: input, window: window, system: system, keeps: keeps,
										source: source, sent: clampMaxTokens(wanted, window, input), defaulted: defaulted}
									// What each move would actually send. "Raise
									// max_tokens" names no number, so the move is
									// modelled as raising it as far as it needs to
									// go, and it is priced at the input the next
									// request carries, which includes the reply just
									// cut off. /compact leaves the system prompt,
									// what the compaction keeps and a summary of
									// unknown length behind, so it is priced by the
									// summary: helps says whether one that comes out
									// at summary tokens would let the next request,
									// capped at want, ask for more than beyond. A
									// promise has to hold at the longest summary a
									// compaction keeps; a condition has to name the
									// longest that helps, and one below the shortest
									// summary there can be is no condition at all.
									raiseSends := clampMaxTokens(math.MaxInt, window, input+s.sent)
									raiseHelps := raiseSends > s.sent
									helps := func(want, summary, beyond int) bool {
										return clampMaxTokens(want, window, s.afterCompaction(summary)) > beyond
									}

									got := cutOffNotice(s, "itg-flash", "DSA-Flash-CODE")
									where := fmt.Sprintf("window %d, max_tokens %d, input %d, system %d, keeps %d (sent %d)", window, wanted, input, system, keeps, s.sent)
									checked++
									hedged, at := false, 0
									if m := hedgeAt.FindStringSubmatch(got); m != nil {
										hedged = true
										at, _ = strconv.Atoi(m[1])
									}
									checkHedge := func(want, beyond int) {
										switch {
										case at < shortestSummary || at >= longestSummary:
											t.Errorf("%s: conditions /compact on a summary of %d tokens, outside [%d, %d):\n%s", where, at, shortestSummary, longestSummary, got)
										case !helps(want, at, beyond):
											t.Errorf("%s: conditions /compact on a summary of %d tokens, which sends no more than %d:\n%s", where, at, beyond, got)
										case helps(want, at+1, beyond):
											t.Errorf("%s: conditions /compact on a summary of %d tokens, when %d would still do:\n%s", where, at, at+1, got)
										}
									}

									switch {
									case strings.Contains(got, "cannot give a reply more"):
										if raiseHelps || helps(wanted, shortestSummary, s.sent) || helps(math.MaxInt, shortestSummary, s.sent) {
											t.Errorf("%s: says nothing can give more, but raise=%v compact(shortest)=%v both(shortest)=%v against %d:\n%s",
												where, raiseHelps, helps(wanted, shortestSummary, s.sent), helps(math.MaxInt, shortestSummary, s.sent), s.sent, got)
										}
										// The room it names is what the shortest
										// compaction leaves, which is the one figure
										// that lets a person check the claim.
										room := window - s.afterCompaction(shortestSummary) - contextHeadroom
										if room < 0 {
											room = 0
										}
										if m := roomFor.FindStringSubmatch(got); m == nil || m[1] != strconv.Itoa(room) {
											t.Errorf("%s: names a room that is not what the shortest compaction leaves (%d):\n%s", where, room, got)
										}
									case strings.Contains(got, "for longer answers"):
										if !raiseHelps {
											t.Errorf("%s: prescribes raising max_tokens, which sends %d after %d:\n%s", where, raiseSends, s.sent, got)
										}
										// When it names a cap, the cap has to be what
										// raising would actually get, and the way past
										// the cap has to be one that works.
										if cap := fmt.Sprintf("gets at most %d", raiseSends); strings.Contains(got, "gets at most") && !strings.Contains(got, cap) {
											t.Errorf("%s: names a cap that is not what raising gets (%d):\n%s", where, raiseSends, got)
										}
										past := strings.Contains(got, "until /compact makes room")
										switch {
										case past && !hedged && !helps(math.MaxInt, longestSummary, raiseSends):
											t.Errorf("%s: promises /compact past the cap, but at the longest summary it sends no more than %d:\n%s", where, raiseSends, got)
										case past && hedged:
											checkHedge(math.MaxInt, raiseSends)
										case !past && strings.Contains(got, "gets at most") && helps(math.MaxInt, shortestSummary, raiseSends):
											t.Errorf("%s: names a cap and no way past it, though /compact at the shortest summary sends more than %d:\n%s", where, raiseSends, got)
										}
									case strings.Contains(got, "needs both /compact and a"):
										if hedged {
											checkHedge(math.MaxInt, s.sent)
										} else if !helps(math.MaxInt, longestSummary, s.sent) {
											t.Errorf("%s: promises both, which at the longest summary sends no more than %d:\n%s", where, s.sent, got)
										}
										if raiseHelps || helps(wanted, shortestSummary, s.sent) {
											t.Errorf("%s: says both are needed, but raise=%v compact(shortest)=%v alone would do:\n%s",
												where, raiseHelps, helps(wanted, shortestSummary, s.sent), got)
										}
									case strings.Contains(got, "/compact makes room"):
										if hedged {
											checkHedge(wanted, s.sent)
										} else if !helps(wanted, longestSummary, s.sent) {
											t.Errorf("%s: promises /compact, which at the longest summary sends no more than %d:\n%s", where, s.sent, got)
										}
									default:
										t.Errorf("%s: the notice recommends nothing this test recognises:\n%s", where, got)
									}

									// The key it names is one the person has.
									namesTheKey := strings.Contains(got, "max_tokens on that profile") || strings.Contains(got, "max_tokens set on that profile")
									switch {
									case defaulted && (strings.Contains(got, "higher max_tokens") || strings.Contains(got, "raise max_tokens")):
										t.Errorf("%s: tells a profile that sets no max_tokens to raise it:\n%s", where, got)
									case defaulted && namesTheKey && !strings.Contains(got, "set max_tokens") && !strings.Contains(got, "max_tokens set"):
										t.Errorf("%s: names max_tokens to a profile that sets none without saying to set it:\n%s", where, got)
									case !defaulted && (strings.Contains(got, "sets none") || strings.Contains(got, "max_tokens set on")):
										t.Errorf("%s: treats a profile that set max_tokens as one that did not:\n%s", where, got)
									}

									// Anything that blames the window has to say where
									// the figure came from, and offer a larger one when
									// nobody stated it. A guess from a model name is the
									// likeliest reason a window looks full when it is
									// not, and a branch that leaves it out tells
									// somebody to compact a conversation that fits.
									if !strings.Contains(got, "window") {
										continue
									}
									if !strings.Contains(got, string(source)) {
										t.Errorf("%s, window %s: blames the window without saying where the figure came from:\n%s", where, source, got)
									}
									// Offered whenever the figure was not stated,
									// and also when nothing else helps at all:
									// there the window is the only lever there is,
									// so it is worth naming even to the person who
									// set it.
									offers := strings.Contains(got, "set context_window") || strings.Contains(got, "raise context_window")
									nothingHelps := strings.Contains(got, "cannot give a reply more")
									if want := nothingHelps || source != windowFromConfig; offers != want {
										t.Errorf("%s, window %s: offering a larger context_window = %v, want %v:\n%s", where, source, offers, want, got)
									}
								}
							}
						}
					}
				}
			}
		}
		if checked != want {
			t.Fatalf("checked %d combinations, want %d", checked, want)
		}
	})

	t.Run("a run with no profile name is named by its model", func(t *testing.T) {
		s := requestSizing{wanted: 4096, input: 1000, window: 32768, source: windowFromConfig,
			sent: clampMaxTokens(4096, 32768, 1000)}
		got := cutOffNotice(s, "", "DSA-Flash-CODE")
		if !strings.Contains(got, `"DSA-Flash-CODE"`) {
			t.Errorf("with no profile name the notice names nothing the person can find:\n%s", got)
		}
	})

	t.Run("the profile is named as it appears in config.json", func(t *testing.T) {
		loop := probeTestLoop(t, &probeCounter{found: false}, profile)
		run := modelRun{profileName: "itg-flash", profile: profile, maxTokens: 4096}
		msgs := []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("short")}}}

		got := cutOffNotice(loop.sizeRequest(context.Background(), "s1", run, msgs), run.profileName, run.profile.Model)

		if !strings.Contains(got, `"itg-flash"`) {
			t.Errorf("does not name the profile the person would look for:\n%s", got)
		}
		if strings.Contains(got, `"DSA-Flash-CODE" profile`) {
			t.Errorf("names the model id as though it were a profile:\n%s", got)
		}
	})
}

// The notice must describe the request that produced the reply, not one
// worked out again afterwards. The reply's usage is recorded before the
// notice is written, and the input it reports includes what the reply
// itself added — so a request that went out with the profile's whole
// reservation and filled it re-read as one the window had shrunk, and the
// notice said raising max_tokens would not help about the one case where
// it is the fix.
//
// The request here is small and gets the full 4096. The server then
// reports 26600 tokens of input and 4096 of output, which recomputed
// against a 32768 window leaves room for only the 1024 floor.
func TestACutOffReplyIsJudgedByTheRequestThatWasSent(t *testing.T) {
	var (
		mu            sync.Mutex
		sentMaxTokens int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sentMaxTokens = body.MaxTokens
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"choices":[{"delta":{"content":"the first part of a long answer"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"length"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":26600,"completion_tokens":4096}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"itg": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL}},
		Profiles:       map[string]config.Profile{"itg-flash": {Provider: "itg", Model: "DSA-Flash-CODE", MaxTokens: 4096, ContextWindow: 32768}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "itg-flash"}},
		DefaultProfile: "itg-flash",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"itg": provider.NewOpenAICompat(srv.URL, ""),
	}, cfg)

	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "write me a long program"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	mu.Lock()
	sent := sentMaxTokens
	mu.Unlock()
	if sent != 4096 {
		t.Fatalf("precondition: the request asked for %d tokens, want the profile's full 4096", sent)
	}
	if u, ok := loop.getUsage(sid); !ok || u.InputTokens != 26600 {
		t.Fatalf("precondition: recorded usage %+v, want the server's 26600 input tokens", u)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	said := ""
	for _, ev := range all {
		if msg, _ := ev.Data["error"].(string); ev.Type == "error" && strings.Contains(msg, "cut off") {
			said = msg
		}
	}
	if said == "" {
		t.Fatal("the reply was cut off and nothing said so")
	}
	if !strings.Contains(said, `raise max_tokens on that profile`) || !strings.Contains(said, "4096") {
		t.Errorf("a reply that filled the profile's own max_tokens was blamed on something else:\n%s", said)
	}
}
