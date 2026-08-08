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
	// pending holds messages typed while a turn was running, in the order
	// they were sent. Under the same lock as cancels on purpose: "is a
	// turn running" and "can this text still be handed to it" have to be
	// answered together, or a message accepted a moment after the turn
	// decided to finish would sit in the queue forever.
	pending map[string][]string
}

func newTurnTracker() turnTracker {
	return turnTracker{
		cancels: map[string]context.CancelFunc{},
		pending: map[string][]string{},
	}
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

// end clears id's registration once its turn is over, dropping anything
// still queued for it — used on the paths that abandon the turn
// (cancellation, a failed request), where nothing is left to deliver the
// queue to. The ordinary end of a turn goes through finishOrTake instead,
// which does not throw the queue away.
func (t *turnTracker) end(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.cancels, id)
	delete(t.pending, id)
}

// inject hands text to the turn currently running for id, to be picked up
// at its next tool call. Reports false when no turn is running, which
// means the caller should just start one.
func (t *turnTracker) inject(id, text string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, running := t.cancels[id]; !running {
		return false
	}
	t.pending[id] = append(t.pending[id], text)
	return true
}

// takeOne pops the next queued message for id, if any. This is what the
// agent loop calls between tool calls.
func (t *turnTracker) takeOne(id string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	q := t.pending[id]
	if len(q) == 0 {
		return "", false
	}
	text := q[0]
	if len(q) == 1 {
		delete(t.pending, id)
	} else {
		t.pending[id] = q[1:]
	}
	return text, true
}

// finishOrTake ends a turn, atomically: either there is still something
// queued — return it, and stay registered as running so a further message
// keeps queueing rather than racing a second turn into existence — or
// there is not, and the registration is cleared here, under the same lock
// inject checks. That is what closes the window where a message accepted
// microseconds before a turn ended would never be answered by anyone.
func (t *turnTracker) finishOrTake(id string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if q := t.pending[id]; len(q) > 0 {
		text := q[0]
		if len(q) == 1 {
			delete(t.pending, id)
		} else {
			t.pending[id] = q[1:]
		}
		return text, true
	}
	delete(t.cancels, id)
	delete(t.pending, id)
	return "", false
}

// cancel stops id's turn if one is running, and reports whether it was —
// the endpoint behind Esc in the clients. The turn's own goroutine (see
// handleSendMessage) clears the registration and records the
// turn.cancelled event, so this only has to pull the trigger.
func (t *turnTracker) cancel(id string) bool {
	t.mu.Lock()
	c, running := t.cancels[id]
	// Stopping the turn drops what was queued for it. Someone who hits
	// stop wants the work to end, not for the messages they typed while
	// waiting to be picked up and acted on afterwards.
	delete(t.pending, id)
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
		// A turn is already running. Rather than refusing, hand the text
		// to that turn: the agent loop asks for queued input at every
		// tool call, so it lands at the model's next step instead of
		// waiting for the whole job to finish. That is the difference
		// between redirecting a long task and watching it finish the
		// wrong thing.
		if d.turns.inject(id, req.Text) {
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "injected"})
			return
		}
		// The turn ended in the gap between begin and inject. Nothing is
		// running now, so the client can simply send it again.
		writeError(w, http.StatusConflict, fmt.Errorf("session %s is already processing a message", id))
		return
	}

	go func() {
		text := req.Text
		for {
			err := d.Loop.SendMessage(turnCtx, id, sess.Agent, text)

			// Read the cancellation state BEFORE calling cancel() below —
			// cancel() makes turnCtx.Err() non-nil unconditionally, so
			// checking it afterwards would classify every successful turn
			// as user-cancelled.
			wasCancelled := turnCtx.Err() != nil

			// A cancelled turn is a user action, not a failure: record it
			// as its own event so clients can drop the spinner without
			// showing an error. Checking the context rather than the
			// error keeps this correct no matter which layer noticed the
			// cancellation first.
			if wasCancelled {
				cancel()
				d.turns.end(id)
				d.Loop.Store.Append(id, events.TypeTurnCancelled, map[string]any{})
				return
			}
			if err != nil {
				log.Printf("session %s: SendMessage: %v", id, err)
				cancel()
				d.turns.end(id)
				d.Loop.Store.Append(id, events.TypeTurnDone, map[string]any{})
				return
			}

			// A message can arrive after the model's last tool call — while
			// it was writing its closing reply, or in the instant before
			// this line. The loop consumed everything queued up to its
			// final step; whatever came after is still owed an answer, and
			// gets one as a turn of its own rather than being dropped.
			//
			// finishOrTake also clears the registration when there is
			// nothing left, and does it under the same lock inject checks:
			// that is what makes "nothing queued" and "no longer running"
			// a single decision, with no window in between for a message
			// to fall into.
			next, more := d.turns.finishOrTake(id)
			if !more {
				cancel()
				// The turn boundary clients act on, appended after the
				// registration is cleared. Clients send their next message
				// the moment they see it, so the other order is a race:
				// event observed, still registered as busy, 409.
				d.Loop.Store.Append(id, events.TypeTurnDone, map[string]any{})
				return
			}
			text = next
		}
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
