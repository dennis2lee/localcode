package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// reviewedFork copies srcID's log into a new top-level session forkID the
// way handleForkSession does: a session.forked event saying how many
// events follow as the copy, then every event but session.renamed.
func reviewedFork(t *testing.T, loop *Loop, srcID, forkID string) {
	t.Helper()
	evs, err := loop.Store.Events(srcID, 0)
	if err != nil {
		t.Fatalf("source events: %v", err)
	}
	copied := 0
	for _, ev := range evs {
		if ev.Type != events.TypeSessionRenamed {
			copied++
		}
	}
	if _, err := loop.Store.CreateSession(forkID, "", "general-purpose", true); err != nil {
		t.Fatalf("create fork: %v", err)
	}
	if _, err := loop.Store.Append(forkID, events.TypeSessionForked, map[string]any{
		"from": srcID, "copied": copied,
	}); err != nil {
		t.Fatalf("forked marker: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == events.TypeSessionRenamed {
			continue
		}
		if _, err := loop.Store.Append(forkID, ev.Type, ev.Data); err != nil {
			t.Fatalf("copy event: %v", err)
		}
	}
}

func reviewUsage(t *testing.T, loop *Loop, sid, model string, in, out int) {
	t.Helper()
	if _, err := loop.Store.Append(sid, events.TypeUsage, map[string]any{
		"model": model, "input_tokens": in, "output_tokens": out,
	}); err != nil {
		t.Fatalf("usage event: %v", err)
	}
}

// A fork's copy of another log is counted once, across conversations:
// a fork of a log that holds session.renamed events, and a fork of that
// fork, add only the calls made after each fork.
func TestReviewedForkCopiesCountOnce(t *testing.T) {
	p := &scriptedProvider{turns: [][]provider.StreamEvent{}}
	loop, sid := scriptedLoop(t, p, tools.NewRegistry(nil))
	reviewUsage(t, loop, sid, "m", 100, 10)
	if _, err := loop.Store.Append(sid, events.TypeSessionRenamed, map[string]any{"title": "orig"}); err != nil {
		t.Fatalf("renamed: %v", err)
	}
	reviewUsage(t, loop, sid, "m", 50, 5)

	reviewedFork(t, loop, sid, "forkB")
	reviewUsage(t, loop, "forkB", "m", 30, 3)
	reviewedFork(t, loop, "forkB", "forkC")
	reviewUsage(t, loop, "forkC", "m", 7, 1)

	totals, sessions, unread := loop.usageOfSessions([]string{sid, "forkB", "forkC"}, time.Time{})
	if unread != 0 {
		t.Fatalf("unread = %d, want 0", unread)
	}
	got := totals["m"]
	want := modelTotals{InputTokens: 100 + 50 + 30 + 7, OutputTokens: 10 + 5 + 3 + 1, Calls: 4}
	if got != want {
		t.Errorf("across source, fork and fork-of-fork = %+v, want %+v", got, want)
	}
	if sessions != 3 {
		t.Errorf("sessions = %d, want 3", sessions)
	}
}

// The same, through the window /usage today uses: the copy is stamped
// with the moment of the fork, so without the skip an old conversation's
// spend would count again on the day it was forked.
func TestReviewedForkCopiesDoNotCountAgainToday(t *testing.T) {
	p := &scriptedProvider{turns: [][]provider.StreamEvent{}}
	loop, sid := scriptedLoop(t, p, tools.NewRegistry(nil))
	reviewUsage(t, loop, sid, "m", 100, 10)
	reviewedFork(t, loop, sid, "forkB")
	reviewUsage(t, loop, "forkB", "m", 30, 3)

	y, m, d := time.Now().Date()
	since := time.Date(y, m, d, 0, 0, 0, 0, time.Now().Location())
	totals, _, _ := loop.usageOfSessions([]string{sid, "forkB"}, since)
	got := totals["m"]
	want := modelTotals{InputTokens: 130, OutputTokens: 13, Calls: 2}
	if got != want {
		t.Errorf("today across a fork = %+v, want %+v", got, want)
	}
}

