package agent

import (
	"context"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

func someTools(n int) []provider.Tool {
	out := make([]provider.Tool, n)
	for i := range out {
		out[i] = provider.Tool{Name: "tool" + strings.Repeat("x", i%7), Description: strings.Repeat("does a thing. ", 40)}
	}
	return out
}

// The reproduction: a count taken under a 16,000-token system prompt
// survived a switch to an agent with a short one. The next request
// carried 3% of a 32,768 window, the measure read 52%, and auto-
// compaction ran. Rebased to the request about to be sent, the count
// reads what that request carries.
func TestACountFollowsTheSystemPromptItIsSentUnder(t *testing.T) {
	const window = 32768
	big, small := strings.Repeat("p", 16000*4), "you are a coding agent"
	msgs := exchange(400)
	loop, profile := windowLoop(t, window, msgs)
	tl := someTools(3)
	m := measureCall(big, msgs[:1], tl, msgs[1].Content)
	loop.recordUsage("s1", profile.Model, window, m, streamUsage{hasUsage: true, inputTokens: m.system + m.tools + 20, outputTokens: 100})
	if pct := loop.compactionMeasure("s1", small, msgs) * 100 / window; pct < 50 {
		t.Fatalf("precondition: the count taken under the long prompt reads %d%%", pct)
	}
	loop.rebaseUsage("s1", small, tl)
	if got, want := loop.compactionMeasure("s1", small, msgs), estimateTokens(small, nil)+toolSpecTokens(tl)+20+100; got > want+5 {
		t.Errorf("under the short prompt the measure reads %d, want about %d", got, want)
	}
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, small, nil)
	if p.asked() != 0 {
		t.Error("auto-compaction ran on a count of the long prompt")
	}
	// The next request's estimate, which sizes its reply, reads the same.
	if got := loop.inputEstimate("s1", small, msgs); got > 200+toolSpecTokens(tl) {
		t.Errorf("the next request is sized as %d tokens in", got)
	}
	// And what is appended after the switch is counted from the first
	// token: the measurement moved with the count, so the old prompt's
	// length does not hide it.
	rebased := loop.compactionMeasure("s1", small, msgs)
	grown := append(append([]provider.Message(nil), msgs...), provider.Message{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(strings.Repeat("g", 2000*4))}})
	if got := loop.compactionMeasure("s1", small, grown) - rebased; got < 2000 {
		t.Errorf("2,000 tokens appended after the switch added %d to the measure", got)
	}
}

// Tool definitions are in the count and in no estimate, so a shorter
// tool list was invisible even in principle: MCP tools removed left the
// count of the longer list deciding. A longer list is counted in.
func TestACountFollowsTheToolsItIsSentWith(t *testing.T) {
	const window = 32768
	msgs := exchange(400)
	loop, profile := windowLoop(t, window, msgs)
	many := someTools(60)
	m := measureCall(autoCompactSystem, msgs[:1], many, msgs[1].Content)
	loop.recordUsage("s1", profile.Model, window, m, streamUsage{hasUsage: true, inputTokens: m.system + m.tools + 20, outputTokens: 100})
	before := loop.compactionMeasure("s1", autoCompactSystem, msgs)
	loop.rebaseUsage("s1", autoCompactSystem, nil)
	after := loop.compactionMeasure("s1", autoCompactSystem, msgs)
	if before-after != toolSpecTokens(many) {
		t.Errorf("sixty tools removed took %d off the measure, want %d", before-after, toolSpecTokens(many))
	}
	loop.rebaseUsage("s1", autoCompactSystem, many)
	if again := loop.compactionMeasure("s1", autoCompactSystem, msgs); again != before {
		t.Errorf("the tools put back read %d, first %d", again, before)
	}
	// Nothing changed, nothing moves.
	loop.rebaseUsage("s1", autoCompactSystem, many)
	if again := loop.compactionMeasure("s1", autoCompactSystem, msgs); again != before {
		t.Errorf("a rebase with nothing changed moved the measure to %d from %d", again, before)
	}
}

// OutputTokens is completion_tokens, which on a reasoning model includes
// the reasoning, and the reasoning is not sent back. The measures read
// what is: the server's reasoning count taken off where it says, the
// streamed reasoning's estimate where it does not, and never under the
// reply's own estimate. /usage still counts all of it.
func TestTheMeasuresLeaveOutReasoningNotSentBack(t *testing.T) {
	const window = 32768
	reply := []provider.Block{
		{Type: provider.BlockThinking, Text: strings.Repeat("t", 3000*4), Signature: "s"},
		provider.TextBlock(strings.Repeat("a", 100*4)),
	}
	msgs := exchange(400)
	for _, c := range []struct {
		name      string
		reasoning int
		output    int
		want      int
	}{
		{"the server counts the reasoning", 2950, 3100, 150},
		{"it does not: the streamed reasoning is estimated", 0, 3500, 3500 - 3000},
		{"never under the reply's own estimate", 3100, 3100, estimateTokens("", []provider.Message{{Role: provider.RoleAssistant, Content: reply[1:]}})},
	} {
		loop, profile := windowLoop(t, window, msgs)
		if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
		m := measureCall(autoCompactSystem, msgs[:1], nil, reply)
		loop.recordUsage("s1", profile.Model, window, m, streamUsage{hasUsage: true, inputTokens: 500, outputTokens: c.output, reasoning: c.reasoning})
		u, _ := loop.getUsage("s1")
		if u.carriedOutput() != c.want {
			t.Errorf("%s: carried %d, want %d", c.name, u.carriedOutput(), c.want)
		}
		if got := loop.compactionMeasure("s1", autoCompactSystem, withoutReasoning(append(msgs[:1:1], provider.Message{Role: provider.RoleAssistant, Content: reply}))); got != 500+c.want {
			t.Errorf("%s: the measure reads %d, want %d", c.name, got, 500+c.want)
		}
		loop.mu.Lock()
		billed := loop.cumulativeUsage["s1"][profile.Model].OutputTokens
		loop.mu.Unlock()
		if billed != c.output {
			t.Errorf("%s: /usage counts %d output, want the %d billed", c.name, billed, c.output)
		}
		all, _ := loop.Store.Events("s1", 0)
		if len(all) == 0 {
			t.Fatalf("%s: no usage event", c.name)
		}
		for _, ev := range all {
			if ev.Type == events.TypeUsage {
				if pct := dataFloat(ev.Data, "percent"); int(pct*float64(window)/100+0.5) != 500+c.want {
					t.Errorf("%s: the gauge reads %.2f%%, want %d tokens of %d", c.name, pct, 500+c.want, window)
				}
			}
		}
	}
}

