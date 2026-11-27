package agent

// From the review of the compaction and usage commits (codex, 2026-11-27).
// Each was a probe that failed against them, save the last two, which
// pinned what already held.

import (
	"context"
	"fmt"
	"localcode/internal/provider"
	"localcode/internal/tools"
	"strings"
	"testing"
)

func TestContextReadsTheCountRebasedAsTheNextTurnWill(t *testing.T) {
	loop := newFallbackLoop(t, "http://127.0.0.1:1")
	loop.ProbeContextWindow = nil
	profile := loop.Config.Profiles["primary"]
	profile.ContextWindow = 8192
	loop.Config.Profiles["primary"] = profile
	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	setTestUsage(loop, "s1", sessionUsage{InputTokens: 7500, Measured: 7500, MeasuredSystem: 7000, Known: true})
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "/context"); err != nil {
		t.Fatal(err)
	}
	out := lastMessagePartEnd(t, loop.Store, "s1")
	p := &scriptedProvider{turns: [][]provider.StreamEvent{{{Type: provider.EventTextDelta, TextDelta: "done"}, {Type: provider.EventMessageStop, StopReason: "end_turn"}}}}
	loop.Providers["local"] = p
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "hello"); err != nil {
		t.Fatal(err)
	}
	actual := p.sentRequests()[0].MaxTokens
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "but the next reply gets") {
			t.Fatalf("preview: %s; actual next request cap=%d", strings.TrimSpace(line), actual)
		}
	}
}

func TestARebaseKeepsACachedCountWhole(t *testing.T) {
	loop, _ := windowLoop(t, 32768, exchange(400))
	setTestUsage(loop, "s1", sessionUsage{InputTokens: 100, CachedInputTokens: 10000, Measured: 6000, MeasuredSystem: 5000, Known: true})
	loop.rebaseUsage("s1", "", nil)
	u, _ := loop.getUsage("s1")
	if u.promptTokens() != 5100 || u.Measured != 1000 {
		t.Fatalf("unexpected rebase: %+v", u)
	}
	loop.rebaseUsage("s1", "", nil)
	v, _ := loop.getUsage("s1")
	if u != v {
		t.Fatal("second rebase changed the count")
	}

}

func TestTheCutOffNoticePricesTheReplyAsCarried(t *testing.T) {
	p := &scriptedProvider{turns: [][]provider.StreamEvent{{
		{Type: provider.EventTextDelta, TextDelta: strings.Repeat("a", 384)},
		{Type: provider.EventUsage, InputTokens: 1000, OutputTokens: 4096, ReasoningTokens: 4000},
		{Type: provider.EventMessageStop, StopReason: "max_tokens"},
	}}}
	loop, sid := scriptedLoop(t, p, tools.NewRegistry(nil))
	profile := loop.Config.Profiles["balanced"]
	profile.ContextWindow = 8192
	loop.Config.Profiles["balanced"] = profile
	setTestUsage(loop, sid, sessionUsage{InputTokens: 1000})
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatal(err)
	}
	u, _ := loop.getUsage(sid)
	next := clampMaxTokens(10000, 8192, u.promptTokens()+u.carriedOutput())
	for _, notice := range recovered(t, loop, sid) {
		if strings.Contains(notice, "needs both /compact") && next > 4096 {
			t.Fatalf("carried=%d; next reply can get %d tokens by raising max_tokens alone; notice=%s", u.carriedOutput(), next, notice)
		}
	}
}

type chatFunc func(context.Context, provider.ChatRequest) (<-chan provider.StreamEvent, error)

func (f chatFunc) Chat(ctx context.Context, r provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	return f(ctx, r)
}

// A fallback in the same turn sends under its own system prompt and
// tools, and its request was sized on the first model's: rebased at the
// swap, on both paths to it (the request refused, and the stream dying
// before it wrote anything).
func TestAFallbacksRequestIsSizedOnItsOwnBasis(t *testing.T) {
	for _, midStream := range []bool{false, true} {
		fallbackSizedOnItsOwnBasis(t, midStream)
	}
}

func fallbackSizedOnItsOwnBasis(t *testing.T, midStream bool) {
	t.Helper()
	loop := newFallbackLoop(t, "http://127.0.0.1:1")
	loop.SetSmartAgentEnabled(true)
	loop.Tools.Register(tools.ViewImage{})
	backup := loop.Config.Profiles["backup"]
	backup.ContextWindow = 8192
	loop.Config.Profiles["backup"] = backup
	loop.ProbeContextWindow = nil
	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	calls := 0
	loop.Providers["local"] = chatFunc(func(ctx context.Context, r provider.ChatRequest) (<-chan provider.StreamEvent, error) {
		calls++
		if calls == 2 {
			if midStream {
				ch := make(chan provider.StreamEvent, 1)
				ch <- provider.StreamEvent{Type: provider.EventError, Err: fmt.Errorf("404 model not found")}
				close(ch)
				return ch, nil
			}
			return nil, fmt.Errorf("404 model not found")
		}
		if calls == 3 {
			u, _ := loop.getUsage("s1")
			sys, tl := estimateTokens(r.System, nil), toolSpecTokens(r.Tools)
			if u.MeasuredSystem != sys || u.MeasuredTools != tl {
				wantInput := u.promptTokens() + u.carriedOutput() + max(0, estimateTokens(r.System, withoutReasoning(r.Messages))-u.Measured-(sys-u.MeasuredSystem)) + (sys - u.MeasuredSystem) + (tl - u.MeasuredTools)
				t.Errorf("mid-stream %v, fallback %s: basis system=%d tools=%d, request system=%d tools=%d; cap=%d, want=%d after rebase", midStream, r.Model, u.MeasuredSystem, u.MeasuredTools, sys, tl, r.MaxTokens, clampMaxTokens(4096, 8192, wantInput))
			}
		}
		ch := make(chan provider.StreamEvent, 3)
		ch <- provider.StreamEvent{Type: provider.EventTextDelta, TextDelta: "done"}
		ch <- provider.StreamEvent{Type: provider.EventUsage, InputTokens: 5000, OutputTokens: 10}
		ch <- provider.StreamEvent{Type: provider.EventMessageStop, StopReason: "end_turn"}
		close(ch)
		return ch, nil
	})
	for range 2 {
		if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("mid-stream %v: expected a fallback, calls=%d", midStream, calls)
	}
}

func TestContextLeftLeavesOutReasoning(t *testing.T) {
	loop, _ := windowLoop(t, 8192, exchange(384))
	setTestUsage(loop, "s1", sessionUsage{InputTokens: 1000, OutputTokens: 7000, Carried: 100, CarriedKnown: true, MaxContext: 8192})
	got := loop.contextLeftFor("s1")
	if !strings.Contains(got, "7,092") {
		t.Fatalf("want 7,092 tokens left; got %s", got)
	}
}

func TestTheCompactThrottleOnAResolvedWindow(t *testing.T) {
	loop, profile := failLoop(t)
	profile.ContextWindow = 0
	probe := &probeCounter{window: failWindow, found: true}
	loop.ProbeContextWindow = probe.ContextWindow
	p := &emptySummaryProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	loop.mu.Lock()
	loop.messages["s1"] = exchange(9000 * 4)
	loop.mu.Unlock()
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if p.asked() != 1 {
		t.Fatalf("shrinking retried: %d", p.asked())
	}
	f, ok := loop.lastCompactFailure("s1")
	if !ok || f.key != compactKey(profile, failWindow) {
		t.Fatalf("wrong resolved key: %+v", f)
	}
}
