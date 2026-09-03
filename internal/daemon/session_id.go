package daemon

import (
	"fmt"
	"sync"
	"time"
)

// A session id is its creation time in nanoseconds, which is unique on a
// clock that moves between two calls and not on one that does not.
// Windows advances time.Now in steps — a fraction of a millisecond, up to
// sixteen of them — and two sessions created inside one step got the same
// id, the second refused as "already exists". The id keeps its shape; what
// is added is the guarantee that each one this process hands out is
// greater than the last.
var (
	sessionIDMu   sync.Mutex
	lastSessionID int64
	sessionClock  = time.Now
)

func newSessionID() string {
	sessionIDMu.Lock()
	defer sessionIDMu.Unlock()
	now := sessionClock().UnixNano()
	if now <= lastSessionID {
		now = lastSessionID + 1
	}
	lastSessionID = now
	return fmt.Sprintf("s-%d", now)
}
