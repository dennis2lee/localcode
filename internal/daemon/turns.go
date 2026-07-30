package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"localcode/internal/events"
)

// turnTracker records which sessions currently have a turn in flight and
// how to cancel each one. A session is busy iff it has a cancel func
// registered — the previous design kept a separate `busy map[string]bool`
// alongside `cancels map[string]context.CancelFunc`, both under one lock,
// with a documented invariant that the two must never disagree about
// whether a turn is running. Folding "busy" into "has a cancel registered"
// makes that invariant true by construction instead of by convention.
type turnTracker struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newTurnTracker() turnTracker {
	return turnTracker{cancels: map[string]context.CancelFunc{}}
}

// begin registers a turn for id and reports true, or reports false (and
// registers nothing) when one is already running — the caller turns that
// into a 409.
func (t *turnTracker) begin(id string, cancel context.CancelFunc) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, running := t.cancels[id]; running {
		return false
	}
	t.cancels[id] = cancel
	return true
}

// end clears id's registration once its turn is over.
func (t *turnTracker) end(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.cancels, id)
}

// cancel stops id's turn if one is running, and reports whether it was —
// the endpoint behind Esc in the clients. The turn's own goroutine (see
// handleSendMessage) clears the registration and records the
// turn.cancelled event, so this only has to pull the trigger.
func (t *turnTracker) cancel(id string) bool {
	t.mu.Lock()
	c, running := t.cancels[id]
	t.mu.Unlock()
	if running {
		c()
	}
	return running
}

// busy reports whether id currently has a turn in flight.
func (t *turnTracker) busy(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, running := t.cancels[id]
	return running
}

// anyBusy filters ids down to the ones with a turn currently in flight —
// used by bulk operations (delete-all) that must refuse if any of a set of
// sessions is mid-turn.
func (t *turnTracker) anyBusy(ids []string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var busyIDs []string
	for _, id := range ids {
		if _, running := t.cancels[id]; running {
			busyIDs = append(busyIDs, id)
		}
	}
	return busyIDs
}

// anyRunning reports whether any session anywhere has a turn in flight —
// used by the workspace switch, which is process-wide rather than
// per-session and so refuses on any turn at all, not just a named set.
func (t *turnTracker) anyRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.cancels) > 0
}

func (d *Daemon) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := d.Loop.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("text is required"))
		return
	}

	// Deliberately rooted at context.Background(), not r.Context(): the HTTP
	// request returns immediately (202) and must not cancel the turn when
	// the client disconnects. It is cancellable only on purpose, via
	// handleCancelTurn.
	turnCtx, cancel := context.WithCancel(context.Background())

	if !d.turns.begin(id, cancel) {
		cancel()
		writeError(w, http.StatusConflict, fmt.Errorf("session %s is already processing a message", id))
		return
	}

	go func() {
		err := d.Loop.SendMessage(turnCtx, id, sess.Agent, req.Text)

		// Read the cancellation state BEFORE calling cancel() below —
		// cancel() makes turnCtx.Err() non-nil unconditionally, so
		// checking it afterwards would classify every successful turn as
		// user-cancelled.
		wasCancelled := turnCtx.Err() != nil

		// Clear the registration BEFORE appending the terminal event.
		// Clients send their next (possibly queued) message the moment
		// they see it, so the other order is a race: event observed,
		// still registered as busy, 409.
		cancel()
		d.turns.end(id)

		// A cancelled turn is a user action, not a failure: record it as
		// its own event so clients can drop the spinner without showing an
		// error. Checking the context rather than the error keeps this
		// correct no matter which layer noticed the cancellation first.
		if wasCancelled {
			d.Loop.Store.Append(id, events.TypeTurnCancelled, map[string]any{})
			return
		}
		if err != nil {
			log.Printf("session %s: SendMessage: %v", id, err)
		}
		// The turn boundary clients act on. message.part.end is NOT that
		// boundary — it fires per model message, and a turn with tool
		// calls has several, which is exactly what used to make clients
		// think the turn was over mid-tool and 409 on their next send.
		// Emitted on the error path too (the error event itself already
		// told the user what went wrong; this just marks the turn over).
		d.Loop.Store.Append(id, events.TypeTurnDone, map[string]any{})
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// handleCancelTurn stops the turn currently running for a session, the
// endpoint behind Esc in the clients. Cancelling when nothing is running
// is not an error, just {"cancelled": false} — a user mashing Esc at an
// idle prompt should not see a failure.
func (d *Daemon) handleCancelTurn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	running := d.turns.cancel(id)
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": running})
}
