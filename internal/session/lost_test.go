package session

import (
	"testing"

	"localcode/internal/events"
)

// The report was a reply that arrives halfway and then stops, with the
// spinner running forever.
//
// Every token of model output is one event. Delivery to a subscriber is
// best effort — a stalled reader must never hold up the model — so when
// a reader falls behind, events are dropped. Dropped events are not
// recoverable from the stream: the writer moves on. So the reader loses
// the middle of the reply, and then loses the turn.done that would have
// cleared the spinner, and has no way of knowing either happened.
//
// It cannot be fixed by never dropping. It is fixed by telling the
// reader, so it can reconnect and replay the log.
func TestASubscriberThatFallsBehindIsTold(t *testing.T) {
	s, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	ch, lost, unsub, err := s.Subscribe("s1")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()

	select {
	case <-lost:
		t.Fatal("reported as behind before anything was published")
	default:
	}

	// Nobody reads ch. Publish well past its capacity, the way a model
	// emitting tokens does while a client is busy rendering.
	for i := 0; i < cap(ch)+50; i++ {
		if _, err := s.Append("s1", events.TypeMessagePartDelta, map[string]any{"text": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	// The event that matters most is the last one.
	if _, err := s.Append("s1", events.TypeTurnDone, map[string]any{}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-lost:
	default:
		t.Fatal("events were dropped and the subscriber was never told; it is now showing a truncated reply and waiting for a turn.done that will never arrive")
	}

	// And the log is complete, so a reconnect recovers everything.
	all, err := s.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != cap(ch)+51 {
		t.Errorf("log holds %d events, want all %d — the log is what a reconnect replays", len(all), cap(ch)+51)
	}
	if all[len(all)-1].Type != events.TypeTurnDone {
		t.Errorf("last logged event is %q, want turn.done", all[len(all)-1].Type)
	}
}

// A transient event is true only at the moment it is sent and carries no
// history, so missing one is not falling behind. Tearing down a working
// stream over a missed tokens-per-second reading would turn a cosmetic
// nicety into a visible reconnect.
func TestAMissedTransientEventIsNotFallingBehind(t *testing.T) {
	s, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	ch, lost, unsub, err := s.Subscribe("s1")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()

	for i := 0; i < cap(ch)+50; i++ {
		s.Broadcast("s1", events.TypeUsage, map[string]any{"tps": 12.5, "estimated": true})
	}

	select {
	case <-lost:
		t.Fatal("a missed transient event tore down the stream; only logged events are worth a reconnect")
	default:
	}
}
