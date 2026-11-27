package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
)

// windowProvider refuses what vLLM and Anthropic refuse: a request whose
// input and the output it asks for are together over the window. It
// counts input on this side's estimate, so a refusal here is one the
// estimate could have avoided. Anything that fits is answered with reply.
type windowProvider struct {
	window int
	reply  string

	mu      sync.Mutex
	asked   int
	refused int
	caps    []int
}

func (p *windowProvider) Chat(_ context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked++
	p.caps = append(p.caps, req.MaxTokens)
	if in := estimateTokens(req.System, req.Messages); in+req.MaxTokens > p.window {
		p.refused++
		return nil, fmt.Errorf("400: This model's maximum context length is %d tokens. However, you requested %d tokens (%d in the messages, %d in the completion)", p.window, in+req.MaxTokens, in, req.MaxTokens)
	}
	reply := p.reply
	if reply == "" {
		reply = "a summary"
	}
	ch := make(chan provider.StreamEvent, 2)
	ch <- provider.StreamEvent{Type: provider.EventTextDelta, TextDelta: reply}
	ch <- provider.StreamEvent{Type: provider.EventMessageStop, StopReason: "end_turn"}
	close(ch)
	return ch, nil
}

func (p *windowProvider) counts() (asked, refused int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asked, p.refused
}

// windowLoop is a loop over one profile with the given window, auto-
// compaction on at 50%, and session "s1" holding msgs.
func windowLoop(t *testing.T, window int, msgs []provider.Message) (*Loop, config.Profile) {
	t.Helper()
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: window}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)
	loop.setHistory("s1", msgs)
	return loop, profile
}

func exchange(chars int) []provider.Message {
	return []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("read internal/agent/compact.go")}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("c", chars))}},
	}
}

// The reproduction: an 8,192-token window under a 4,200-token system
// prompt. The summarization asked for 4,096 tokens of output beside the
// system prompt, which no trimming of the history could make fit, and
// every attempt was refused: seven requests at the top of every turn,
// and no compaction. Now one request, sized to fit, and the compaction
// happens.
func TestAutoCompactionFitsItsRequestToASmallWindow(t *testing.T) {
	const window = 8192
	system := strings.Repeat("p", 4200*4)
	loop, profile := windowLoop(t, window, exchange(3000*4))
	p := &windowProvider{window: window}
	if !loop.maybeAutoCompact(context.Background(), "s1", p, profile, system, nil) {
		t.Error("the conversation was not compacted")
	}
	if asked, refused := p.counts(); refused != 0 || asked != 1 {
		t.Errorf("%d summarization requests, %d of them refused; want one, accepted (caps asked for: %v)", asked, refused, p.caps)
	}
}

// "/compact" of a short conversation on a 4,096-token window asked for
// 4,096 tokens of output, the whole window, and was refused: "compaction
// failed: ... maximum context length is 4096 tokens ... 4096 in the
// completion".
func TestCompactRunsOnA4096Window(t *testing.T) {
	const window = 4096
	loop, profile := windowLoop(t, window, exchange(600))
	p := &windowProvider{window: window}
	if err := loop.compactHistory(context.Background(), "s1", p, profile, "you are a coding agent", nil, "", CompactManual); err != nil {
		t.Fatalf("/compact on a %d window: %v", window, err)
	}
	if asked, refused := p.counts(); refused != 0 || asked != 1 {
		t.Errorf("%d summarization requests, %d of them refused; want one, accepted (caps: %v)", asked, refused, p.caps)
	}
	// The whole conversation went: it fitted, and nothing was dropped.
	if h := loop.history("s1"); len(h) != 1 || !strings.Contains(h[0].Content[0].Text, "a summary") {
		t.Errorf("the history after /compact: %v", h)
	}
}

// Where the system prompt and the instruction leave no room for even the
// least reply, nothing is sent: a request that cannot fit is refused
// every time, and auto-compaction would send it at the top of every turn.
func TestASummaryWithNoRoomIsNotAskedFor(t *testing.T) {
	const window = 4096
	system := strings.Repeat("p", 3000*4)
	loop, profile := windowLoop(t, window, exchange(3000*4))
	p := &windowProvider{window: window}
	err := loop.compactHistory(context.Background(), "s1", p, profile, system, nil, "", CompactManual)
	if err == nil || !strings.Contains(err.Error(), "no room for a summary in the 4096-token context window") {
		t.Errorf("/compact with no room: %v", err)
	}
	if loop.maybeAutoCompact(context.Background(), "s1", p, profile, system, nil) {
		t.Error("auto-compaction compacted with no room")
	}
	if asked, _ := p.counts(); asked != 0 {
		t.Errorf("%d requests sent that could not fit", asked)
	}
	if len(loop.history("s1")) != 2 {
		t.Error("the history was changed")
	}
}

