package browser

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// prompt fails the test if f takes longer than a lock held for no reason
// would let it.
func prompt(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Errorf("%s waited on another conversation's call to the browser", what)
	}
}

// One conversation waiting on the browser holds no other conversation
// up: the manager's lock is not held across a call to the browser.
func TestASlowCallToTheBrowserHoldsNoOtherConversation(t *testing.T) {
	for _, tc := range []struct {
		name, stall string
		slow        func(m *Manager, p *process)
	}{
		{"a link's tab attaching", "Target.attachToTarget", func(m *Manager, p *process) { m.created(p, "opener", "popup") }},
		{"a new tab opening", "Target.createTarget", func(m *Manager, p *process) { m.NewTab(context.Background(), "c1") }},
		{"a first tab opening", "Target.createTarget", func(m *Manager, p *process) { m.Tab(context.Background(), "c3") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, held, _ := stallingBrowser(t, tc.stall)
			p := &process{conn: conn, exited: make(chan struct{})}
			m := NewManager(Options{})
			m.proc = p
			m.convs["c1"] = &convTabs{tabs: []*Tab{{conn: conn, targetID: "opener", session: "s1"}}, changed: make(chan struct{})}
			other := &Tab{conn: conn, targetID: "other", session: "s2"}
			m.convs["c2"] = &convTabs{tabs: []*Tab{other}, changed: make(chan struct{})}
			go tc.slow(m, p)
			select {
			case <-held:
			case <-time.After(3 * time.Second):
				t.Fatalf("the slow call never reached %s", tc.stall)
			}
			prompt(t, "Existing", func() {
				if m.Existing("c2") != other {
					t.Error("the other conversation's tab is not its own")
				}
			})
			prompt(t, "Tabs", func() { m.Tabs(context.Background(), "c2") })
			prompt(t, "ActivateTab", func() { _ = m.ActivateTab("c2", "other") })
		})
	}
}

// Two calls for the same conversation's first tab at once open one tab
// for it: the second to finish closes its own and returns the first's.
func TestTwoFirstTabsAtOnceAreOne(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	tabs := make(chan *Tab, 2)
	for i := 0; i < 2; i++ {
		go func() {
			tab, err := m.Tab(ctx, "c")
			if err != nil {
				t.Error(err)
			}
			tabs <- tab
		}()
	}
	a, b := <-tabs, <-tabs
	if a == nil || a != b {
		t.Fatalf("two calls returned two tabs: %p and %p", a, b)
	}
	if got := m.Tabs(ctx, "c"); len(got) != 1 {
		t.Errorf("the conversation has %d tabs", len(got))
	}
}

// standIn is a manager on a stand-in browser holding stall, with
// conversation c1 on tab "A".
func standIn(t *testing.T, stall string) (*Manager, *process, <-chan struct{}, func()) {
	t.Helper()
	conn, held, release := stallingBrowser(t, stall)
	p := &process{conn: conn, exited: make(chan struct{})}
	m := NewManager(Options{})
	m.proc = p
	m.convs["c1"] = &convTabs{tabs: []*Tab{{conn: conn, targetID: "A", session: "sA"}}, changed: make(chan struct{})}
	return m, p, held, release
}

func waitHeld(t *testing.T, held <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-held:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s never reached the browser", what)
	}
}

// drained waits for every piece of conv's work asked for so far to end.
func drained(t *testing.T, m *Manager, conv string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		q := m.queues[conv]
		idle := q == nil || q.serving == q.next
		m.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("conversation %s's work never ended", conv)
}

// tabsOf is conv's tab ids in order, and the active one.
func tabsOf(m *Manager, conv string) ([]string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.convs[conv]
	if c == nil {
		return nil, ""
	}
	var ids []string
	for _, t := range c.tabs {
		ids = append(ids, t.targetID)
	}
	return ids, c.current().targetID
}

