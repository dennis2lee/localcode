package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"localcode/internal/events"
)

// handleEvents streams the session's event log as SSE: any backlog since
// the given seq first, then live events. Subscribing before reading the
// backlog (rather than after) means the only failure mode is a duplicate
// event across the two sources, never a gap — duplicates are filtered by
// seq before being written to the client.
// heartbeatInterval is how often the event stream writes a comment line
// into an otherwise silent connection. A var, not a const, so a test can
// shorten it rather than sleeping through the real thing.
var heartbeatInterval = 20 * time.Second

// collapseFinishedDeltas removes the streamed text fragments of replies
// that have already finished.
//
// A model reply arrives as one message.part.delta per few characters and
// ends with a message.part.end carrying the whole text — so for any reply
// that is over, every delta in the log is a slice of a string that is
// about to be sent again in full. Replaying them cost the client one
// markdown re-render and one relayout per fragment, for text it then
// replaced.
//
// Only the deltas of *finished* replies go. The ones after the last
// message.part.end belong to a reply still streaming, where they are the
// only text there is — that is what makes re-opening a conversation while
// the model is mid-sentence show the sentence so far rather than nothing.
func collapseFinishedDeltas(evs []events.Event) []events.Event {
	lastEnd := -1
	for i, ev := range evs {
		if ev.Type == events.TypeMessagePartEnd {
			lastEnd = i
		}
	}
	if lastEnd < 0 {
		return evs
	}
	out := make([]events.Event, 0, len(evs))
	for i, ev := range evs {
		if i < lastEnd && ev.Type == events.TypeMessagePartDelta {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func (d *Daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Where to start replaying from, in precedence order: an explicit
	// ?since=, then the Last-Event-ID a reconnecting EventSource sends
	// back, then ?tail=.
	//
	// Last-Event-ID has to beat ?tail= rather than the other way round,
	// and the ordering is the whole correctness argument: EventSource
	// reconnects to the *same URL*, so the ?tail= that opened the stream
	// is still on it. Preferring tail there would re-cut to the end of
	// the log on every dropped connection and silently drop everything
	// the client missed while it was away — which is precisely the
	// failure the resume machinery exists to prevent.
	since := uint64(0)
	switch {
	case r.URL.Query().Get("since") != "":
		v, err := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid since: %w", err))
			return
		}
		since = v

	case r.Header.Get("Last-Event-ID") != "":
		// Browsers' EventSource auto-reconnects on a dropped connection
		// and resends whatever `id:` value the server last sent, so a
		// client that never set ?since= explicitly still resumes without
		// re-fetching what it already has.
		if v, err := strconv.ParseUint(r.Header.Get("Last-Event-ID"), 10, 64); err == nil {
			since = v
		}

	case r.URL.Query().Get("tail") != "":
		// ?tail=N opens a long conversation at its end rather than its
		// beginning: the client asks for roughly the last N events and
		// the daemon moves that cut back to a turn boundary. See
		// Store.TailSince for why the boundary matters as much as the
		// count.
		n, err := strconv.Atoi(r.URL.Query().Get("tail"))
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid tail: %q", r.URL.Query().Get("tail")))
			return
		}
		v, err := d.Loop.Store.TailSince(id, n)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		since = v
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}

	live, lost, unsub, err := d.Loop.Store.Subscribe(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	defer unsub()

	// Daemon-wide events (MCP status) ride the same connection so a client
	// needs only one stream, but they are a separate source: they carry no
	// seq, are never persisted, and so are never part of the backlog. See
	// broadcast.go.
	daemonLive, unsubDaemon := d.daemonEvents.subscribe()
	defer unsubDaemon()

	backlog, err := d.Loop.Store.Events(id, since)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // send headers immediately even if there's no backlog to write yet

	lastSeq := since
	// send writes one event. resume is the value the client should come
	// back with after a dropped connection, and it is the largest sequence
	// sent so far rather than this event's own — see the backlog loop.
	// Zero means no `id:` line at all.
	send := func(ev events.Event, resume uint64) {
		payload, err := json.Marshal(ev)
		if err != nil {
			return
		}
		if resume == 0 {
			fmt.Fprintf(w, "data: %s\n\n", payload)
		} else {
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", resume, payload)
		}
		flusher.Flush()
	}
	writeSSE := func(ev events.Event) {
		if ev.Seq == 0 {
			// A transient event (Store.Broadcast): true only right now,
			// never part of the log. It gets no `id:` and does not move
			// lastSeq — either would corrupt the resume point the browser
			// sends back after a dropped connection.
			send(ev, 0)
			return
		}
		if ev.Seq <= lastSeq {
			return // already sent via backlog or an earlier live event
		}
		lastSeq = ev.Seq
		send(ev, ev.Seq)
	}

	// The backlog is sent as it comes, without the guard above.
	//
	// Its membership was already decided, by Store.Events selecting the
	// events after `since`. Deciding it a second time here against a
	// running maximum drops any event whose sequence fails to exceed the
	// one before it — and while sequences normally ascend, a log where
	// they do not is not hypothetical: two daemons sharing one session
	// directory keep independent counters, and their appends interleave
	// into a file whose sequences repeat and go backwards. Filtering such
	// a file loses conversation that is sitting in it, on every attach,
	// with nothing said. Sending it whole is the only reading of "the
	// record is what happened" that survives a damaged log.
	//
	// The resume id still only ever climbs, so a reconnect cannot be sent
	// backwards by one of those repeats. On an ordinary log every id here
	// is the event's own sequence, exactly as before.
	for _, ev := range collapseFinishedDeltas(backlog) {
		if ev.Seq > lastSeq {
			lastSeq = ev.Seq
		}
		send(ev, lastSeq)
	}

	// A heartbeat, because a stream that says nothing is indistinguishable
	// from a stream that is dead.
	//
	// A turn can run for minutes without producing an event — thinking,
	// or a long tool — and in that gap nothing crosses the connection at
	// all. Anything in the middle (a proxy, a corporate network appliance,
	// an idle-timeout in the OS) is free to drop it, and the browser is
	// not always told: EventSource sits there believing it is connected,
	// no error fires, no reconnect is attempted, and every event from then
	// on is lost. What the person sees is a light that blinks forever, no
	// output, and a stop button that appears to do nothing — because the
	// daemon does cancel, and it is the reply that never arrives.
	//
	// A comment line is the standard remedy: it is valid SSE, clients
	// ignore it, and it gives both ends something to notice a dead
	// connection by. 20 seconds is comfortably inside the 30-60s idle
	// timeout such intermediaries typically use.
	beat := time.NewTicker(heartbeatInterval)
	defer beat.Stop()

	for {
		select {
		case <-beat.C:
			// Any write failure here ends the stream, which is what
			// should happen: the client will reconnect with
			// Last-Event-ID and miss nothing.
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-lost:
			// This stream fell behind and an event was dropped. It cannot
			// be recovered from here — the event is not coming again — so
			// the honest move is to end the response. EventSource
			// reconnects on its own and sends back the last id it saw,
			// and the backlog above replays everything in between.
			//
			// Left unhandled, this is what a message that stops halfway
			// looks like: the middle of a reply is gone, and so is the
			// turn.done that would have cleared the spinner.
			return
		case ev, ok := <-live:
			if !ok {
				return
			}
			writeSSE(ev)
		case ev, ok := <-daemonLive:
			if !ok {
				return
			}
			// No `id:` line and no lastSeq bookkeeping: these events have
			// no sequence of their own, and emitting one would corrupt the
			// Last-Event-ID the browser sends back to resume the session
			// log after a dropped connection.
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
