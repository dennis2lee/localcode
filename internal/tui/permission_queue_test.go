package tui

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localcode/internal/client"
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

// If the resolve POST fails after the key handler settled the modal
// locally, the question is still waiting on the daemon and must come
// back as pending so the user can answer it again.
func TestFailedPermissionResolveRestoresRequest(t *testing.T) {
	t.Run("restores settled request when nothing else arrived", func(t *testing.T) {
		m := armedPending(newTestModel(), false)
		m.settlePermission("p1")
		if m.pending != nil {
			t.Fatalf("expected pending to be nil after settle, got %+v", m.pending)
		}

		updated, _ := m.Update(permissionResolvedMsg{id: "p1", err: errors.New("connection refused")})
		m = updated.(Model)

		if m.pending == nil || m.pending.id != "p1" {
			t.Fatalf("pending = %+v, want p1 restored after failed resolve", m.pending)
		}
		if m.errMsg == "" {
			t.Fatal("expected errMsg to be set on failed resolve")
		}
	})

	t.Run("another question arriving in the meantime stays shown first", func(t *testing.T) {
		m := armedPending(newTestModel(), false)
		m, _, handled := press(m, "y")
		if !handled {
			t.Fatal("y did not settle the request")
		}
		m.applyEvent(events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
			"id": "p2", "tool": "bash", "description": "second", "rule": "run *",
		}})

		updated, _ := m.Update(permissionResolvedMsg{id: "p1", err: errors.New("connection refused")})
		m = updated.(Model)

		if m.pending == nil || m.pending.id != "p2" {
			t.Fatalf("on screen = %+v, want p2 to stay shown first", m.pending)
		}
		if len(m.pendingQueue) != 1 || m.pendingQueue[0].id != "p1" {
			t.Fatalf("pendingQueue = %+v, want p1 queued behind p2", m.pendingQueue)
		}
	})

	t.Run("resolved by another client in the meantime does not come back", func(t *testing.T) {
		m := armedPending(newTestModel(), false)
		m, _, handled := press(m, "y")
		if !handled {
			t.Fatal("y did not settle the request")
		}
		m.applyEvent(events.Event{Type: events.TypePermissionResolved, Data: map[string]any{
			"id": "p1", "allow": true, "scope": "once",
		}})

		updated, _ := m.Update(permissionResolvedMsg{id: "p1", err: errors.New("connection refused")})
		m = updated.(Model)

		if m.pending != nil {
			t.Fatalf("pending = %+v, want nil because another client already resolved p1", m.pending)
		}
		if len(m.pendingQueue) != 0 {
			t.Fatalf("pendingQueue = %+v, want empty", m.pendingQueue)
		}
	})

	for _, p1First := range []bool{true, false} {
		name := "two answers in flight: p1 fails then p2 succeeds"
		if !p1First {
			name = "two answers in flight: p2 succeeds then p1 fails"
		}
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/permissions/p1") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprint(w, `{"error":"daemon unreachable"}`)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()

			m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event), false)
			m = armedPending(m, false)
			m.pendingQueue = []*pendingPermission{
				{id: "p2", tool: "bash", description: "second", rule: "run *"},
			}

			m, cmd1, handled := press(m, "y")
			if !handled || cmd1 == nil {
				t.Fatal("first y did not answer p1")
			}
			m.pendingSince = time.Now().Add(-time.Hour)
			m, cmd2, handled := press(m, "y")
			if !handled || cmd2 == nil {
				t.Fatal("second y did not answer p2")
			}
			if m.pending != nil || len(m.pendingQueue) != 0 {
				t.Fatalf("expected no pending requests before POSTs return, got pending=%+v queue=%+v", m.pending, m.pendingQueue)
			}

			msg1 := cmd1()
			msg2 := cmd2()
			if p1First {
				updated, _ := m.Update(msg1)
				m = updated.(Model)
				updated, _ = m.Update(msg2)
				m = updated.(Model)
			} else {
				updated, _ := m.Update(msg2)
				m = updated.(Model)
				updated, _ = m.Update(msg1)
				m = updated.(Model)
			}

			if m.pending == nil || m.pending.id != "p1" {
				t.Fatalf("pending = %+v, want p1 shown again and p2 not", m.pending)
			}
			if len(m.pendingQueue) != 0 {
				t.Fatalf("pendingQueue = %+v, want empty (p2 succeeded)", m.pendingQueue)
			}
			if m.errMsg == "" {
				t.Fatal("expected errMsg to be set after p1 failed")
			}
		})
	}
}
