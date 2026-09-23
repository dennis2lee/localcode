package agent

// Found in review round two.

import (
	"context"
	"strings"
	"testing"

	"localcode/internal/config"
)

// A compaction that could not leave the conversation smaller does not run,
// on a fresh conversation as on one that was compacted before.
//
// d9a8e9d added a no-shrink guard: with the system prompt alone most of
// the window, every turn measures over the threshold, and each turn would
// spend a billed summarization call replacing a summary and one exchange
// with a bigger summary. a2a09df replaced that guard with
// soonAfterASummary, which only fires when the history opens with a
// compaction summary. A conversation that was never compacted has no such
// mark, so the first turn over the threshold compacts a two-message
// exchange under a 20,000-token system prompt: the summary and its notes
// are longer than the exchange they replace, and the call is billed.
//
// Auto-compact on at 50% on a 32,768 window, a system prompt of about
// 20,000 tokens, one plain exchange of about 15: no summarization call,
// since the header and notes alone are longer than the exchange.
func TestReviewedTinyConversationUnderAHugeSystemPromptDoesNotCompact(t *testing.T) {
	const window = 32768
	system := strings.Repeat("p", 80000)
	msgs := shortConversation()
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: window}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)
	loop.setHistory("s1", msgs)
	measured := estimateTokens(system, msgs)
	setTestUsage(loop, "s1", sessionUsage{InputTokens: measured, OutputTokens: 10, MaxContext: window, Measured: measured})
	if got := loop.compactionMeasure("s1", system, sendableHistory(loop.history("s1"))); got*100/window < 50 {
		t.Fatalf("precondition: the request is %d%% of the window, want over the 50%% threshold", got*100/window)
	}
	if soonAfterASummary(sendableHistory(loop.history("s1"))) {
		t.Fatalf("precondition: a plain exchange is not a summary")
	}
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, system, nil)
	if p.asked() != 0 {
		t.Errorf("a %d-token exchange under a %d-token system prompt was sent to be summarized: the summary and its notes outlive the exchange",
			estimateTokens("", msgs), estimateTokens(system, nil))
	}
}
