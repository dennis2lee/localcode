package agent

import "sync"

// The boundary between admitting work into a session tree and tearing one
// down.
//
// These are the two operations that must not interleave, and until now
// each guarded itself. Deletion cancelled what it could see; admission
// checked a flag on its way past. Both were correct in isolation and the
// gap between them was wide: a spawn that had already created its child
// session and appended to the parent, but had not yet registered its
// cancel func, was invisible to a deletion that had just taken its
// snapshot. The child was then deleted out from under a goroutine that was
// about to start running it.
//
// Closing the gap needs one thing both sides hold, so this is a
// reader/writer boundary in the ordinary sense. Admission is the reader:
// short, frequent, several at a time. Deletion is the writer: rare, slow,
// exclusive over one tree. What makes it work is that a claim waits for
// admissions already in flight before it looks at anything, so the tree it
// then reads is one nothing is still being added to.
//
// Deliberately not a sync.RWMutex. A claim is held across cancelling model
// turns and removing files, which is seconds rather than microseconds, and
// it has to be scoped to one tree rather than to the whole daemon:
// deleting one conversation must not stop another from delegating.
type lifecycle struct {
	mu   sync.Mutex
	cond *sync.Cond

	// admits counts admissions in flight, per tree they are joining.
	//
	// Per tree rather than one number, because a claim on one tree should
	// not wait on delegation happening in another. It used to be one
	// count, on the reasoning that an admission is only a few microseconds
	// long; that reasoning was wrong. An admission window covers a
	// delegate hook, which can be a subprocess, and two persistent session
	// writes. Deleting one conversation is not something to hold up behind
	// a hook belonging to a different one, and sustained delegation
	// elsewhere should not be able to starve it.
	//
	// The key is the parent whose tree is being joined, or topLevel for a
	// new conversation, which belongs to no tree and is drained only by
	// delete-all.
	admits map[string]int

	// total is the sum of admits, kept alongside so claimAll does not have
	// to walk the map.
	total int

	// closing is refcounted, not a boolean. Deleting an ancestor and one
	// of its descendants at the same time claims overlapping sets, and
	// with a flag the first to finish would clear a marker the second was
	// still relying on.
	closing map[string]int

	// closeAll is the same for the whole daemon: delete-all admits
	// nothing anywhere.
	closeAll int
}

// topLevel is the admission key for work that joins no existing tree: a
// new conversation, or a turn starting on one. Nothing but delete-all
// waits for these, because nothing but delete-all can invalidate them.
const topLevel = ""

func newLifecycle() *lifecycle {
	lc := &lifecycle{closing: map[string]int{}, admits: map[string]int{}}
	lc.cond = sync.NewCond(&lc.mu)
	return lc
}

// admit opens an admission window for a child of parentID, or reports
// false if that tree is being deleted. Every caller that returns true must
// call admitted when it is finished, whether it succeeded or not.
//
// Nil-safe, like the trace writer and for the same reason: a Loop
// assembled by hand rather than by New has no boundary to enforce, and
// that is not a reason to panic at the point something wanted to know.
func (lc *lifecycle) admit(parentID string) bool {
	if lc == nil {
		return true
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.closeAll > 0 || lc.closing[parentID] > 0 {
		return false
	}
	lc.admits[parentID]++
	lc.total++
	return true
}

// admitTop opens an admission window for a new top-level conversation or
// for a turn starting on one. Refused only while delete-all holds the
// daemon.
func (lc *lifecycle) admitTop() bool { return lc.admit(topLevel) }

// admitted closes an admission window opened by admit.
func (lc *lifecycle) admitted(parentID string) {
	if lc == nil {
		return
	}
	lc.mu.Lock()
	if lc.admits[parentID] <= 1 {
		delete(lc.admits, parentID)
	} else {
		lc.admits[parentID]--
	}
	lc.total--
	lc.cond.Broadcast()
	lc.mu.Unlock()
}

func (lc *lifecycle) admittedTop() { lc.admitted(topLevel) }

// claim marks ids as closing and waits until nothing is being admitted
// anywhere.
//
// The wait is the point. Marking alone only stops admissions that have not
// started; the caller is about to read the session tree and act on what it
// finds, and an admission halfway through is a child that is neither in
// the tree yet nor stoppable.
func (lc *lifecycle) claim(ids []string) {
	if lc == nil {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for _, id := range ids {
		lc.closing[id]++
	}
	for lc.inFlight(ids) > 0 {
		lc.cond.Wait()
	}
}

// inFlight counts admissions still open into any of ids. Called with the
// lock held.
func (lc *lifecycle) inFlight(ids []string) int {
	n := 0
	for _, id := range ids {
		n += lc.admits[id]
	}
	return n
}

func (lc *lifecycle) release(ids []string) {
	if lc == nil {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for _, id := range ids {
		if lc.closing[id] <= 1 {
			delete(lc.closing, id)
			continue
		}
		lc.closing[id]--
	}
}

// claimAll is claim for every session there is and every session there is
// about to be, which is what delete-all means. Held across the whole
// operation, so the busy check that follows it cannot be overtaken by a
// turn starting a moment later.
func (lc *lifecycle) claimAll() {
	if lc == nil {
		return
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.closeAll++
	for lc.total > 0 {
		lc.cond.Wait()
	}
}

func (lc *lifecycle) releaseAll() {
	if lc == nil {
		return
	}
	lc.mu.Lock()
	if lc.closeAll > 0 {
		lc.closeAll--
	}
	lc.mu.Unlock()
}

// closingAll reports whether a delete-all is in progress, for the daemon
// paths that cannot be drained because they are not short: starting a turn
// and creating a session.
func (lc *lifecycle) closingAll() bool {
	if lc == nil {
		return false
	}
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.closeAll > 0
}
