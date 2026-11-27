package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
)

// emptySummaryProvider answers every request with usage and no text: a
// reasoning model that spent its cap thinking, on an adapter that keeps
// only the answer.
type emptySummaryProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *emptySummaryProvider) Chat(context.Context, provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	ch := make(chan provider.StreamEvent, 2)
	ch <- provider.StreamEvent{Type: provider.EventUsage, InputTokens: 9000, OutputTokens: 1024}
	ch <- provider.StreamEvent{Type: provider.EventMessageStop, StopReason: "max_tokens"}
	close(ch)
	return ch, nil
}

func (p *emptySummaryProvider) asked() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

const failWindow = 16384

// failLoop is a loop on a 16,384 window with auto-compaction at 50%, and
// session "s1" in the store holding a conversation over the threshold.
func failLoop(t *testing.T) (*Loop, config.Profile) {
	t.Helper()
	loop, profile := windowLoop(t, failWindow, exchange(10000*4))
	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	return loop, profile
}

func compactNotices(t *testing.T, loop *Loop) []events.Event {
	t.Helper()
	all, err := loop.Store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []events.Event
	for _, ev := range all {
		if ev.Type == events.TypeError && strings.Contains(dataString(ev.Data, "error"), "automatic compaction failed") {
			out = append(out, ev)
		}
	}
	return out
}

// The reproduction: the summary comes back empty every time, and every
// turn after the conversation crossed the threshold sent a summarization
// of the whole history before the person's message, billed, with nothing
// in the transcript. Now: one attempt, one notice saying why and when it
// is tried again, and no other until the conversation has grown by a
// tenth of the window.
func TestAFailedAutoCompactionIsNotSentEveryTurn(t *testing.T) {
	loop, profile := failLoop(t)
	p := &emptySummaryProvider{}
	for range 5 {
		if loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil) {
			t.Fatal("compacted on an empty summary")
		}
	}
	if n := p.asked(); n != 1 {
		t.Errorf("five turns sent %d summarizations; want 1", n)
	}
	notices := compactNotices(t, loop)
	if len(notices) != 1 {
		t.Fatalf("%d notices in the transcript, want 1", len(notices))
	}
	msg := dataString(notices[0].Data, "error")
	for _, want := range []string{"empty summary", "unchanged", "about 1638 tokens", "/compact"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the notice %q does not say %q", msg, want)
		}
	}
	if !isTrue(notices[0].Data["recovered"]) {
		t.Error("the notice ends the turn: it is not marked recovered")
	}

	// Grown by less than a tenth of the window: still not.
	loop.appendHistory("s1", provider.Message{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(strings.Repeat("u", 1000*4))}})
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if n := p.asked(); n != 1 {
		t.Errorf("a conversation grown by 1,000 tokens was summarized again (%d calls)", n)
	}
	// By a tenth: once more, and its own notice.
	loop.appendHistory("s1", provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("a", 1000*4))}})
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if n := p.asked(); n != 2 {
		t.Errorf("a conversation grown by 2,000 tokens: %d calls in all, want 2", n)
	}
	if n := len(compactNotices(t, loop)); n != 2 {
		t.Errorf("%d notices after the second failure, want 2", n)
	}
	// Another model is another attempt.
	other := profile
	other.Model = "DSA-Pro-CODE"
	loop.maybeAutoCompact(context.Background(), "s1", p, other, autoCompactSystem, nil)
	if n := p.asked(); n != 3 {
		t.Errorf("a switch to another model was not tried (%d calls)", n)
	}
}

// "/compact" is not held back by an automatic compaction that failed, and
// a history replaced since is not the one it failed on.
func TestAFailedAutoCompactionHoldsBackOnlyItself(t *testing.T) {
	loop, profile := failLoop(t)
	p := &emptySummaryProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if err := loop.compactHistory(context.Background(), "s1", p, profile, autoCompactSystem, nil, "", CompactManual); err == nil {
		t.Fatal("an empty summary compacted")
	}
	if n := p.asked(); n != 2 {
		t.Errorf("/compact after a failed automatic compaction: %d calls, want 2", n)
	}
	loop.setHistory("s1", exchange(10000*4))
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if n := p.asked(); n != 3 {
		t.Errorf("a replaced history was held back by the failure before it (%d calls)", n)
	}
}

// Esc during the summarization ends the turn; it is not a failure to
// report or to wait out.
func TestACancelledAutoCompactionIsNotAFailure(t *testing.T) {
	loop, profile := failLoop(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loop.maybeAutoCompact(ctx, "s1", &countingProvider{}, profile, autoCompactSystem, nil)
	if n := len(compactNotices(t, loop)); n != 0 {
		t.Errorf("%d notices for a cancelled compaction", n)
	}
	p := &emptySummaryProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if n := p.asked(); n != 1 {
		t.Errorf("the next turn was held back by a cancelled compaction (%d calls)", n)
	}
}

// A summary that came back empty was billed and counted nowhere: not in
// /usage, not after a restart, not in /usage all or the usage window. It
// is counted in all of them alike, from the notice that records it, for
// the automatic, the manual and the rescue path.
func TestAnEmptySummaryIsCountedWhereverUsageIsRead(t *testing.T) {
	loop, profile := failLoop(t)
	p := &emptySummaryProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	// "/compact" as the person types it, through the command.
	loop.Providers["local"] = p
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "/compact"); err != nil {
		t.Fatal(err)
	}
	if n := p.asked(); n != 2 {
		t.Fatalf("precondition: %d summarizations, want the automatic one and /compact's", n)
	}
	const calls, input, output = 2, 2 * 9000, 2 * 1024

	loop.mu.Lock()
	live := loop.cumulativeUsage["s1"][profile.Model]
	loop.mu.Unlock()
	if live.Calls != calls || live.InputTokens != input || live.OutputTokens != output {
		t.Errorf("/usage counts %+v, want %d calls, %d in, %d out", live, calls, input, output)
	}
	all, err := loop.Store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, cum := rehydrateUsage(all)
	if got := cum[profile.Model]; got != live {
		t.Errorf("after a restart /usage counts %+v, live %+v", got, live)
	}
	var each UsageFigures
	for _, ev := range all {
		each.Add(UsageOfEvent(ev))
	}
	if each.Calls != calls || each.InputTokens != input || each.OutputTokens != output {
		t.Errorf("the events read one by one (localcode run) count %+v", each)
	}
	summary, ok := loop.UsageAcross("all", time.Now())
	if !ok {
		t.Fatal("/usage all is not known")
	}
	if got := summary.Models[profile.Model]; got.Calls != calls || got.InputTokens != input {
		t.Errorf("/usage all and the usage window count %+v", got)
	}
}
