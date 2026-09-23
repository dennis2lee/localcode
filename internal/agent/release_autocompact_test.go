package agent

import (
	"context"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/tools"
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
// nothing appended since means nothing is added to it. A build log full
// of separator lines, which a tokenizer packs many characters to a token
// and the estimate reads at four, is what the provider counted, not what
// a character estimate of it would say.
func TestAutoCompactionTrustsACountThatCoversEverything(t *testing.T) {
	log := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("read the build log")}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("================ step ok\n", 3500))}},
	}
	loop, profile := autoCompactLoop(t, 32768, log, 13000, 100)
	if est := estimateTokens(autoCompactSystem, log); est*100/32768 < 50 {
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

// The system prompt is in every request and in the measure, count or no
// count: a 10,000-token prompt on a 32,768 window is 30% of it before a
// word of the conversation.
func TestAutoCompactionCountsTheSystemPrompt(t *testing.T) {
	system := strings.Repeat("s", 40000)
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: 32768}

	t.Run("no count", func(t *testing.T) {
		loop := probeTestLoop(t, &probeCounter{found: false}, profile)
		loop.SetAutoCompactEnabled(true)
		loop.SetCompactPercent(50)
		msgs := []provider.Message{
			{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("read the build log")}},
			{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("y", 32000))}},
		}
		loop.setHistory("s1", msgs)
		if estimateTokens("", msgs)*100/32768 >= 50 {
			t.Fatalf("precondition: the conversation alone should be under the threshold")
		}
		p := &countingProvider{}
		loop.maybeAutoCompact(context.Background(), "s1", p, profile, system, nil)
		if p.asked() == 0 {
			t.Errorf("a request of %d tokens, system prompt included, on a 32,768 window was not compacted at 50%%", estimateTokens(system, msgs))
		}
	})

	t.Run("a count and a command's output after it", func(t *testing.T) {
		msgs := shortConversation()
		loop := probeTestLoop(t, &probeCounter{found: false}, profile)
		loop.SetAutoCompactEnabled(true)
		loop.SetCompactPercent(50)
		loop.setHistory("s1", msgs)
		measured := estimateTokens(system, msgs)
		setTestUsage(loop, "s1", sessionUsage{InputTokens: measured, OutputTokens: 100, MaxContext: 32768, Measured: measured})
		loop.appendShellTurn("s1", "!cat build.log", strings.Repeat("x", 32000))
		p := &countingProvider{}
		loop.maybeAutoCompact(context.Background(), "s1", p, profile, system, nil)
		if p.asked() == 0 {
			t.Errorf("a count of %d with 8,000 tokens of output after it was not compacted at 50%% of 32,768", measured)
		}
	})
}

