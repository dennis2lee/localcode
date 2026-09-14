package agent

import (
	"context"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/prompt"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// A compaction summary is text this build's own model wrote about earlier
// text, not external content a tool printed. It used to re-enter the
// conversation as a bare TextBlock with no Source, so historyEntries
// skipped it and the next turn's manifest described it only through the
// aggregate conversation entry, which is typed as external content from a
// tool result. Both construction sites, the live compaction and the
// restart replay, have to tag it, or the label survives one process
// lifetime.
func TestACompactionSummaryIsNamedAsGeneratedText(t *testing.T) {
	// The live path: compactHistory replaces the history with the summary.
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		{{Type: provider.EventTextDelta, TextDelta: "the compacted record"}},
	}}
	loop, sid := scriptedLoop(t, p, tools.NewRegistry(nil))
	loop.appendHistory(sid, provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Block{provider.TextBlock("hello")},
	})
	loop.appendHistory(sid, provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Block{provider.TextBlock("hi there")},
	})
	profile := config.Profile{Provider: "local", Model: "m"}
	if err := loop.compactHistory(context.Background(), sid, p, profile, "", nil, "", CompactManual); err != nil {
		t.Fatalf("compactHistory: %v", err)
	}
	live := loop.history(sid)
	if len(live) != 1 || len(live[0].Content) != 1 {
		t.Fatalf("live history = %+v, want the single summary message", live)
	}
	if live[0].Content[0].Source != compactSummarySource {
		t.Fatalf("live summary Source = %q, want %q", live[0].Content[0].Source, compactSummarySource)
	}
	if !strings.Contains(live[0].Content[0].Text, "the compacted record") {
		t.Errorf("live summary = %q, want it to carry the model's text", live[0].Content[0].Text)
	}

	// The restart path: the same tag has to come back out of the event log.
	rebuilt := rehydrateHistory([]events.Event{
		ev(events.TypeUserMessage, map[string]any{"text": "hello"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "hi there"}),
		ev(events.TypeCompacted, map[string]any{"summary": "the compacted record", "manual": true}),
	})
	if len(rebuilt) != 1 || len(rebuilt[0].Content) != 1 {
		t.Fatalf("rebuilt history = %+v, want the single summary message", rebuilt)
	}
	if rebuilt[0].Content[0].Source != compactSummarySource {
		t.Fatalf("rebuilt summary Source = %q, want %q", rebuilt[0].Content[0].Source, compactSummarySource)
	}

	// And the manifest has to name it as generated text, on both paths.
	for name, msgs := range map[string][]provider.Message{"live": live, "rebuilt": rebuilt} {
		var saw bool
		for _, e := range sourceEntries(historyEntries(msgs, false)) {
			if e.ID != compactSummarySource {
				continue
			}
			saw = true
			if e.Provenance != prompt.FromGeneratedSummary {
				t.Errorf("%s summary provenance = %q, want generated_summary", name, e.Provenance)
			}
			if e.Trust != prompt.TrustGenerated || e.Trust.Instruction() {
				t.Errorf("%s summary trust = %q, want generated and never instruction", name, e.Trust)
			}
		}
		if !saw {
			t.Errorf("the %s summary sends no %q entry: %v", name, compactSummarySource, entryIDs(historyEntries(msgs, false)))
		}
	}

	// The lookup behind it: entryForSource resolves the tag directly.
	e, ok := entryForSource(compactSummarySource, "the compacted record", false)
	if !ok {
		t.Fatalf("entryForSource(%q) not found", compactSummarySource)
	}
	if e.Provenance != prompt.FromGeneratedSummary || e.Trust != prompt.TrustGenerated {
		t.Errorf("entryForSource = provenance %q trust %q, want generated_summary and generated",
			e.Provenance, e.Trust)
	}
	if e.Trust.Instruction() {
		t.Error("a compaction summary is instruction-authoritative")
	}
}
