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

func (d *Daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	since := uint64(0)
	if s := r.URL.Query().Get("since"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid since: %w", err))
			return
		}
		since = v
	} else if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
		// Browsers' EventSource auto-reconnects on a dropped connection and
		// resends whatever `id:` value the server last sent as this
		// header, so a client that never set ?since= explicitly still
		// resumes without re-fetching events it already has.
		if v, err := strconv.ParseUint(lastEventID, 10, 64); err == nil {
			since = v
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}

	live, unsub, err := d.Loop.Store.Subscribe(id)
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
	writeSSE := func(ev events.Event) {
		if ev.Seq <= lastSeq {
			return // already sent via backlog or an earlier live event
		}
		lastSeq = ev.Seq
		payload, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, payload)
		flusher.Flush()
	}

	for _, ev := range backlog {
		writeSSE(ev)
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