// Images appended after the count are left out of the decision. What one
// costs depends on its pixels and on the model, and a compaction replaces
// every image with a note, so pricing them at the sizing's ceiling
// compacted away screenshots the person had just pasted.
func TestAutoCompactionLeavesOutImagesTheCountDidNotSee(t *testing.T) {
	const window = 32768
	shots := func(n int) []provider.Block {
		out := []provider.Block{provider.TextBlock("here is what the page looks like")}
		for i := 0; i < n; i++ {
			out = append(out, provider.Block{Type: provider.BlockImage, MediaType: "image/png", Data: []byte("png")})
		}
		return out
	}

	// A turn with ten screenshots was cancelled before the first token:
	// the message stays, no reply, and the count is from before it.
	// A conversation long enough to clear the guard against compacting
	// one no longer than what a compaction leaves, so the images are what
	// decides.
	worth := func() []provider.Message {
		return []provider.Message{
			{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("read the build log")}},
			{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("the step failed. ", 120))}},
		}
	}
	clears := func(t *testing.T, msgs []provider.Message) {
		t.Helper()
		if conversationTokens(msgs, countImages(msgs)) <= compactionKeeps(msgs, false)+shortestSummary {
			t.Fatalf("precondition: the conversation's text should clear the no-shrink guard")
		}
	}

	t.Run("a cancelled turn with screenshots", func(t *testing.T) {
		loop, profile := autoCompactLoop(t, window, worth(), 1000, 100)
		loop.appendHistory("s1", provider.Message{Role: provider.RoleUser, Content: shots(10)})
		clears(t, loop.history("s1"))
		loop.maybeAutoCompact(context.Background(), "s1", summaryOf("the user pasted screenshots"), profile, autoCompactSystem, nil)
		if n := countImages(loop.history("s1")); n != 10 {
			t.Errorf("the history the retry carries holds %d of the 10 screenshots pasted into the cancelled turn", n)
		}
	})

	// After /rewind the count is dropped, and a conversation of twelve
	// screenshots is measured by its text.
	t.Run("no count after a rewind", func(t *testing.T) {
		msgs := []provider.Message{
			{Role: provider.RoleUser, Content: shots(12)},
			{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("The sidebar overlaps the log pane. ", 60))}},
		}
		loop, profile := autoCompactLoop(t, window, msgs, 8000, 100)
		loop.setHistory("s1", msgs)
		clears(t, msgs)
		p := &countingProvider{}
		loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
		if p.asked() != 0 {
			t.Errorf("twelve screenshots with no count were priced at %d tokens and compacted", 12*imageTokenEstimate)
		}
	})

	// The images the count did cover are in the count, and an image
	// appended after it is still left out: the measurement records how
	// many there were, live and in the log.
	t.Run("images the count covered", func(t *testing.T) {
		msgs := []provider.Message{
			{Role: provider.RoleUser, Content: shots(4)},
			{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("seen")}},
		}
		loop, _ := autoCompactLoop(t, window, msgs, 6000, 100)
		setTestUsage(loop, "s1", sessionUsage{InputTokens: 6000, OutputTokens: 100, MaxContext: window,
			Measured: estimateTokens(autoCompactSystem, msgs), MeasuredImages: 4})
		loop.appendHistory("s1", provider.Message{Role: provider.RoleUser, Content: shots(1)})
		// The appended text is a few dozen tokens with its framing; the
		// appended image, priced, would be 1,600.
		got := loop.compactionMeasure("s1", autoCompactSystem, sendableHistory(loop.history("s1")))
		if got < 6100+5 || got > 6100+50 {
			t.Errorf("measure = %d, want the count (6,100) plus the appended text, the four covered images not charged again and the appended one left out", got)
		}
	})
}

// A count read back from a log written before the measurement was
// recorded cannot say what it covered. It decides alone, as it did before
// the measurement existed, rather than being overruled by an estimate of
// everything, which priced twelve screenshots at the ceiling and
// compacted a conversation the provider had counted at 22% on the first
// message after the upgrade.
func TestAutoCompactionTrustsACountFromAnOldLog(t *testing.T) {
	const window = 32768
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: window}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)
	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	imgs := make([]events.Image, 12)
	for i := range imgs {
		imgs[i] = events.Image{MediaType: "image/png", Data: []byte("png")}
	}
	loop.Store.Append("s1", events.TypeUserMessage, map[string]any{"text": "why does the build page look like this?", "images": imgs})
	loop.Store.Append("s1", events.TypeMessagePartEnd, map[string]any{"text": "The sidebar overlaps the log pane in every screenshot."})
	loop.Store.Append("s1", events.TypeUsage, map[string]any{
		"input_tokens": 7000, "output_tokens": 100, "max_context": window, "model": "DSA-Flash-CODE",
	})
	loop.RehydrateSession("s1")
	if u, ok := loop.getUsage("s1"); !ok || u.Measured != 0 || u.promptTokens() != 7000 {
		t.Fatalf("precondition: restored count %+v ok=%v", u, ok)
	}
	// Neither added to the conversation nor stripped of its output: the
	// count and the reply it counted, and nothing else.
	if got := loop.compactionMeasure("s1", autoCompactSystem, sendableHistory(loop.history("s1"))); got != 7100 {
		t.Errorf("an old log's count measures %d, want the count and its output, 7,100", got)
	}
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if p.asked() != 0 {
		t.Errorf("a conversation the provider counted at %d%% was compacted on the first turn after a restore", 7100*100/window)
	}
}

