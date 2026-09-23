package agent

import (
	"context"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
)

// Auto-compaction decides at the top of a turn whether the conversation
// is full enough to summarize, and it decided from the provider's last
// count alone. That count describes the messages it was taken over.
// Anything appended after it was invisible: the output of a "!" command,
// a delegated turn's answer, the tool results of a turn whose follow-up
// request failed or was cancelled. Any of those can be a quarter of the
// window, and the next request carries all of them.

const autoCompactSystem = "you are a coding agent"

// autoCompactLoop is a loop with auto-compaction on at 50% on a window
// of the given size, and a session "s1" whose history is msgs, with a
// provider count covering exactly msgs: counted input, the reply's own
// output, and the measurement that says which messages the count saw.
func autoCompactLoop(t *testing.T, window int, msgs []provider.Message, counted, output int) (*Loop, config.Profile) {
	t.Helper()
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: window}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)
	loop.setHistory("s1", msgs)
	setTestUsage(loop, "s1", sessionUsage{
		InputTokens: counted, OutputTokens: output, MaxContext: window,
		Measured: estimateTokens(autoCompactSystem, msgs),
	})
	return loop, profile
}

func shortConversation() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("read the build log")}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("which one?")}},
	}
}

// The reproduction: a count of 1,100 tokens on a 32,768 window, then a
// command's output of about 20,000 tokens appended without a model call.
// The next request carries about 21,000 tokens, 65% of the window, and
// the 50% threshold did not fire because the count said 3%.
func TestAutoCompactionSeesWhatWasAppendedAfterTheCount(t *testing.T) {
	loop, profile := autoCompactLoop(t, 32768, shortConversation(), 1000, 100)
	loop.appendShellTurn("s1", "!cat build.log", strings.Repeat("x", 80000))

	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if p.asked() == 0 {
		t.Errorf("a conversation of about %d tokens on a %d window was not compacted at 50%%: the count said %d",
			loop.inputEstimate("s1", autoCompactSystem, loop.history("s1")), 32768, 1100)
	}
}

// Not eager: a count that covers the whole conversation decides, and
// nothing appended since means nothing is added to it. A Korean
// conversation the provider counted at 40% is 40%, not whatever a
// character estimate of it would say.
func TestAutoCompactionTrustsACountThatCoversEverything(t *testing.T) {
	korean := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(strings.Repeat("빌드 로그를 읽어 줘. ", 3000))}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("어느 로그요?")}},
	}
	loop, profile := autoCompactLoop(t, 32768, korean, 13000, 100)
	if est := estimateTokens(autoCompactSystem, korean); est*100/32768 < 50 {
		t.Fatalf("precondition: the character estimate (%d) should read over the threshold on its own", est)
	}
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if p.asked() != 0 {
		t.Errorf("a conversation the provider counted at %d%% was compacted at a 50%% threshold", 13100*100/32768)
	}
}

// The window is the one the next request goes to. The count records the
// window of the model that took it, and a /model switch to a smaller one
// left the threshold measured against the larger: 20,000 tokens read as
// 10% of 200,000 while the next request was going to a 32,768 window.
func TestAutoCompactionMeasuresAgainstTheWindowTheNextRequestGoesTo(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("read the build log")}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("y", 80000))}},
	}
	loop, profile := autoCompactLoop(t, 32768, msgs, 20000, 100)
	setTestUsage(loop, "s1", sessionUsage{InputTokens: 20000, OutputTokens: 100, MaxContext: 200000,
		Measured: estimateTokens(autoCompactSystem, msgs)})
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if p.asked() == 0 {
		t.Errorf("20,100 tokens bound for a 32,768 window were measured against the 200,000 the count was taken on")
	}
}

// With no count at all, after a rewind or a restart from a log that did
// not keep one, the conversation is what it is: a history of about
// 21,000 tokens is over half a 32,768 window whether or not a provider
// has said so yet. It used to wait for a count, which the first request
// had to survive to produce.
func TestAutoCompactionWithNoCountMeasuresTheConversation(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("read the build log")}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("y", 84000))}},
	}
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: 32768}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)
	loop.setHistory("s1", msgs)
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if p.asked() == 0 {
		t.Errorf("a %d-token conversation on a 32,768 window with no count was not compacted", estimateTokens(autoCompactSystem, msgs))
	}

	// And a short one with no count is left alone.
	loop.setHistory("s1", shortConversation())
	quiet := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", quiet, profile, autoCompactSystem, nil)
	if quiet.asked() != 0 {
		t.Errorf("a short conversation with no count was compacted")
	}
}
