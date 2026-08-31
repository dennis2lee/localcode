package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
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

	// noInject names the sessions whose slot is held by something that is
	// not a turn: an archive, and in time anything else that needs the
	// session to itself for a moment.
	//
	// It exists because inject is the fallback when begin loses, and a
	// message handed to a holder that will never run it is a message
	// dropped in silence. Under the same lock as cancels for the reason
	// pending is: "who holds this" and "may text be given to them" are one
	// question.
	noInject map[string]bool

	// onChange is called when a session starts or stops being busy, so
	// clients can show which conversations are working without polling.
	// Set once at construction; nil in tests that only exercise the
	// bookkeeping.
	onChange func(sessionID string, busy bool)
}

func newTurnTracker() turnTracker {
	return turnTracker{
		cancels:  map[string]context.CancelFunc{},
		pending:  map[string][]string{},
		noInject: map[string]bool{},
	}
}

// begin registers a turn for id and reports true, or reports false (and
// registers nothing) when one is already running — the caller turns that
// into a 409.
func (t *turnTracker) begin(id string, cancel context.CancelFunc) bool {
	t.mu.Lock()
	if _, running := t.cancels[id]; running {
		t.mu.Unlock()
		return false
	}
	t.cancels[id] = cancel
	t.mu.Unlock()
	t.changed(id, true)
	return true
}

// beginExclusive takes the session's slot for something that is not a
// turn, so nothing else can start one while it runs.
//
// Deciding and registering in one step, which is the whole point: asking
// "is a turn running" and then archiving is the check-then-act interval a
// turn can start inside. The caller must call end.
//
// Unlike begin, the slot refuses injection. A message arriving while an
// archive holds it must not be queued for a turn that will never exist;
// the caller answers it properly instead.
func (t *turnTracker) beginExclusive(id string, cancel context.CancelFunc) bool {
	t.mu.Lock()
	if _, held := t.cancels[id]; held {
		t.mu.Unlock()
		return false
	}
	t.cancels[id] = cancel
	t.noInject[id] = true
	t.mu.Unlock()
	t.changed(id, true)
	return true
}

// changed announces that a session started or stopped being busy.
//
// Called outside the lock: the notifier fans out to every connected
// client, and holding the tracker's mutex across that would let one slow
// socket block every other session's turn from starting.
func (t *turnTracker) changed(id string, busy bool) {
	if t.onChange != nil {
		t.onChange(id, busy)
	}
}

// end clears id's registration once its turn is over, dropping anything
// still queued for it — used on the paths that abandon the turn
// (cancellation, a failed request), where nothing is left to deliver the
// queue to. The ordinary end of a turn goes through finishOrTake instead,
// which does not throw the queue away.
func (t *turnTracker) end(id string) {
	t.mu.Lock()
	delete(t.cancels, id)
	delete(t.noInject, id)
	delete(t.pending, id)
	t.mu.Unlock()
	t.changed(id, false)
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
	// The slot is held by something that is not a turn, so there is nobody
	// to hand this to. Reporting false sends the caller back to its own
	// refusal, which knows what is actually going on.
	if t.noInject[id] {
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

// hasPending reports whether anything is queued for id, without taking
// it. The agent loop asks before deciding to carry a stalled turn on by
// itself: a message the user has already typed is a better continuation
// than one localcode invents, and it is delivered the moment this turn
// ends (see finishOrTake).
func (t *turnTracker) hasPending(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending[id]) > 0
}

// finishOrTake ends a turn, atomically: either there is still something
// queued — return it, and stay registered as running so a further message
// keeps queueing rather than racing a second turn into existence — or
// there is not, and the registration is cleared here, under the same lock
// inject checks. That is what closes the window where a message accepted
// microseconds before a turn ended would never be answered by anyone.
func (t *turnTracker) finishOrTake(id string) (string, bool) {
	t.mu.Lock()
	if q := t.pending[id]; len(q) > 0 {
		text := q[0]
		if len(q) == 1 {
			delete(t.pending, id)
		} else {
			t.pending[id] = q[1:]
		}
		t.mu.Unlock()
		return text, true
	}
	delete(t.cancels, id)
	delete(t.noInject, id)
	delete(t.pending, id)
	t.mu.Unlock()
	t.changed(id, false)
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

// running lists the sessions with a turn registered, sorted so the answer
// is stable — what handleListSessions decorates the session list with, so
// a client reloading the page can show which conversations are working
// without waiting for the next activity event.
func (t *turnTracker) running() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.cancels))
	for id := range t.cancels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// whileSessionIdle runs fn with the guarantee that this one session has no
