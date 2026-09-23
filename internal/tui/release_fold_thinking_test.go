package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/events"
)

func thinkingDelta(text string, fold bool) events.Event {
	return events.Event{Type: events.TypeThinkingDelta, Data: map[string]any{
		"text": text, "fold": fold, "show_thinking": true,
	}}
}

// thinkingEntries returns the reasoning blocks in the transcript.
func (m Model) thinkingEntries() []transcriptEntry {
	var out []transcriptEntry
	for _, e := range m.transcript {
		if e.kind == entryThinking {
			out = append(out, e)
		}
	}
	return out
}

// A muse model's reasoning gets a block of its own while it streams,
// headed with its running time, and folds to one line saying how long it
// took when its end arrives. The answer after it is a separate entry.
func TestAMuseReasoningBlockStreamsThenFolds(t *testing.T) {
	m := newTestModel()
	m.applyEvent(thinkingDelta("The user asks 17 times 23. ", true))
	m.applyEvent(thinkingDelta("That is 391.", true))

	blocks := m.thinkingEntries()
	if len(blocks) != 1 {
		t.Fatalf("got %d reasoning blocks, want one: %#v", len(blocks), m.transcript)
	}
	if !blocks[0].live || !strings.HasPrefix(blocks[0].note, "Thinking · ") {
		t.Errorf("a streaming block is %#v, want live and headed Thinking", blocks[0])
	}
	if blocks[0].text != "The user asks 17 times 23. That is 391." {
		t.Errorf("block text = %q", blocks[0].text)
	}
	if !m.thinking {
		t.Error("the busy line no longer says thinking")
	}

	m.applyEvent(events.Event{Type: events.TypeThinkingEnd, Data: map[string]any{"fold": true, "elapsed_ms": float64(12400)}})
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "17 times 23 is 391."}})

	blocks = m.thinkingEntries()
	if blocks[0].live || blocks[0].open || blocks[0].note != "Thought for 12s" {
		t.Errorf("a finished block is %#v, want folded and saying Thought for 12s", blocks[0])
	}
	last := m.transcript[len(m.transcript)-1]
	if last.kind != entryModel || last.text != "17 times 23 is 391." {
		t.Errorf("the answer did not start an entry of its own: %#v", m.transcript)
	}

	out := stripTestANSI(renderTranscript(m.transcript, 80))
	if !strings.Contains(out, "▸ Thought for 12s") {
		t.Errorf("the folded block does not draw its line:\n%s", out)
	}
	if strings.Contains(out, "The user asks") {
		t.Errorf("a folded block still draws its text:\n%s", out)
	}
}

// Other models, and show_thinking off, keep what the TUI always did:
// the busy line's word and nothing in the transcript.
func TestReasoningNotMarkedForFoldingStaysOffTheTranscript(t *testing.T) {
	m := newTestModel()
	m.applyEvent(thinkingDelta("weighing it up", false))
	if len(m.thinkingEntries()) != 0 {
		t.Errorf("an unmarked stream drew a block: %#v", m.transcript)
	}
	if !m.thinking {
		t.Error("the busy line does not say thinking")
	}

	m = newTestModel()
	m.applyEvent(events.Event{Type: events.TypeThinkingDelta, Data: map[string]any{
		"text": "hidden", "fold": true, "show_thinking": false,
	}})
	if len(m.thinkingEntries()) != 0 {
		t.Errorf("show_thinking off still drew a block: %#v", m.transcript)
	}
}

// A block whose end never comes folds when the answer starts, when a tool
// starts, and when the turn ends, on this client's own clock.
func TestAReasoningBlockFoldsWithoutItsEnd(t *testing.T) {
	for name, ev := range map[string]events.Event{
		"answer":    {Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "ok"}},
		"tool":      {Type: events.TypeToolStart, Data: map[string]any{"name": "glob", "input": `{"pattern":"*.go"}`, "tool_use_id": "t1"}},
		"turn done": {Type: events.TypeTurnDone, Data: map[string]any{}},
		"cancelled": {Type: events.TypeTurnCancelled, Data: map[string]any{}},
		"error":     {Type: events.TypeError, Data: map[string]any{"error": "boom"}},
	} {
		t.Run(name, func(t *testing.T) {
			m := newTestModel()
			m.applyEvent(thinkingDelta("thinking about it", true))
			m.thinkingSince = time.Now().Add(-3 * time.Second)
			m.applyEvent(ev)
			b := m.thinkingEntries()
			if len(b) != 1 || b[0].live {
				t.Fatalf("the block did not fold: %#v", b)
			}
			if b[0].note != "Thought for 3s" {
				t.Errorf("note = %q, want the client's own 3s", b[0].note)
			}
		})
	}
}