// A compaction does not summarize a summary again until what followed it
// is longer than it. A system prompt that is most of the window reads
// over the threshold every turn, and each turn would otherwise spend a
// summarization call replacing a summary and one exchange with a summary.
func TestAutoCompactionDoesNotSummarizeASummaryAgain(t *testing.T) {
	const window = 32768
	system := strings.Repeat("p", window*4*6/10)
	summary := provider.Message{Role: provider.RoleUser, Content: []provider.Block{{
		Type: provider.BlockText, Text: summaryHeader + strings.Repeat("s", 4000), Source: compactSummarySource,
	}}}
	afterOne := []provider.Message{
		summary,
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("and now?")}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("done")}},
	}
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: window}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)
	loop.setHistory("s1", afterOne)
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, system, nil)
	if p.asked() != 0 {
		t.Errorf("a summary and one exchange were compacted again under a system prompt of %d%% of the window", estimateTokens(system, nil)*100/window)
	}

	// The same text not marked as a summary is a conversation like any
	// other, and compacts once it is half the room the system prompt
	// leaves (see TestAutoCompactionUnderASystemPromptOverTheThreshold).
	plain := append([]provider.Message{{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(summary.Content[0].Text + strings.Repeat("t", 24000))}}}, afterOne[1:]...)
	loop.setHistory("s1", plain)
	fresh := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", fresh, profile, system, nil)
	if fresh.asked() == 0 {
		t.Errorf("a conversation that is not a summary was treated as one")
	}

	// And once more than the summary has followed it, there is something
	// to shrink again.
	loop.setHistory("s1", append(append([]provider.Message(nil), afterOne...),
		provider.Message{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(strings.Repeat("x", 28000))}}))
	busy := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", busy, profile, system, nil)
	if busy.asked() == 0 {
		t.Errorf("a summary followed by more than it, and more than half the room, was not compacted")
	}
}

// On a small window auto-compaction runs at the threshold, as it does on
// a large one. A fixed floor of the longest summary a compaction keeps
// held it off an 8,192-token window until the request was past 80%.
func TestAutoCompactionRunsOnASmallWindow(t *testing.T) {
	const window = 8192
	system := strings.Repeat("p", 6000)
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("read the build log")}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("y", 12000))}},
	}
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: window}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)
	loop.setHistory("s1", msgs)
	if pct := estimateTokens(system, msgs) * 100 / window; pct < 50 || pct > 60 {
		t.Fatalf("precondition: the request is %d%% of the window, want just over the threshold", pct)
	}
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, system, nil)
	if p.asked() == 0 {
		t.Errorf("a conversation just over the threshold on an 8,192-token window was not compacted")
	}
}

// A count restored from a v0.145.0 log carries the measurement without
// the number of images it covered. Read as zero, every image in the
// history read as appended, and the 1,600 tokens each the measurement had
// priced it at were taken off the text appended since: a command's
// 20,000-token output vanished from the decision.
func TestAutoCompactionReadsAV0145CountsImages(t *testing.T) {
	const window = 32768
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: window}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)
	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	imgs := make([]events.Image, 12)
	for i := range imgs {
		imgs[i] = events.Image{MediaType: "image/png", Data: []byte("png")}
	}
	loop.Store.Append("s1", events.TypeUserMessage, map[string]any{"text": "why does the build page look like this?", "images": imgs})
	loop.Store.Append("s1", events.TypeMessagePartEnd, map[string]any{"text": "The sidebar overlaps the log pane in every screenshot."})
	loop.RehydrateSession("s1")
	measured := estimateTokens(autoCompactSystem, sendableHistory(loop.history("s1")))
	loop.Store.Append("s1", events.TypeUsage, map[string]any{
		"input_tokens": 7000, "output_tokens": 100, "max_context": window, "model": "DSA-Flash-CODE", "measured": measured,
	})
	loop.RehydrateSession("s1")
	if u, _ := loop.getUsage("s1"); u.MeasuredImages != 12 {
		t.Fatalf("the restored count covers %d images, want the 12 in the history it was taken over", u.MeasuredImages)
	}
	loop.appendShellTurn("s1", "!cat build.log", strings.Repeat("x", 80000))
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if p.asked() == 0 {
		t.Errorf("a restored conversation carrying about %d tokens on a 32,768 window was not compacted at 50%%",
			loop.compactionMeasure("s1", autoCompactSystem, sendableHistory(loop.history("s1"))))
	}
}