// What the compaction decision measures is the same live and after a
// restart: the provider's count for what it covered, the text appended
// since estimated, and the images told apart by how many the count saw.
func TestReviewedMeasureAgreesLiveAndAfterRestart(t *testing.T) {
	p := &scriptedProvider{turns: [][]provider.StreamEvent{}}
	loop, sid := scriptedLoop(t, p, tools.NewRegistry(nil))
	const system = "sys"
	img1 := provider.ImageBlock("image/png", []byte("first png"))
	img2 := provider.ImageBlock("image/png", []byte("second png"))

	covered := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("look at this"), img1}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("seen")}},
	}
	if _, err := loop.Store.Append(sid, events.TypeUserMessage, map[string]any{
		"text": "look at this", "images": []provider.Block{img1},
	}); err != nil {
		t.Fatalf("user message: %v", err)
	}
	if _, err := loop.Store.Append(sid, events.TypeMessagePartEnd, map[string]any{"text": "seen"}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	loop.setHistory(sid, covered)
	loop.recordUsage(sid, "m", 200000, measure(system, covered),
		streamUsage{hasUsage: true, inputTokens: 5000, outputTokens: 60})

	// One more screenshot after the count, on both the transcript and
	// the live history, as a turn would leave them.
	if _, err := loop.Store.Append(sid, events.TypeUserMessage, map[string]any{
		"text": "and this", "images": []provider.Block{img2},
	}); err != nil {
		t.Fatalf("second user message: %v", err)
	}
	loop.appendHistory(sid, provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Block{provider.TextBlock("and this"), img2},
	})

	live := loop.compactionMeasure(sid, system, loop.history(sid))
	liveUse, ok := loop.getUsage(sid)
	if !ok {
		t.Fatal("no live usage")
	}
	loop.ReleaseSessionMemory(sid)
	loop.RehydrateSession(sid)
	rest := loop.compactionMeasure(sid, system, loop.history(sid))
	restUse, ok := loop.getUsage(sid)
	if !ok {
		t.Fatal("no restored usage")
	}
	if liveUse != restUse {
		t.Errorf("usage live %+v, restored %+v", liveUse, restUse)
	}
	if live != rest {
		t.Errorf("compactionMeasure live %d, restored %d", live, rest)
	}
	// The appended screenshot is left out of the decision both times:
	// the count saw one image, the history holds two.
	if liveUse.MeasuredImages != 1 {
		t.Errorf("MeasuredImages = %d, want 1", liveUse.MeasuredImages)
	}
	restHist := loop.history(sid)
	appended := estimateTokens(system, restHist) - liveUse.Measured - max(0, countImages(restHist)-liveUse.MeasuredImages)*imageTokenEstimate
	if want := 5000 + 60 + max(0, appended); live != want {
		t.Errorf("compactionMeasure = %d, want count plus estimated text since %d", live, want)
	}
}

// A log from before measured_images existed still says how many images
// the count covered: the history as it stood at that count.
func TestReviewedOldLogRecoversCoveredImages(t *testing.T) {
	p := &scriptedProvider{turns: [][]provider.StreamEvent{}}
	loop, sid := scriptedLoop(t, p, tools.NewRegistry(nil))
	img := provider.ImageBlock("image/png", []byte("a png"))
	if _, err := loop.Store.Append(sid, events.TypeUserMessage, map[string]any{
		"text": "look", "images": []provider.Block{img, img},
	}); err != nil {
		t.Fatalf("user message: %v", err)
	}
	if _, err := loop.Store.Append(sid, events.TypeMessagePartEnd, map[string]any{"text": "seen"}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	history := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("look"), img, img}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("seen")}},
	}
	if _, err := loop.Store.Append(sid, events.TypeUsage, map[string]any{
		"model": "m", "input_tokens": 4000, "output_tokens": 40,
		"max_context": 200000, "measured": estimateTokens("sys", history),
	}); err != nil {
		t.Fatalf("usage event: %v", err)
	}
	loop.ReleaseSessionMemory(sid)
	loop.RehydrateSession(sid)
	u, ok := loop.getUsage(sid)
	if !ok {
		t.Fatal("no restored usage")
	}
	if u.MeasuredImages != 2 {
		t.Errorf("restored MeasuredImages = %d, want 2", u.MeasuredImages)
	}
}