// A restored count is the live one: its fixed part and what the reply
// carries come back from the log, and it rebases as the live one does.
// A count from a log that never recorded them is left as it was.
func TestACountsBasisSurvivesARestart(t *testing.T) {
	const window = 32768
	msgs := exchange(400)
	loop, profile := windowLoop(t, window, msgs)
	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	reply := []provider.Block{{Type: provider.BlockThinking, Text: strings.Repeat("t", 2000*4)}, provider.TextBlock("ok")}
	m := measureCall(strings.Repeat("p", 8000*4), msgs[:1], someTools(5), reply)
	loop.recordUsage("s1", profile.Model, window, m, streamUsage{hasUsage: true, inputTokens: 9000, outputTokens: 2100, reasoning: 2050})
	live, _ := loop.getUsage("s1")
	all, _ := loop.Store.Events("s1", 0)
	restored, ok, _ := rehydrateUsage(all)
	if !ok || restored.Known != live.Known || restored.CarriedKnown != live.CarriedKnown ||
		restored.MeasuredSystem != live.MeasuredSystem || restored.MeasuredTools != live.MeasuredTools || restored.Carried != live.Carried {
		t.Errorf("live %+v, restored %+v", live, restored)
	}
	old := []events.Event{{Type: events.TypeUsage, Data: map[string]any{"input_tokens": 9000, "output_tokens": 2100, "measured": 9100}}}
	if u, _, _ := rehydrateUsage(old); u.Known || u.CarriedKnown || u.carriedOutput() != 2100 {
		t.Errorf("a count from an older log reads as %+v", u)
	}
}

// Through a turn: a count of the long prompt's agent, then a message to
// an agent with a short one. The turn is rebased before auto-compaction
// decides, so no summarization goes out ahead of it.
func TestASwitchToAShortPromptDoesNotCompact(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.CreateSession("s1", "", "long", true); err != nil {
		t.Fatal(err)
	}
	const window = 32768
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:  map[string]config.Profile{"p": {Provider: "local", Model: "m", ContextWindow: window}},
		Agents: map[string]config.AgentConfig{
			"long":  {Profile: "p", Prompt: strings.Repeat("Follow the house rules. ", 16000*4/24)},
			"short": {Profile: "p"},
		},
		DefaultProfile: "p",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	reply := func(input int) []provider.StreamEvent {
		return []provider.StreamEvent{
			{Type: provider.EventTextDelta, TextDelta: "done"},
			{Type: provider.EventUsage, InputTokens: input, OutputTokens: 20},
			{Type: provider.EventMessageStop, StopReason: "end_turn"},
		}
	}
	p := &scriptedProvider{turns: [][]provider.StreamEvent{reply(17500), reply(1500), reply(1600)}}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": p}, cfg)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)
	if err := loop.SendMessage(context.Background(), "s1", "long", "read the build log "+strings.Repeat("x", 2000)); err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(context.Background(), "s1", "short", "and now?"); err != nil {
		t.Fatal(err)
	}
	if n := p.sentCount(); n != 2 {
		t.Fatalf("%d requests for two turns: a summarization went out ahead of the second", n)
	}
}

// The server's reasoning count reaches the measure through a turn, and
// where the server gives none, the streamed reasoning's estimate does:
// the usage event of the stream and the reasoning block, not only a
// hand-built count. The adapters put the whole reasoning on its end.
func TestAReasoningCountReachesTheMeasure(t *testing.T) {
	for _, c := range []struct {
		name      string
		reasoning int
		want      int
	}{
		{"counted by the server", 3000, 100 + blockFraming/4},
		{"estimated from the stream", 0, 3100 - 1000},
	} {
		p := &scriptedProvider{turns: [][]provider.StreamEvent{{
			{Type: provider.EventThinkingDelta, ThinkingDelta: strings.Repeat("t", 4000)},
			{Type: provider.EventThinkingEnd, ThinkingDelta: strings.Repeat("t", 4000)},
			{Type: provider.EventTextDelta, TextDelta: strings.Repeat("a", 400)},
			{Type: provider.EventUsage, InputTokens: 500, OutputTokens: 3100, ReasoningTokens: c.reasoning},
			{Type: provider.EventMessageStop, StopReason: "end_turn"},
		}}}
		loop, sid := scriptedLoop(t, p, tools.NewRegistry(nil))
		if err := loop.SendMessage(context.Background(), sid, "general-purpose", "think hard"); err != nil {
			t.Fatal(err)
		}
		u, ok := loop.getUsage(sid)
		if !ok || !u.CarriedKnown || u.carriedOutput() != c.want {
			t.Errorf("%s: the turn's count carries %d of 3,100 output tokens, want %d (%+v)", c.name, u.carriedOutput(), c.want, u)
		}
	}
}
