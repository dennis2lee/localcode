package agent

// Found in review round three.

import (
	"context"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
)

// maybeAutoCompact handed soonAfterASummary the sendable history, in which
// two user messages in a row are merged into one. The compaction summary
// re-enters as a user message, so the first user message after a
// compaction merged into it, and the guard compared the rest of the
// follow-up against the summary plus that message, instead of the
// follow-up against the summary.
//
// A summary of about 1,000 tokens, then a pasted document of about 800
// and a reply of about 300: what followed the summary (about 1,100) is
// longer than the summary, so by the guard's own rule there is something
// to shrink again. Merged, the summary side read about 1,850 against
// about 300, and the guard kept skipping while the threshold fired.
//
// Auto-compact on at 50% on a 4,096 window, no count (a compaction's
// setHistory drops it), a post-compaction history whose follow-up outgrew
// its summary: the summarization call is tried.
func TestReviewedSummaryGuardCountsTheFirstFollowUpAsFollowUp(t *testing.T) {
	const window = 4096
	const system = autoCompactSystem
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: window}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)

	summary := provider.Message{Role: provider.RoleUser, Content: []provider.Block{{
		Type: provider.BlockText, Text: summaryHeader + strings.Repeat("s", 4000),
		Source: compactSummarySource,
	}}}
	firstFollowUp := provider.Message{Role: provider.RoleUser,
		Content: []provider.Block{provider.TextBlock(strings.Repeat("u", 3200))}}
	reply := provider.Message{Role: provider.RoleAssistant,
		Content: []provider.Block{provider.TextBlock(strings.Repeat("a", 1200))}}
	loop.setHistory("s1", []provider.Message{summary, firstFollowUp, reply})

	raw := loop.history("s1")
	if got := conversationTokens(raw[:1], 0); got >= conversationTokens(raw[1:], 0) {
		t.Fatalf("precondition: the follow-up (%d) should read longer than the summary (%d)",
			conversationTokens(raw[1:], 0), got)
	}
	sent := sendableHistory(raw)
	if len(sent) != 2 {
		t.Fatalf("precondition: the summary and the first follow-up merge into one message, got %d", len(sent))
	}
	used := loop.compactionMeasure("s1", system, sent)
	if used*100/window < 50 {
		t.Fatalf("precondition: the request is %d%% of the window, want over the 50%% threshold", used*100/window)
	}
	if conversationTokens(sent, 0) <= compactionKeeps(sent, false)+shortestSummary {
		t.Fatalf("precondition: the no-shrink guard passes, so only the summary guard can skip")
	}
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, system, nil)
	if p.asked() != 1 {
		t.Errorf("a follow-up longer than its summary was not sent to be summarized: " +
			"the summary guard measured the summary merged with the first follow-up")
	}
}