// Nothing to compact, and the window is not asked for: resolving it can
// probe the server, and the first message of a new conversation would
// wait on that probe before it was even recorded.
func TestAutoCompactionDoesNotProbeForAnEmptyConversation(t *testing.T) {
	probe := &probeCounter{found: true, window: 8192}
	profile := config.Profile{Provider: "local", Model: "muse-glimmer-30b"}
	loop := probeTestLoop(t, probe, profile)
	loop.SetAutoCompactEnabled(true)
	loop.maybeAutoCompact(context.Background(), "s1", &countingProvider{}, profile, autoCompactSystem, nil)
	probe.mu.Lock()
	calls := probe.calls
	probe.mu.Unlock()
	if calls != 0 {
		t.Errorf("an empty conversation probed the server %d time(s) before the first message was recorded", calls)
	}
}

// The measurement's image count is written to the log and read back, so
// a restored session tells appended images from covered ones as a live
// one does.
func TestTheMeasuredImagesSurviveARestart(t *testing.T) {
	loop, sid := scriptedLoop(t, &scriptedProvider{turns: [][]provider.StreamEvent{cachedCall("seen")}}, tools.NewRegistry(nil))
	img := provider.Block{Type: provider.BlockImage, MediaType: "image/png", Data: []byte("png")}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "look", img, img); err != nil {
		t.Fatalf("turn: %v", err)
	}
	live, _ := loop.getUsage(sid)
	if live.MeasuredImages != 2 {
		t.Fatalf("the live measurement counts %d images, want 2", live.MeasuredImages)
	}
	evs, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	restored, _, _ := rehydrateUsage(evs)
	if restored.MeasuredImages != live.MeasuredImages || restored.Measured != live.Measured {
		t.Errorf("restored measurement %+v, live %+v", restored, live)
	}
}

// The images the count covered weigh in the guards what they weigh in
// the measure. Left out of the summary guard, a screenshot-driven
// exchange after a compaction, each turn a short question with a few
// screenshots the count had seen, kept the guard holding while the count
// climbed past the threshold toward overflow.
func TestAutoCompactionWeighsTheImagesTheCountSaw(t *testing.T) {
	const window = 32768
	summary := provider.Message{Role: provider.RoleUser, Content: []provider.Block{{
		Type: provider.BlockText, Text: summaryHeader + strings.Repeat("s", 4000), Source: compactSummarySource,
	}}}
	history := []provider.Message{summary}
	for i := 0; i < 4; i++ {
		turn := []provider.Block{provider.TextBlock("and this one?")}
		for j := 0; j < 3; j++ {
			turn = append(turn, provider.Block{Type: provider.BlockImage, MediaType: "image/png", Data: []byte("png")})
		}
		history = append(history,
			provider.Message{Role: provider.RoleUser, Content: turn},
			provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("the sidebar overlaps it")}})
	}
	loop, profile := autoCompactLoop(t, window, history, 19000, 100)
	setTestUsage(loop, "s1", sessionUsage{InputTokens: 19000, OutputTokens: 100, MaxContext: window,
		Measured: estimateTokens(autoCompactSystem, history), MeasuredImages: 12})
	if soonAfterASummary(history, 0) {
		t.Errorf("twelve screenshots the count covered after a summary weigh nothing in the summary guard")
	}
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if p.asked() == 0 {
		t.Errorf("a conversation the count put at %d%% after a summary was not compacted", 19100*100/window)
	}
}

