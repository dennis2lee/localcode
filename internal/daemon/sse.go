package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"localcode/internal/events"
)

// handleEvents streams the session's event log as SSE: any backlog since
// the given seq first, then live events. Subscribing before reading the
// backlog (rather than after) means the only failure mode is a duplicate
// event across the two sources, never a gap — duplicates are filtered by
// seq before being written to the client.
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

	for {
		select {
		case ev, ok := <-live:
			if !ok {
				return
			}
			writeSSE(ev)
		case <-r.Context().Done():
			return
		}
	}
}