// A page that closes while it is being attached gets its close event
// before it is in any list. It is dropped in its conversation's turn,
// after the attach, not left as a closed active tab.
func TestAPageThatClosesWhileItAttachesIsNotPublished(t *testing.T) {
	m, p, held, release := standIn(t, "Target.attachToTarget")
	m.created(p, "A", "B")
	waitHeld(t, held, "the attach")
	m.destroyed(p, "B")
	release()
	drained(t, m, "c1")
	if got, active := tabsOf(m, "c1"); len(got) != 1 || active != "A" {
		t.Errorf("tabs = %v, active %s; the closed page was published", got, active)
	}
}

// Pages opened by pages, the browser's events in order: B by A, then D by
// B while B is still attaching. Both are the conversation's, in the order
// they opened, and the last opened is the one it works in, whichever
// attach the scheduler would have finished first.
func TestPagesOpenedOneByAnotherKeepTheirOrder(t *testing.T) {
	for i := 0; i < 20; i++ {
		m, p, held, release := standIn(t, "Target.attachToTarget")
		m.created(p, "A", "B")
		m.created(p, "B", "D")
		waitHeld(t, held, "the first attach")
		release()
		drained(t, m, "c1")
		got, active := tabsOf(m, "c1")
		if len(got) != 3 || got[1] != "B" || got[2] != "D" || active != "D" {
			t.Fatalf("tabs = %v, active %s; want A, B, D with D active", got, active)
		}
	}
}

// The page a popup was opened from can close while the popup attaches.
// The popup is the conversation's tab, not closed with it.
func TestAPopupOutlivesItsOpenerClosing(t *testing.T) {
	m, p, held, release := standIn(t, "Target.attachToTarget")
	m.created(p, "A", "B")
	waitHeld(t, held, "the attach")
	m.destroyed(p, "A")
	release()
	drained(t, m, "c1")
	if got, active := tabsOf(m, "c1"); len(got) != 1 || active != "B" {
		t.Errorf("tabs = %v, active %s; want the popup alone", got, active)
	}
}

// A tab still opening when its conversation is deleted does not bring the
// conversation back: the delete waits its turn, behind the open, and
// closes what it opened.
func TestATabOpeningForAClosedConversationIsNotPublished(t *testing.T) {
	m, _, held, release := standIn(t, "Target.createTarget")
	opened := make(chan error, 1)
	go func() {
		_, err := m.NewTab(context.Background(), "c1")
		opened <- err
	}()
	waitHeld(t, held, "the new tab")
	closed := make(chan struct{})
	go func() { m.CloseTab("c1"); close(closed) }()
	release()
	<-opened
	<-closed
	if got, _ := tabsOf(m, "c1"); got != nil {
		t.Errorf("the deleted conversation has tabs again: %v", got)
	}
}

// A page that closed while in no list is remembered by age, not by
// count: however many others close meanwhile, it is not forgotten while a
// tab that is it could still be waiting to be published.
func TestAClosedPageIsRememberedLongEnough(t *testing.T) {
	m := NewManager(Options{})
	for i := 0; i <= 300; i++ {
		m.destroyed(nil, fmt.Sprintf("x%d", i))
	}
	m.mu.Lock()
	kept := m.wasGone("x0")
	m.gone["old"] = time.Now().Add(-goneFor - time.Second)
	m.markGone("new")
	_, stale := m.gone["old"]
	m.mu.Unlock()
	if !kept {
		t.Error("the first of 301 closed pages was forgotten")
	}
	if stale {
		t.Error("a page closed longer ago than goneFor is still remembered")
	}
}

// Closing a conversation's tabs drops its queue too, when nothing else
// waits in it: a deleted or archived conversation leaves nothing behind.
func TestClosingAConversationDropsItsQueue(t *testing.T) {
	m, _, _, release := standIn(t, "Target.createTarget")
	release()
	m.CloseTab("c1")
	m.mu.Lock()
	_, kept := m.queues["c1"]
	m.mu.Unlock()
	if kept {
		t.Error("the closed conversation's queue is still there")
	}
}