// A compaction's summary still reads as one after a restart, so the
// guard against summarizing a summary holds on both sides of it.
func TestReviewedSummaryGuardSurvivesRestart(t *testing.T) {
	p := replayProvider{events: cachedCall("a summary")}
	loop, store, profile := replayLoop(t, p, 200000)
	const sid = "s1"
	loop.setHistory(sid, shortConversation())
	if err := loop.compactHistory(context.Background(), sid, p, profile, "", nil, "", CompactManual); err != nil {
		t.Fatalf("compaction: %v", err)
	}
	_ = store
	liveHist := loop.history(sid)
	if !soonAfterASummary(liveHist, 0) {
		t.Fatal("a fresh summary does not read as one live")
	}
	loop.ReleaseSessionMemory(sid)
	loop.RehydrateSession(sid)
	restHist := loop.history(sid)
	if !soonAfterASummary(restHist, 0) {
		t.Errorf("a fresh summary does not read as one after a restart: %+v", restHist)
	}
	if len(restHist) == 0 || len(restHist[0].Content) == 0 || restHist[0].Content[0].Source != liveHist[0].Content[0].Source {
		t.Errorf("summary Source live %q, restored %+v", liveHist[0].Content[0].Source, restHist)
	}
}

// soonAfterASummary priced follow-up images at the sizing ceiling, 1,600
// tokens each, while the threshold it guards leaves them out. A
// screenshot pasted right after a compaction flipped the guard from
// "still a summary" to "something to shrink", and the compaction then
// replaced that screenshot with a note. Found in review round one.
func TestReviewedSummaryGuardLeavesOutNewImages(t *testing.T) {
	summaryText := summaryHeader + strings.Repeat("the summary says the build was green. ", 1000)
	summary := []provider.Message{{
		Role: provider.RoleUser,
		Content: []provider.Block{{
			Type: provider.BlockText, Text: summaryText,
			Source: compactSummarySource,
		}},
	}}
	followText := strings.Repeat("w", (estimateTokens("", summary)-200)*4)
	if estimateTokens("", summary) <= estimateTokens("", []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(followText)}},
	}) {
		t.Fatalf("precondition: the follow-up text should read shorter than the summary")
	}
	withImage := []provider.Message{summary[0], {
		Role: provider.RoleUser,
		Content: []provider.Block{
			provider.TextBlock(followText),
			provider.ImageBlock("image/png", []byte("a fresh screenshot")),
		},
	}}
	if !soonAfterASummary(withImage, 1) {
		t.Errorf("a follow-up shorter than the summary stops reading as one once a screenshot is pasted after it")
	}
}

// An archived descendant's calls are still its conversation's spend;
// a deleted one's are gone with it.
func TestReviewedTreeCountsArchivedNotDeleted(t *testing.T) {
	p := &scriptedProvider{turns: [][]provider.StreamEvent{}}
	loop, sid := scriptedLoop(t, p, tools.NewRegistry(nil))
	reviewUsage(t, loop, sid, "m", 100, 10)
	if _, err := loop.Store.CreateSession("arch", sid, "general-purpose", false); err != nil {
		t.Fatal(err)
	}
	reviewUsage(t, loop, "arch", "m", 40, 4)
	if _, err := loop.Store.CreateSession("gone", sid, "general-purpose", false); err != nil {
		t.Fatal(err)
	}
	reviewUsage(t, loop, "gone", "m", 9, 1)

	if err := loop.Store.Delete("gone"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Only a top-level conversation archives; its tasks are shelved with
	// it. Archiving the root must not drop the tree's figures.
	if _, err := loop.Store.Archive(sid); err != nil {
		t.Fatalf("archive: %v", err)
	}

	total := loop.UsageOfTree(sid).Total()
	want := UsageFigures{InputTokens: 140, OutputTokens: 14, Calls: 2}
	if total != want {
		t.Errorf("tree usage = %+v, want %+v (the archived conversation and its child, not the deleted one)", total, want)
	}
	across, _, _ := loop.usageAcross(usageWindow{name: "every conversation"})
	if got := across["m"]; got.InputTokens != 140 || got.OutputTokens != 14 || got.Calls != 2 {
		t.Errorf("/usage all after archive+delete = %+v, want %+v", got, want)
	}
}
