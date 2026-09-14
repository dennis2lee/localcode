package tui

import (
	"testing"

	"localcode/internal/events"
)

// An Orchestrate fanout raises up to four permission requests at once,
// and the broker holds one question per waiter rather than one flag for
// the session. The screen must show the oldest unresolved request, show
// the next when one resolves, and unlock the composer only when the
// queue empties — and replaying the log must drop each answered request
// instead of stacking modals from last week behind the live one.
func TestPermissionQueueKeepsTheOldestUnresolvedRequestOnScreen(t *testing.T) {
	req := func(id string) events.Event {
		return events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
			"id": id, "tool": "bash", "description": "run " + id, "rule": "run *",
		}}
	}
	res := func(id string) events.Event {
		return events.Event{Type: events.TypePermissionResolved, Data: map[string]any{
			"id": id, "allow": true, "scope": "once",
		}}
	}
	cases := []struct {
		name string
		evs  []events.Event
		// wantShown is the id on screen, or "" for no modal.
		wantShown string
		// wantQueued are the ids waiting behind it, oldest first.
		wantQueued []string
	}{
		{
			name:       "a single request shows",
			evs:        []events.Event{req("p1")},
			wantShown:  "p1",
			wantQueued: nil,
		},
		{
			name:       "a second request waits behind the first",
			evs:        []events.Event{req("p1"), req("p2")},
			wantShown:  "p1",
			wantQueued: []string{"p2"},
		},
		{
			// The bug that bit: answering the second closed the first
			// without answering it, stranding its turn.
			name:       "resolving the second leaves the first on screen",
			evs:        []events.Event{req("p1"), req("p2"), res("p2")},
			wantShown:  "p1",
			wantQueued: nil,
		},
		{
			name:       "resolving the shown one promotes the next",
			evs:        []events.Event{req("p1"), req("p2"), res("p1")},
			wantShown:  "p2",
			wantQueued: nil,
		},
		{
			name:       "resolving the shown one with nothing queued clears",
			evs:        []events.Event{req("p1"), res("p1")},
			wantShown:  "",
			wantQueued: nil,
		},
		{
			// Both halves of every permission live in the log, so a
			// reopened session replays each old answer in turn. Without
			// the drop, answering nothing fills the queue with answered
			// questions and the live modal sits behind them.
			name: "replay drops answered pairs and shows the live request",
			evs: []events.Event{
				req("p-old-1"), res("p-old-1"),
				req("p-old-2"), res("p-old-2"),
				req("p-live"),
			},
			wantShown:  "p-live",
			wantQueued: nil,
		},
		{
			name:       "a resolution for an unknown id changes nothing",
			evs:        []events.Event{req("p1"), req("p2"), res("p7")},
			wantShown:  "p1",
			wantQueued: []string{"p2"},
		},
		{
			name:       "three fanout requests drain in order",
			evs:        []events.Event{req("p1"), req("p2"), req("p3"), res("p1"), res("p2")},
			wantShown:  "p3",
			wantQueued: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			for _, ev := range tc.evs {
				m.applyEvent(ev)
			}
			var shown string
			if m.pending != nil {
				shown = m.pending.id
			}
			if shown != tc.wantShown {
				t.Errorf("on screen is %q, want %q", shown, tc.wantShown)
			}
			var queued []string
			for _, q := range m.pendingQueue {
				queued = append(queued, q.id)
			}
			if len(queued) != len(tc.wantQueued) {
				t.Fatalf("queued %v, want %v", queued, tc.wantQueued)
			}
			for i := range queued {
				if queued[i] != tc.wantQueued[i] {
					t.Errorf("queued %v, want %v", queued, tc.wantQueued)
					break
				}
			}
			// The composer is locked exactly while a request is on
			// screen: handleEnter refuses every message until the
			// queue empties, and sends once it has.
			m.input.SetValue("hello")
			_, cmd := handleEnter(m)
			if tc.wantShown == "" && cmd == nil {
				t.Error("Enter did nothing with the queue empty; the session is still wedged")
			}
			if tc.wantShown != "" && cmd != nil {
				t.Error("Enter sent a message while a request was still on screen")
			}
		})
	}
}

// Answering at the keyboard promotes the next queued request now, rather
// than waiting for this answer's own resolved event to come back — that
// event then matches nothing and is ignored, which is what an answer
// already given should do.
func TestAnsweringAtTheKeyboardPromotesTheNextRequest(t *testing.T) {
	m := armedPending(newTestModel(), false)
	m.pendingQueue = []*pendingPermission{
		{id: "p2", tool: "bash", description: "second", rule: "run *"},
	}

	next, cmd, handled := press(m, "y")
	if !handled || cmd == nil {
		t.Fatal("y did not answer the request on screen")
	}
	if next.pending == nil || next.pending.id != "p2" {
		t.Fatalf("the next request did not come up: %+v", next.pending)
	}
	if len(next.pendingQueue) != 0 {
		t.Fatalf("the promoted request stayed queued: %+v", next.pendingQueue)
	}
}