// Two reasoning blocks in one turn, one per request, stay two blocks.
func TestEachReasoningBlockIsItsOwn(t *testing.T) {
	m := newTestModel()
	m.applyEvent(thinkingDelta("first", true))
	m.applyEvent(events.Event{Type: events.TypeThinkingEnd, Data: map[string]any{"fold": true, "elapsed_ms": float64(1000)}})
	m.applyEvent(events.Event{Type: events.TypeToolStart, Data: map[string]any{"name": "glob", "input": "{}", "tool_use_id": "t1"}})
	m.applyEvent(thinkingDelta("second", true))
	b := m.thinkingEntries()
	if len(b) != 2 || b[0].text != "first" || b[1].text != "second" || b[0].live || !b[1].live {
		t.Errorf("blocks = %#v", b)
	}
}

// Ctrl+O opens every folded block and closes them again; a block folded
// after the key follows the same choice. With nothing to open it says
// so and changes nothing.
func TestCtrlOOpensAndFoldsTheReasoning(t *testing.T) {
	m := newTestModel()
	ctrlO := tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}

	next, _, handled := m.handleKey(ctrlO)
	m = next.(Model)
	if !handled || m.thinkingExpanded {
		t.Fatalf("with no blocks, Ctrl+O handled=%v and left expanded=%v", handled, m.thinkingExpanded)
	}
	if !strings.Contains(m.transcriptText(), "No folded reasoning") {
		t.Errorf("Ctrl+O with nothing to open said nothing:\n%s", m.transcriptText())
	}

	m.applyEvent(thinkingDelta("line one\nline two", true))
	m.applyEvent(events.Event{Type: events.TypeThinkingEnd, Data: map[string]any{"fold": true, "elapsed_ms": float64(2000)}})

	next, _, _ = m.handleKey(ctrlO)
	m = next.(Model)
	if b := m.thinkingEntries(); !b[0].open {
		t.Fatalf("Ctrl+O did not open the block: %#v", b)
	}
	out := stripTestANSI(renderTranscript(m.transcript, 60))
	if !strings.Contains(out, "▾ Thought for 2s") || !strings.Contains(out, "│ line one") || !strings.Contains(out, "│ line two") {
		t.Errorf("an open block does not draw its text:\n%s", out)
	}

	m.applyEvent(thinkingDelta("later", true))
	m.applyEvent(events.Event{Type: events.TypeThinkingEnd, Data: map[string]any{"fold": true}})
	if b := m.thinkingEntries(); !b[1].open {
		t.Errorf("a block folded after Ctrl+O did not follow it: %#v", b[1])
	}

	next, _, _ = m.handleKey(ctrlO)
	m = next.(Model)
	for _, b := range m.thinkingEntries() {
		if b.open {
			t.Errorf("the second Ctrl+O left a block open: %#v", b)
		}
	}
}

// A live block shows only the end of a long reasoning, so the answer is
// not pushed off the screen while it is still being thought, and so a
// long reasoning is not rewrapped whole on every delta.
func TestALiveBlockShowsOnlyItsLastLines(t *testing.T) {
	m := newTestModel()
	var long strings.Builder
	for i := 0; i < 400; i++ {
		long.WriteString("step ")
		long.WriteString(strings.Repeat("x", i%7))
		long.WriteString(" of the reasoning\n")
	}
	long.WriteString("the very last thought")
	m.applyEvent(thinkingDelta(long.String(), true))

	out := stripTestANSI(renderTranscript(m.transcript, 60))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1+thinkingTailLines {
		t.Errorf("a live block drew %d lines, want a header and %d:\n%s", len(lines), thinkingTailLines, out)
	}
	if !strings.Contains(out, "the very last thought") {
		t.Errorf("the tail lost the newest text:\n%s", out)
	}
	if got := thinkingTail(strings.Repeat("가", 5000), 60); !strings.HasPrefix(got, "가") {
		t.Errorf("the tail was cut inside a character: %q", got[:6])
	}
}

// The live block's clock moves between deltas, once a second.
func TestTheLiveBlocksClockTicks(t *testing.T) {
	m := newTestModel()
	m.applyEvent(thinkingDelta("hm", true))
	if m.tickThinking() {
		t.Error("the clock moved within the same second")
	}
	m.thinkingSince = time.Now().Add(-5 * time.Second)
	if !m.tickThinking() || m.thinkingEntries()[0].note != "Thinking · 5s" {
		t.Errorf("after five seconds the header says %q", m.thinkingEntries()[0].note)
	}
}

