package daemon

import (
	"testing"
	"time"
)

// The Windows CI runner is where this was found: its clock stood still
// across two CreateSession calls and the second was refused as a
// duplicate. A clock that never moves is the strongest form of that.
func TestSessionIDsDifferEvenWhenTheClockStandsStill(t *testing.T) {
	fixed := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	old := sessionClock
	sessionClock = func() time.Time { return fixed }
	t.Cleanup(func() { sessionClock = old })

	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := newSessionID()
		if seen[id] {
			t.Fatalf("id %s was handed out twice", id)
		}
		seen[id] = true
	}
}