// The summary is held to the cap its own call ran under, a quarter above
// it, not to the cap of a call on a larger window: a server that ignores
// max_tokens otherwise leaves a summary longer than the window has room
// for.
func TestASummaryIsHeldToItsOwnCallsCap(t *testing.T) {
	const window = 4096
	loop, profile := windowLoop(t, window, exchange(600))
	p := &windowProvider{window: window, reply: strings.Repeat("s", 40000)}
	if err := loop.compactHistory(context.Background(), "s1", p, profile, "", nil, "", CompactManual); err != nil {
		t.Fatal(err)
	}
	asked := p.caps[0]
	if asked >= defaultMaxTokens {
		t.Fatalf("precondition: the call on a %d window asked for %d tokens", window, asked)
	}
	text := loop.history("s1")[0].Content[0].Text
	kept := len(text) - len(summaryHeader)
	if limit := (asked + asked/4) * 4; kept > limit {
		t.Errorf("kept %d bytes of the summary; its call asked for %d tokens, so at most %d", kept, asked, limit)
	}
	if !strings.Contains(text, summaryCutNote) {
		t.Error("the cut summary does not say it was cut")
	}
}

// The cap and the budget fit the window together, and the cap is never
// more than a turn's default: across windows and system prompts, the
// request summarySize sizes is one windowProvider accepts.
func TestSummarySizeFitsTheWindow(t *testing.T) {
	for _, window := range []int{4096, 6000, 8192, 16384, 32768, 200000} {
		for _, systemTokens := range []int{0, 500, 2000, 4200, 12000} {
			for _, convChars := range []int{400, 8000, 80000, 2000000} {
				msgs := exchange(convChars)
				system := strings.Repeat("p", systemTokens*4)
				instr := instructionTokens(compactionPrompt, len(msgs))
				maxTokens, budget, err := summarySize(window, system, instr, msgs)
				if err != nil {
					if room := window - estimateTokens(system, nil) - instr - contextHeadroom; room > minOutputTokens {
						t.Errorf("window %d, system %d: refused with %d of room: %v", window, systemTokens, room, err)
					}
					continue
				}
				if maxTokens < minOutputTokens || maxTokens > defaultMaxTokens {
					t.Errorf("window %d, system %d, conversation %d: cap %d", window, systemTokens, convChars/4, maxTokens)
				}
				// A long conversation is trimmed to fit, and does not take
				// the summary's room: where there is room for both, the
				// summary keeps the whole cap.
				if room := window - estimateTokens(system, nil) - instr - contextHeadroom; room >= 2*defaultMaxTokens && maxTokens != defaultMaxTokens {
					t.Errorf("window %d, system %d, conversation %d: cap %d with %d of room", window, systemTokens, convChars/4, maxTokens, room)
				}
				kept, _, ferr := fitHistory(system, msgs, budget)
				if ferr != nil {
					kept, _ = forceFit(system, msgs, budget)
				}
				if in := estimateTokens(system, kept) + instr; in+maxTokens > window-contextHeadroom+1 {
					t.Errorf("window %d, system %d, conversation %d: %d in and %d out", window, systemTokens, convChars/4, in, maxTokens)
				}
			}
		}
	}
}

// A server that keeps refusing (its tokenizer counts four times what this
// side does) is retried with less history each time, and no longer once
// none fits beside the system prompt: a request that is the system prompt
// alone summarizes nothing, and it was sent until the attempts ran out.
func TestRetriesStopWhenNoHistoryFitsBesideTheSystemPrompt(t *testing.T) {
	const window = 32768
	system := strings.Repeat("p", 12000*4)
	loop, profile := windowLoop(t, window, exchange(200000))
	p := &windowProvider{window: 1}
	err := loop.compactHistory(context.Background(), "s1", p, profile, system, nil, "", CompactManual)
	if err == nil || !isContextOverflow(err) {
		t.Fatalf("compaction against a server that refuses everything: %v", err)
	}
	if asked, _ := p.counts(); asked >= maxCompactAttempts+1 {
		t.Errorf("%d requests, the last ones carrying no history; want the retries to stop when none fits", asked)
	}
}

// At the least reply and not a token over, no history fits beside it, and
// a history budget of nothing is what forceFit reads as no window: the
// whole history went out, a 11,148-token request on a 3,223 window. From
// the review of the summary sizing.
func TestASummaryWithRoomForNoHistoryIsNotAskedFor(t *testing.T) {
	msgs := exchange(40000)
	instr := instructionTokens(compactionPrompt, len(msgs))
	window := instr + contextHeadroom + minOutputTokens
	if _, _, err := summarySize(window, "", instr, msgs); err == nil {
		t.Error("summarySize sized a request with room for no history")
	}
	if _, budget, err := summarySize(window+1, "", instr, msgs); err != nil || budget <= 0 {
		t.Errorf("a token more: budget %d, %v", budget, err)
	}
	loop, profile := windowLoop(t, window, msgs)
	p := &windowProvider{window: window}
	if err := loop.compactHistory(context.Background(), "s1", p, profile, "", nil, "", CompactManual); err == nil {
		t.Error("/compact with room for no history compacted")
	}
	if asked, _ := p.counts(); asked != 0 {
		t.Errorf("%d requests sent with room for no history", asked)
	}
}