// A model that reasons after it has started answering: the block goes in
// front of the answer, and the answer is drawn once. Splitting the reply
// at the block had message.part.end write the whole reply again below
// it, the part above included.
func TestReasoningAfterTheAnswerStartedDoesNotRepeatTheAnswer(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "Part one. "}})
	m.applyEvent(thinkingDelta("wait, reconsider", true))
	m.applyEvent(events.Event{Type: events.TypeThinkingEnd, Data: map[string]any{"fold": true, "elapsed_ms": float64(1000)}})
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "Part two."}})
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "Part one. Part two."}})

	out := stripTestANSI(renderTranscript(m.transcript, 80))
	if n := strings.Count(out, "Part one."); n != 1 {
		t.Errorf("the answer's first part is drawn %d times:\n%s", n, out)
	}
	if !strings.Contains(out, "Part one. Part two.") {
		t.Errorf("the answer is not drawn whole:\n%s", out)
	}
	if b := m.thinkingEntries(); len(b) != 1 || b[0].live {
		t.Errorf("blocks = %#v", b)
	}
	if last := m.transcript[len(m.transcript)-1]; last.kind != entryModel {
		t.Errorf("the answer is not the last entry: %#v", m.transcript)
	}
}

// After a reconnect the daemon replays a finished reply as its end alone,
// and the reasoning's own end was never logged. The end folds the block,
// and the next request's reasoning gets a block of its own.
func TestAReplyArrivingAsItsEndAloneFoldsTheBlock(t *testing.T) {
	m := newTestModel()
	m.applyEvent(thinkingDelta("first request", true))
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "Answer one."}})
	m.applyEvent(thinkingDelta("second request", true))
	b := m.thinkingEntries()
	if len(b) != 2 || b[0].live || b[0].text != "first request" || b[1].text != "second request" {
		t.Errorf("blocks = %#v", b)
	}
}

// A new prompt closes a block its turn left open.
func TestANewPromptFoldsABlockLeftOpen(t *testing.T) {
	m := newTestModel()
	m.applyEvent(thinkingDelta("old turn", true))
	m.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "second question"}})
	m.applyEvent(thinkingDelta("new turn", true))
	b := m.thinkingEntries()
	if len(b) != 2 || b[0].live || b[0].text != "old turn" || b[1].text != "new turn" {
		t.Errorf("blocks = %#v", b)
	}
}

// Reasoning of whitespace alone opens no block: it would show nothing
// and still count for Ctrl+O, which then did nothing on screen and left
// the next block open.
func TestWhitespaceReasoningOpensNoBlock(t *testing.T) {
	m := newTestModel()
	m.applyEvent(thinkingDelta("\n\n", true))
	m.applyEvent(events.Event{Type: events.TypeThinkingEnd, Data: map[string]any{"fold": true}})
	if b := m.thinkingEntries(); len(b) != 0 {
		t.Fatalf("whitespace opened a block: %#v", b)
	}
	next, _, _ := m.handleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = next.(Model)
	if m.thinkingExpanded {
		t.Error("Ctrl+O with nothing to open changed what the next block does")
	}
	// A block whose first delta is whitespace opens on the first text.
	m.applyEvent(thinkingDelta("\n", true))
	m.applyEvent(thinkingDelta("real text", true))
	if b := m.thinkingEntries(); len(b) != 1 || b[0].text != "real text" {
		t.Errorf("blocks = %#v", b)
	}
}

// Ctrl+O with nothing to open says so only between replies: a note
// written while an answer streams closed it, and the rest of the answer
// was then drawn again below the note.
func TestCtrlOWithNothingToOpenLeavesAStreamingAnswerAlone(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "Hello"}})
	next, _, handled := m.handleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = next.(Model)
	if !handled {
		t.Fatal("Ctrl+O fell through to the prompt box")
	}
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": ", world"}})
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "Hello, world"}})
	out := stripTestANSI(renderTranscript(m.transcript, 80))
	if n := strings.Count(out, "Hello"); n != 1 {
		t.Errorf("the answer is drawn %d times:\n%s", n, out)
	}
}

// Command output ends a message too, and folds a block left open, as the
// Web UI has it. (Found by the first external review.)
func TestShellCommandOutputFoldsALiveBlock(t *testing.T) {
	m := newTestModel()
	m.applyEvent(thinkingDelta("reasoning under way", true))
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{
		"text": "out", "shell_command": "ls",
	}})
	if got := m.liveThinking(); got >= 0 {
		t.Fatalf("live block still open after the command's output (index %d)", got)
	}
}

// Reasoning the fold does not apply to closes a block still open, so the
// next muse request's reasoning is not written into it. (Found by the
// first external review.)
func TestPlainReasoningClosesALiveBlock(t *testing.T) {
	m := newTestModel()
	m.applyEvent(thinkingDelta("muse reasoning", true))
	m.applyEvent(thinkingDelta("other model's reasoning", false))
	if got := m.liveThinking(); got >= 0 {
		t.Fatalf("live block still open after plain reasoning (index %d)", got)
	}
	m.applyEvent(thinkingDelta("muse again", true))
	if b := m.thinkingEntries(); len(b) != 2 || b[1].text != "muse again" {
		t.Errorf("blocks = %#v", b)
	}
}