// turn in flight and none can start until it returns, or reports that it
// does without running fn at all.
//
// Checking and then acting are two different moments, and this closes the
// gap between them: a turn that began in that gap would run its first
// relative-path write against whichever directory won the race (see
// handleSetWorkspace). It used to guard the whole daemon rather than one
// session — the process had one working directory, so a turn anywhere at
// all had to block a move — until workspaces became per-session in
// v0.39.0 and the guard could narrow to match (see turnTracker.anyBusy
// for the one bulk operation, delete-all, that still spans sessions).
//
// fn runs under the tracker's lock, so it must not block: a stat and a
// store write, not a network call.
func (t *turnTracker) whileSessionIdle(id string, fn func() error) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, running := t.cancels[id]; running {
		return true, nil
	}
	return false, fn()
}

// sendRetries bounds how many times one request will re-attempt to start
// a turn after losing the begin/inject race. Each loss means a turn ended
// in a window of a few instructions, so more than a couple of rounds is a
// pathology rather than contention.
const sendRetries = 3

func (d *Daemon) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// A turn started while delete-all is running is a turn whose log is
	// removed underneath it, and a turn is far too long to make a delete
	// wait for. So it is refused for the duration, with the same 409 a
	// busy session already gives.
	//
	// The window is held until the turn is registered with the tracker,
	// not merely until the check has been read. Once it is registered,
	// delete-all's own busy check can see it; before that it is invisible
	// to everything, and a delete claiming the daemon in that gap would
	// close the session log under a turn about to start.
	release, ok := d.admitTopLevel(w)
	if !ok {
		return
	}
	defer release()

	sess, err := d.Loop.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	// Cheap and early, so an ordinary client that has not refreshed gets
	// the right answer without the daemon doing any work. It is not the
	// authoritative one: the flag can move between here and the claim
	// below, which is why it is asked again after.
	if refuseArchived(w, sess) {
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
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

	// Two outcomes are ordinary here — starting a turn, and handing the
	// text to one already running — and one is a race that used to be
	// reported as if it were the second.
	//
	// A turn already running is not refused: the text goes to that turn,
	// which the agent loop picks up at its next tool call, so a
	// correction lands mid job rather than after it. But the turn can
	// also end in the gap between begin and inject, and then neither
	// applied and the answer was a 409 reading "already processing a
	// message" — about a session that had just gone idle. The client
	// cannot tell that apart from the ordinary busy 409, so the TUI
	// queued the prompt and waited for a turn.done that had already been
	// appended: the message sat there, unsent, with the spinner running,
	// until the user typed something else.
	//
	// Retrying is the whole fix. Nothing is running at that point, so the
	// next begin is the one that should have happened.
	started := false
	for range sendRetries {
		if d.turns.begin(id, cancel) {
			started = true
			break
		}
		if d.turns.inject(id, req.Text) {
			cancel()
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "injected"})
			return
		}
	}

	// The authoritative read. An archive holds the session's slot for its
	// whole duration and refuses injection, so losing the loop above can
	// mean an archive rather than a turn, and the flag can have moved
	// since the early check either way. Asked after the claim is settled,
	// which is the only point at which the answer stops changing.
	if fresh, err := d.Loop.Store.Get(id); err == nil && fresh.ArchivedAt != nil {
		if started {
			d.turns.end(id)
		}
		cancel()
		release()
		refuseArchived(w, fresh)
		return
	}
	// Committed, or not starting. Either way the window is done with; the
	// turn itself runs long after this returns and is covered from here on
	// by the tracker, which is what delete-all checks.
	release()

	if !started {
		cancel()
		// Losing the race this many times in a row means turns are
		// starting and ending faster than this handler can observe, which
		// is not something to keep retrying inside a request.
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
				_, err := d.Loop.Store.Append(id, events.TypeTurnCancelled, map[string]any{})
				logAppend(id, events.TypeTurnCancelled, err)
				return
			}
			if err != nil {
				log.Printf("session %s: SendMessage: %v", id, err)
				cancel()
				d.turns.end(id)
				_, err := d.Loop.Store.Append(id, events.TypeTurnDone, map[string]any{})
				logAppend(id, events.TypeTurnDone, err)
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
				_, err := d.Loop.Store.Append(id, events.TypeTurnDone, map[string]any{})
				logAppend(id, events.TypeTurnDone, err)
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