// Reasoning kept in the live history is left out of every figure the
// decision reads, as a history rebuilt from the log has none. Counted
// live, it made a summary and one exchange read as something to shrink
// every turn, where the same conversation restored did not.
func TestAutoCompactionLeavesOutReasoning(t *testing.T) {
	summary := provider.Message{Role: provider.RoleUser, Content: []provider.Block{{
		Type: provider.BlockText, Text: summaryHeader + strings.Repeat("s", 4000), Source: compactSummarySource,
	}}}
	reply := func(reasoning bool) provider.Message {
		content := []provider.Block{provider.TextBlock("done")}
		if reasoning {
			content = append([]provider.Block{{Type: provider.BlockThinking, Text: strings.Repeat("r", 8000), Signature: "sig"}}, content...)
		}
		return provider.Message{Role: provider.RoleAssistant, Content: content}
	}
	live := []provider.Message{summary, {Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("and now?")}}, reply(true)}
	restored := []provider.Message{summary, live[1], reply(false)}
	if soonAfterASummary(live, 0) != soonAfterASummary(restored, 0) || !soonAfterASummary(live, 0) {
		t.Errorf("the summary guard reads a summary and one exchange as one live (%v) and restored (%v), want both", soonAfterASummary(live, 0), soonAfterASummary(restored, 0))
	}
	if a, b := conversationTokens(live, 0), conversationTokens(restored, 0); a != b {
		t.Errorf("the conversation weighs %d live and %d restored", a, b)
	}
	if a, b := measure(autoCompactSystem, live), measure(autoCompactSystem, restored); a != b {
		t.Errorf("the measurement recorded with a count is %+v live and %+v over the history a restart rebuilds", a, b)
	}
	if len(live[2].Content) != 2 {
		t.Fatalf("withoutReasoning changed the history it was given")
	}
}

// Where the system prompt alone reaches the threshold, no compaction can
// bring the measure under it. A tool-using session was compacted every
// turn there, the file it had just read discarded each time, while the
// conversation never passed 3% of the window. It compacts once the
// conversation is half the room the system prompt leaves.
func TestAutoCompactionUnderASystemPromptOverTheThreshold(t *testing.T) {
	const window = 32768
	system := strings.Repeat("p", window*4*6/10)
	room := window - estimateTokens(system, nil) - minOutputTokens
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: window}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)
	conversation := func(chars int) []provider.Message {
		return []provider.Message{
			{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("read internal/agent/compact.go")}},
			{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("c", chars))}},
		}
	}
	loop.setHistory("s1", conversation(3000))
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, system, nil)
	if p.asked() != 0 {
		t.Errorf("a %d-token conversation under a system prompt over the threshold was compacted; half the room is %d", 750, room/2)
	}
	loop.setHistory("s1", conversation((room/2+200)*4))
	busy := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", busy, profile, system, nil)
	if busy.asked() == 0 {
		t.Errorf("a conversation past half the room under a system prompt over the threshold was not compacted")
	}
}

// The threshold is where compaction starts, exactly: at it, and not a
// token under it.
func TestAutoCompactionStartsAtTheThreshold(t *testing.T) {
	const window = 32768
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("read the build log")}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(strings.Repeat("y", 60000))}},
	}
	for _, c := range []struct {
		counted int
		want    bool
	}{{window/2 - 100, true}, {window/2 - 101, false}} {
		loop, profile := autoCompactLoop(t, window, msgs, c.counted, 100)
		p := &countingProvider{}
		loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
		if got := p.asked() != 0; got != c.want {
			t.Errorf("a measure of %d on a %d window at 50%%: compacted = %v, want %v", c.counted+100, window, got, c.want)
		}
	}
}

// The cached prefix is in the measure on the path every live session
// takes, a count with its measurement, and a count that is all cache is
// a count.
func TestAutoCompactionCountsACachedPrefixItMeasured(t *testing.T) {
	const window = 32768
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("read the build log " + strings.Repeat("x", 8000))}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("which one?")}},
	}
	for _, c := range []struct {
		name          string
		input, cached int
		want          bool
	}{
		{"under the threshold, cache counted", 1000, 2000, false},
		{"over the threshold only with the cache", 1000, 20000, true},
		{"all of it cached", 0, 20000, true},
	} {
		loop, profile := autoCompactLoop(t, window, msgs, c.input, 100)
		setTestUsage(loop, "s1", sessionUsage{InputTokens: c.input, CachedInputTokens: c.cached, OutputTokens: 100, MaxContext: window,
			Measured: estimateTokens(autoCompactSystem, msgs)})
		p := &countingProvider{}
		loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
		if got := p.asked() != 0; got != c.want {
			t.Errorf("%s: compacted = %v, want %v", c.name, got, c.want)
		}
	}
}

