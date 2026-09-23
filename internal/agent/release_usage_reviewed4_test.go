package agent

// Found in review round four: a mid-turn typed instruction lost its
// injected.user tag when the history was rebuilt after a restart.

import (
	"testing"

	"localcode/internal/events"
	"localcode/internal/prompt"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// review4HistEqual says two histories hold the same conversation: same
// roles, same text, same image count per message.
func review4HistEqual(t *testing.T, what string, a, b []provider.Message) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("%s length live %d, restored %d", what, len(a), len(b))
	}
	for i := range a {
		if a[i].Role != b[i].Role {
			t.Fatalf("%s message %d role live %q, restored %q", what, i, a[i].Role, b[i].Role)
		}
		if messageText(a[i]) != messageText(b[i]) {
			t.Fatalf("%s message %d text live %q, restored %q", what, i, messageText(a[i]), messageText(b[i]))
		}
		if countImages([]provider.Message{a[i]}) != countImages([]provider.Message{b[i]}) {
			t.Fatalf("%s message %d images live %d, restored %d", what, i,
				countImages([]provider.Message{a[i]}), countImages([]provider.Message{b[i]}))
		}
	}
}

// A sentence typed mid-turn travels inside the tool_result message, tagged
// as the person's own instruction (Source and Sources "injected.user"),
// and the event log records it the same way takeInjected does. After a
// restart the rebuilt block must carry the same tag: hasInjectedUser (and
// through it droppedCarriedAssets, carriedAssetNote and compactionKeeps)
// and the prompt manifest's injectedEntry all key off it.
func TestReviewedInjectedTagSurvivesRestart(t *testing.T) {
	p := &scriptedProvider{turns: [][]provider.StreamEvent{}}
	loop, sid := scriptedLoop(t, p, tools.NewRegistry(nil))

	appendEv := func(typ events.Type, data map[string]any) {
		t.Helper()
		if _, err := loop.Store.Append(sid, typ, data); err != nil {
			t.Fatalf("append %s: %v", typ, err)
		}
	}
	appendEv(events.TypeUserMessage, map[string]any{"text": "do the thing"})
	appendEv(events.TypeToolStart, map[string]any{"tool_use_id": "call_1", "name": "echo"})
	appendEv(events.TypeToolEnd, map[string]any{"tool_use_id": "call_1", "input": "{}", "content": "ok"})
	// Exactly what takeInjected appends when the text reaches the model.
	appendEv(events.TypeUserMessage, map[string]any{
		"text": "also do not break x", "injected": true, "source": "injected.user",
	})
	appendEv(events.TypeMessagePartEnd, map[string]any{"text": "done"})

	body := injectedPreface + "also do not break x"
	live := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock("do the thing")}},
		{Role: provider.RoleAssistant, Content: []provider.Block{{
			Type: provider.BlockToolUse, ToolUseID: "call_1", ToolName: "echo",
		}}},
		{Role: provider.RoleUser, Content: []provider.Block{
			provider.ToolResultBlock("call_1", "ok", false),
			// Exactly what takeInjected hands the model.
			{Type: provider.BlockText, Text: body, Source: "injected.user", Sources: []provider.BlockSource{{
				ID: "injected.user", From: len(injectedPreface), To: len(body),
			}}},
		}},
		{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("done")}},
	}
	loop.setHistory(sid, live)
	if !hasInjectedUser(loop.history(sid)) {
		t.Fatal("precondition: live history does not read as injected")
	}

	loop.ReleaseSessionMemory(sid)
	loop.RehydrateSession(sid)
	restored := loop.history(sid)
	review4HistEqual(t, "injected history", live, restored)
	if !hasInjectedUser(restored) {
		t.Errorf("restored history lost the injected.user tag: hasInjectedUser live %v, restored %v",
			hasInjectedUser(live), hasInjectedUser(restored))
	}
	liveAssets, restAssets := droppedCarriedAssets(live), droppedCarriedAssets(restored)
	if len(liveAssets) != len(restAssets) {
		t.Errorf("droppedCarriedAssets live %q, restored %q", liveAssets, restAssets)
	}
	if liveKeeps, restKeeps := compactionKeeps(live, true), compactionKeeps(restored, true); liveKeeps != restKeeps {
		t.Errorf("compactionKeeps live %d, restored %d", liveKeeps, restKeeps)
	}
	// The manifest names the mid-turn sentence as the person's own
	// instruction live; restored, the bare block earns no entry at all,
	// and the sentence reads as unattributed conversation text.
	hasEntry := func(es []prompt.Entry, id string) bool {
		for _, e := range es {
			if e.ID == id {
				return true
			}
		}
		return false
	}
	if liveHas, restHas := hasEntry(historyEntries(live, false), "injected.user"), hasEntry(historyEntries(restored, false), "injected.user"); liveHas != restHas {
		t.Errorf("injected.user manifest entry live %v, restored %v", liveHas, restHas)
	}
}
