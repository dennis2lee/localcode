package daemon

import (
	"sync"

	"localcode/internal/events"
)

// broadcaster carries events that belong to the daemon rather than to any
// one conversation — right now, MCP server status.
//
// These deliberately do NOT go through session.Store. Everything in a
// session's event log is persisted and replayed to a client that
// reconnects, which is exactly right for the conversation and exactly
// wrong for this: MCP status is a fact about the present moment, so
// writing it into every open session's log would both bloat the logs and
// have a reconnecting client replay a stale sequence of light changes
// from an hour ago. These events are fan-out only — no history, no seq,
// and a client that was not connected simply missed them and re-reads the
// current state from the REST endpoint on load.
type broadcaster struct {
	mu   sync.Mutex
	subs map[int]chan events.Event
	next int
}

func newBroadcaster() *broadcaster {
	return &broadcaster{subs: map[int]chan events.Event{}}
}

// subscribe returns a channel of daemon-wide events and a function to stop
// listening. The caller must call unsub, and must keep reading until it
// does — see send for what happens if it doesn't.
func (b *broadcaster) subscribe() (<-chan events.Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	// Buffered: send never blocks on a subscriber that is momentarily busy
	// writing to its socket, which would otherwise stall every other
	// client behind it.
	ch := make(chan events.Event, 8)
	b.subs[id] = ch
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
}

// send fans out to every subscriber, dropping the event for any whose
// buffer is full rather than blocking. Dropping is the right failure here:
// each event carries the complete current state, so a client that misses
// one is corrected by the next, and a wedged client must never be able to
// hold up the health checker or another client.
func (b *broadcaster) send(ev events.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