// A conversation no longer than what a compaction puts in its place is
// left alone even where the system prompt is small: a count that tool
// definitions fill past the threshold, which no estimate here sees, over
// one short exchange. Nothing a compaction does can shrink it.
func TestAutoCompactionLeavesATinyConversationUnderLargeToolDefinitions(t *testing.T) {
	const window = 32768
	msgs := shortConversation()
	loop, profile := autoCompactLoop(t, window, msgs, 20000, 100)
	if estimateTokens(autoCompactSystem, nil)*100/window >= 50 {
		t.Fatalf("precondition: the system prompt should be under the threshold, so the room rule is not what decides")
	}
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, autoCompactSystem, nil)
	if p.asked() != 0 {
		t.Errorf("a %d-token exchange under a count of 20,100 was sent to be summarized", estimateTokens("", msgs))
	}
}

// Through the decision, not only the guard: after a compaction (no count
// yet) a follow-up shorter than the summary with a screenshot pasted into
// it still reads as soon after the summary, so the screenshot is not
// compacted away on an estimate of its size.
func TestAutoCompactionHoldsAfterASummaryForAPastedScreenshot(t *testing.T) {
	const window = 4096
	system := strings.Repeat("p", 3200)
	history := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{{
			Type: provider.BlockText, Text: summaryHeader + strings.Repeat("s", 4000), Source: compactSummarySource,
		}}},
		{Role: provider.RoleUser, Content: []provider.Block{
			provider.TextBlock(strings.Repeat("why does it look like this? ", 100)),
			{Type: provider.BlockImage, MediaType: "image/png", Data: []byte("png")},
		}},
	}
	profile := config.Profile{Provider: "local", Model: "DSA-Flash-CODE", ContextWindow: window}
	loop := probeTestLoop(t, &probeCounter{found: false}, profile)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(50)
	loop.setHistory("s1", history)
	if got := loop.compactionMeasure("s1", system, sendableHistory(history)); got*100/window < 50 {
		t.Fatalf("precondition: the text alone should reach the threshold (%d%%)", got*100/window)
	}
	p := &countingProvider{}
	loop.maybeAutoCompact(context.Background(), "s1", p, profile, system, nil)
	if p.asked() != 0 {
		t.Errorf("a follow-up shorter than its summary was compacted once a screenshot joined it")
	}
}

// The request sizing reads what was appended since the count on the
// ruler the count's measurement used, reasoning left out of both: with
// nothing appended, the estimate is the count and its reply, however much
// reasoning the live history keeps.
func TestTheSizingLeavesOutReasoningAsTheMeasurementDoes(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("plan the change")}},
		{Role: provider.RoleAssistant, Content: []provider.Block{
			{Type: provider.BlockThinking, Text: strings.Repeat("r", 40000), Signature: "sig"},
			provider.TextBlock("here is the plan"),
		}},
	}
	loop, _ := autoCompactLoop(t, 32768, msgs, 3000, 100)
	setTestUsage(loop, "s1", sessionUsage{InputTokens: 3000, OutputTokens: 100, MaxContext: 32768,
		Measured: measure(autoCompactSystem, msgs).tokens})
	if got := loop.inputEstimate("s1", autoCompactSystem, msgs); got != 3100 {
		t.Errorf("inputEstimate = %d with nothing appended since the count, want the count and its reply, 3,100", got)
	}
}

// The measure's own estimate of what was appended since the count leaves
// reasoning out, as the measurement it is compared with does: with
// nothing appended, the measure is the count and its reply, however much
// reasoning the live history keeps.
func TestTheCompactionMeasureLeavesOutReasoning(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("plan the change")}},
		{Role: provider.RoleAssistant, Content: []provider.Block{
			{Type: provider.BlockThinking, Text: strings.Repeat("r", 40000), Signature: "sig"},
			provider.TextBlock("here is the plan"),
		}},
	}
	loop, _ := autoCompactLoop(t, 32768, msgs, 3000, 100)
	setTestUsage(loop, "s1", sessionUsage{InputTokens: 3000, OutputTokens: 100, MaxContext: 32768,
		Measured: measure(autoCompactSystem, msgs).tokens})
	if got := loop.compactionMeasure("s1", autoCompactSystem, msgs); got != 3100 {
		t.Errorf("compactionMeasure = %d with nothing appended since the count, want the count and its reply, 3,100", got)
	}
}
