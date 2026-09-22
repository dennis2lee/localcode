package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"localcode/internal/config"
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

		if !strings.Contains(got, "raise max_tokens") {
			t.Errorf("a profile cap does not say to raise max_tokens:\n%s", got)
		}
		if !strings.Contains(got, "4096") {
			t.Errorf("does not name the profile's actual limit:\n%s", got)
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

	// Each case below is checked against what the advice would do: the
	// request clampMaxTokens builds after following it must be larger
	// than the one that was cut off.
	advice := []struct {
		name          string
		wanted, input int
		raiseHelps    bool
		compactHelps  bool
		wantInNotice  string
		notInNotice   string
	}{
		{"the profile at the floor, on a full window", 1024, 31000, false, false, "needs both /compact and a higher max_tokens", "for longer answers"},
		{"a window with exactly the profile's figure left", 4096, 32768 - contextHeadroom - 4096, false, false, "needs both /compact and a higher max_tokens", "for longer answers"},
		{"a profile below the floor, room between it and the floor", 512, 32768 - contextHeadroom - 800, true, false, "at most 1024 until /compact", "needs both"},
		{"a profile below the floor, on a full window", 512, 31000, true, false, "at most 1024 until /compact", "needs both"},
		{"a profile with room to spare", 4096, 1000, true, false, "raise max_tokens on that profile", "nearly full"},
		{"a window that shrank the request", 4096, 28000, false, true, "Raising max_tokens will not help", "raise max_tokens on that profile"},
	}
	for _, c := range advice {
		t.Run(c.name, func(t *testing.T) {
			const window = 32768
			s := requestSizing{wanted: c.wanted, input: c.input, window: window, source: windowFromConfig,
				sent: clampMaxTokens(c.wanted, window, c.input)}
			if raised := clampMaxTokens(c.wanted*4, window, c.input); (raised > s.sent) != c.raiseHelps {
				t.Fatalf("precondition: raising max_tokens sends %d after %d, want helps=%v", raised, s.sent, c.raiseHelps)
			}
			if compacted := clampMaxTokens(c.wanted, window, 0); (compacted > s.sent) != c.compactHelps {
				t.Fatalf("precondition: /compact sends %d after %d, want helps=%v", compacted, s.sent, c.compactHelps)
			}
			got := cutOffNotice(s, "itg-flash", "DSA-Flash-CODE")
			if !strings.Contains(got, c.wantInNotice) {
				t.Errorf("does not say %q:\n%s", c.wantInNotice, got)
			}
			if strings.Contains(got, c.notInNotice) {
				t.Errorf("says %q:\n%s", c.notInNotice, got)
			}
		})
	}

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
