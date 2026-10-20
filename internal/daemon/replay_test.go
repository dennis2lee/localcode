package daemon

import (
	"reflect"
	"testing"

	"localcode/internal/events"
)

func ev(typ events.Type, seq uint64, text string) events.Event {
	return events.Event{Type: typ, Seq: seq, Data: map[string]any{"text": text}}
}

// A finished reply is in the log twice: once as the fragments it streamed
// in, and once whole, in the message.part.end. Replaying both cost the
// client a markdown re-render per fragment for text it then replaced.
func TestReplayDropsTheFragmentsOfFinishedReplies(t *testing.T) {
	in := []events.Event{
		ev(events.TypeUserMessage, 1, "hello"),
		ev(events.TypeMessagePartDelta, 2, "he"),
		ev(events.TypeMessagePartDelta, 3, "llo "),
		ev(events.TypeMessagePartDelta, 4, "there"),
		ev(events.TypeMessagePartEnd, 5, "hello there"),
		ev(events.TypeTurnDone, 6, ""),
	}
	out := collapseFinishedDeltas(in)
	if len(out) != 3 {
		t.Fatalf("replayed %d events, want the user message, the whole reply and turn.done: %+v", len(out), out)
	}
	if out[0].Type != events.TypeUserMessage || out[1].Type != events.TypeMessagePartEnd {
		t.Errorf("wrong events survived: %+v", out)
	}
	if got := out[1].Data["text"]; got != "hello there" {
		t.Errorf("the whole reply is %q, so nothing of it is lost", got)
	}
}

// The fragments after the last message.part.end belong to a reply that is
// still being written, where they are the only text there is. Dropping
// them would make re-opening a conversation mid-sentence show nothing.
func TestReplayKeepsTheFragmentsOfAReplyStillArriving(t *testing.T) {
	in := []events.Event{
		ev(events.TypeUserMessage, 1, "q"),
		ev(events.TypeMessagePartDelta, 2, "old "),
		ev(events.TypeMessagePartEnd, 3, "old answer"),
		ev(events.TypeUserMessage, 4, "q2"),
		ev(events.TypeMessagePartDelta, 5, "half a "),
		ev(events.TypeMessagePartDelta, 6, "sentence"),
	}
	out := collapseFinishedDeltas(in)
	deltas := 0
	for _, e := range out {
		if e.Type == events.TypeMessagePartDelta {
			deltas++
		}
	}
	if deltas != 2 {
		t.Errorf("replayed %d fragments of the reply in progress, want both", deltas)
	}
}

// A log with nothing finished in it is passed through untouched, rather
// than the "no message.part.end" case silently dropping everything.
func TestReplayLeavesALogWithNoFinishedReplyAlone(t *testing.T) {
	in := []events.Event{
		ev(events.TypeUserMessage, 1, "q"),
		ev(events.TypeMessagePartDelta, 2, "a"),
	}
	if got := collapseFinishedDeltas(in); len(got) != 2 {
		t.Errorf("replayed %d of 2 events", len(got))
	}
}

// A reply that asked for a tool is logged as its fragments, the call's
// tool.start, and then its end: the stream is drained before any tool
// runs. With every fragment dropped, a re-opened conversation drew each
// tool call above the sentence that introduced it. One fragment stays,
// where the reply began, carrying the whole reply.
func TestReplayKeepsAReplyAboveTheToolCallsItAskedFor(t *testing.T) {
	in := []events.Event{
		ev(events.TypeUserMessage, 1, "q"),
		ev(events.TypeMessagePartDelta, 2, "Let me "),
		ev(events.TypeMessagePartDelta, 3, "look."),
		{Type: events.TypeToolStart, Seq: 4, Data: map[string]any{"name": "glob"}},
		{Type: events.TypeToolStart, Seq: 5, Data: map[string]any{"name": "grep"}},
		ev(events.TypeMessagePartEnd, 6, "Let me look."),
		{Type: events.TypeToolEnd, Seq: 7},
		{Type: events.TypeToolStart, Seq: 8, Data: map[string]any{"name": "bash"}},
		ev(events.TypeMessagePartEnd, 9, ""),
		ev(events.TypeMessagePartDelta, 10, "Done."),
		ev(events.TypeMessagePartEnd, 11, "Done."),
		ev(events.TypeTurnDone, 12, ""),
	}
	out := collapseFinishedDeltas(in)
	var got []uint64
	for _, e := range out {
		got = append(got, e.Seq)
	}
	if want := []uint64{1, 2, 4, 5, 6, 7, 8, 9, 11, 12}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed seqs %v, want %v", got, want)
	}
	if text := out[1].Data["text"]; text != "Let me look." {
		t.Errorf("the fragment kept is %q, want the whole reply", text)
	}
	// The log's own event is shared with every reader of it.
	if text := in[1].Data["text"]; text != "Let me " {
		t.Errorf("the logged fragment was rewritten to %q", text)
	}
}
