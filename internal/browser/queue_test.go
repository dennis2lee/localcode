package browser

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// A popup that closes while it attaches is not published: whoever watches
// the conversation is not told, and the tab that was active stays active,
// not moved to the popup's opener as a close of the popup would move it.
func TestAPopupClosedWhileAttachingMovesNothing(t *testing.T) {
	m, p, held, release := standIn(t, "Target.attachToTarget")
	m.mu.Lock()
	c := m.convs["c1"]
	c.tabs = append(c.tabs, &Tab{conn: p.conn, targetID: "X", session: "sX"})
	c.active = 1
	changed := c.changed
	m.mu.Unlock()

	m.created(p, "A", "B")
	waitHeld(t, held, "the attach")
	m.destroyed(p, "B")
	release()
	drained(t, m, "c1")

	select {
	case <-changed:
		t.Error("the watcher was told of a popup that was never published")
	default:
	}
	if got, active := tabsOf(m, "c1"); len(got) != 2 || active != "X" {
		t.Errorf("tabs = %v, active %s; want A and X with X active", got, active)
	}
}

// A new tab whose caller stops waiting for its turn returns then, with the
// caller's error, and opens no page when the turn comes.
func TestANewTabThatStopsWaitingOpensNothing(t *testing.T) {
	sb, let := newScriptedBrowser(t, nil, map[string][]int{"Target.createTarget": {1}})
	m, _ := managerOn(sb)
	first := make(chan error, 1)
	go func() {
		_, err := m.NewTab(context.Background(), "c1")
		first <- err
	}()
	sb.waitHeld(t, "Target.createTarget#1")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() {
		_, err := m.NewTab(ctx, "c1")
		second <- err
	}()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the second new tab: %v, want the deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the second new tab still waits for its turn after its caller stopped")
	}

	let["Target.createTarget"][1]()
	if err := <-first; err != nil {
		t.Fatalf("the first new tab: %v", err)
	}
	drained(t, m, "c1")
	if n := sb.count("Target.createTarget"); n != 1 {
		t.Errorf("the browser was asked for %d pages, want 1", n)
	}
}

// A tab close whose caller stops waiting for its turn returns then, with
// the caller's error, and closes nothing when the turn comes.
func TestAClosingThatStopsWaitingClosesNothing(t *testing.T) {
	m, p, held, release := standIn(t, "Target.attachToTarget")
	m.created(p, "A", "B")
	waitHeld(t, held, "the attach")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	closing := make(chan error, 1)
	go func() { closing <- m.CloseTabID(ctx, "c1", "A") }()
	select {
	case err := <-closing:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the close: %v, want the deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the close still waits for its turn after its caller stopped")
	}

	release()
	drained(t, m, "c1")
	if got, _ := tabsOf(m, "c1"); len(got) != 2 || got[0] != "A" {
		t.Errorf("tabs = %v; want A still there, then B", got)
	}
}

// A page that opens but fails to attach is closed in the browser, and
// leaves no handlers behind on the connection: a popup of a conversation
// deleted while it waited, and a tab the manager opened itself. A page the
// failed one opened before it closed is closed too.
func TestAPageThatFailsToAttachIsClosed(t *testing.T) {
	t.Run("a deleted conversation's popup", func(t *testing.T) {
		sb, let := newScriptedBrowser(t,
			map[string]string{"Page.enable": "page crashed"},
			map[string][]int{"Target.createTarget": {1}})
		m, p := managerOn(sb)
		opened := make(chan struct{})
		go func() {
			_, _ = m.NewTab(context.Background(), "c1")
			close(opened)
		}()
		sb.waitHeld(t, "Target.createTarget#1")
		deleted := make(chan struct{})
		go func() {
			<-m.CloseTab("c1")
			close(deleted)
		}()
		queuedBehind(t, m, "c1", 2)
		// Opened before the delete runs, attached after it.
		m.created(p, "A", "B")
		let["Target.createTarget"][1]()
		<-opened
		<-deleted
		drained(t, m, "c1")
		if closed := sb.closed(); !contains(closed, "B") {
			t.Errorf("the popup was never closed (closed: %v)", closed)
		}
		if n := sb.conn.HandlerCount(); n != 0 {
			t.Errorf("%d handlers left on the connection, want 0", n)
		}
	})
	t.Run("and so is a page it opened", func(t *testing.T) {
		sb, _ := newScriptedBrowser(t, map[string]string{"Page.enable": "page crashed"}, nil)
		m, p := managerOn(sb)
		m.created(p, "A", "B")
		drained(t, m, "c1")
		// B opened E before its close reached the browser.
		m.created(p, "B", "E")
		deadline := time.Now().Add(2 * time.Second)
		for !contains(sb.closed(), "E") {
			if time.Now().After(deadline) {
				t.Fatalf("the page the failed one opened was never closed (closed: %v)", sb.closed())
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	t.Run("a new tab", func(t *testing.T) {
		sb, _ := newScriptedBrowser(t, map[string]string{"Page.enable": "page crashed"}, nil)
		m, _ := managerOn(sb)
		delete(m.convs, "c1")
		if _, err := m.NewTab(context.Background(), "c1"); err == nil {
			t.Fatal("the new tab opened on a page that did not attach")
		}
		if opened, closed := sb.count("Target.createTarget"), len(sb.closed()); opened != 1 || closed != 1 {
			t.Errorf("%d pages opened, %d closed; want the one opened closed", opened, closed)
		}
		if n := sb.conn.HandlerCount(); n != 0 {
			t.Errorf("%d handlers left on the connection, want 0", n)
		}
	})
}

// A popup opens another while it attaches, and the conversation is
// deleted between the two. Neither is any conversation's, and neither is
// left open in the browser: the second is closed without being attached,
// and so is a page it opens before its close reaches the browser.
func TestAPopupOfADeletedConversationsPopupIsClosed(t *testing.T) {
	sb, let := newScriptedBrowser(t, nil, map[string][]int{"Target.attachToTarget": {1}})
	m, p := managerOn(sb)
	m.created(p, "A", "B")
	sb.waitHeld(t, "Target.attachToTarget#1")
	deleted := m.CloseTab("c1")
	queuedBehind(t, m, "c1", 2)
	m.created(p, "B", "C")
	let["Target.attachToTarget"][1]()
	<-deleted
	drained(t, m, "c1")
	m.created(p, "C", "E")
	deadline := time.Now().Add(2 * time.Second)
	for !contains(sb.closed(), "E") {
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	closed := sb.closed()
	if got, _ := tabsOf(m, "c1"); got != nil || !contains(closed, "B") || !contains(closed, "C") || !contains(closed, "E") {
		t.Errorf("tabs %v, closed %v; want no tabs and B, C and E closed", got, closed)
	}
	if n := sb.count("Target.attachToTarget"); n != 1 {
		t.Errorf("%d attaches, want 1: the deleted conversation's second popup was attached", n)
	}
}

// The tab a conversation is asked for waits for what is already opening
// in it, a popup attaching or a new tab, rather than answering with the
// tab that is about to stop being the active one.
func TestTheTabAskedForWaitsForTheOneOpening(t *testing.T) {
	t.Run("a popup", func(t *testing.T) {
		m, p, held, release := standIn(t, "Target.attachToTarget")
		m.created(p, "A", "B")
		waitHeld(t, held, "the attach")
		asked := make(chan *Tab, 1)
		go func() {
			tab, _ := m.Tab(context.Background(), "c1")
			asked <- tab
		}()
		select {
		case got := <-asked:
			t.Errorf("answered with %s while the popup attached", got.targetID)
		case <-time.After(100 * time.Millisecond):
		}
		release()
		if got := <-asked; got == nil || got.targetID != "B" {
			t.Errorf("answered with %v, want the popup", got)
		}
	})
	t.Run("a new tab", func(t *testing.T) {
		sb, let := newScriptedBrowser(t, nil, map[string][]int{"Target.createTarget": {1}})
		m, _ := managerOn(sb)
		opened := make(chan *Tab, 1)
		go func() {
			tab, _ := m.NewTab(context.Background(), "c1")
			opened <- tab
		}()
		sb.waitHeld(t, "Target.createTarget#1")
		asked := make(chan *Tab, 1)
		go func() {
			tab, _ := m.Tab(context.Background(), "c1")
			asked <- tab
		}()
		select {
		case got := <-asked:
			t.Errorf("answered with %s while the new tab opened", got.targetID)
		case <-time.After(100 * time.Millisecond):
		}
		let["Target.createTarget"][1]()
		if got, want := <-asked, <-opened; got != want {
			t.Errorf("answered with %v, want the new tab %v", got, want)
		}
	})
}

// A switch to a tab whose page is slow to paint holds up nothing that
// comes after it in the conversation: the conversation can be closed while
// the page has still not painted.
func TestASlowPaintHoldsUpNoClose(t *testing.T) {
	sb, let := newScriptedBrowser(t, nil, map[string][]int{"Runtime.evaluate": {1}})
	m, _ := managerOn(sb)
	m.convs["c1"].tabs = append(m.convs["c1"].tabs, &Tab{conn: sb.conn, targetID: "B", session: "sB"})
	switched := make(chan error, 1)
	go func() { switched <- m.ActivateTab("c1", "B") }()
	sb.waitHeld(t, "Runtime.evaluate#1")
	prompt(t, "closing the conversation", func() { <-m.CloseTab("c1") })
	let["Runtime.evaluate"][1]()
	if err := <-switched; err != nil {
		t.Errorf("the switch: %v", err)
	}
}

// A page opened by a page nobody has any more is closed, and so is a page
// that one opens before its close reaches the browser.
func TestAPageAnOrphanOpensIsClosed(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	m, p := managerOn(sb)
	m.destroyed(p, "X") // in no list: remembered as gone
	m.created(p, "X", "C")
	m.created(p, "C", "E")
	deadline := time.Now().Add(2 * time.Second)
	for !contains(sb.closed(), "C") || !contains(sb.closed(), "E") {
		if time.Now().After(deadline) {
			t.Fatalf("closed %v; want C and E", sb.closed())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Closing a tab that is not the active one tells whoever watches: a
// switch away from it is told only once the tab switched to paints, so a
// watcher can still be on it.
func TestClosingABackgroundTabTellsTheWatcher(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	m, _ := managerOn(sb)
	c := m.convs["c1"]
	c.tabs = append(c.tabs, &Tab{conn: sb.conn, targetID: "B", session: "sB"})
	c.active = 1
	changed := c.changed
	if err := m.CloseTabID(context.Background(), "c1", "A"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Error("the watcher was not told a tab closed")
	}
	if got, active := tabsOf(m, "c1"); len(got) != 1 || active != "B" {
		t.Errorf("tabs = %v, active %s; want B alone", got, active)
	}
}

// Closing a conversation returns once its turn is taken, behind a tab
// still opening, and closes what that opened when the turn comes.
func TestClosingAConversationWaitsOnNoPage(t *testing.T) {
	m, _, held, release := standIn(t, "Target.createTarget")
	go func() { _, _ = m.NewTab(context.Background(), "c1") }()
	waitHeld(t, held, "the new tab")
	var done <-chan struct{}
	prompt(t, "asking to close the conversation", func() { done = m.CloseTab("c1") })
	release()
	<-done
	if got, _ := tabsOf(m, "c1"); got != nil {
		t.Errorf("the closed conversation has tabs: %v", got)
	}
}

// Whoever watches a closed conversation is told when its tabs go from the
// list, not once its pages have closed in the browser.
func TestClosingAConversationTellsTheWatcherFirst(t *testing.T) {
	sb, let := newScriptedBrowser(t, nil, map[string][]int{"Target.closeTarget": {1}})
	m, _ := managerOn(sb)
	changed := m.convs["c1"].changed
	done := m.CloseTab("c1")
	sb.waitHeld(t, "Target.closeTarget#1")
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Error("the watcher was told only once the page closed")
	}
	let["Target.closeTarget"][1]()
	<-done
}

// Closing the active tab closes its page while the tab that becomes
// active paints, not after it.
func TestAClosedTabsPageClosesWhileTheNextPaints(t *testing.T) {
	sb, let := newScriptedBrowser(t, nil, map[string][]int{"Runtime.evaluate": {1}})
	m, _ := managerOn(sb)
	c := m.convs["c1"]
	c.tabs = append(c.tabs, &Tab{conn: sb.conn, targetID: "B", session: "sB"})
	c.active = 1
	closing := make(chan error, 1)
	go func() { closing <- m.CloseTabID(context.Background(), "c1", "B") }()
	sb.waitHeld(t, "Runtime.evaluate#1")
	deadline := time.Now().Add(2 * time.Second)
	for !contains(sb.closed(), "B") {
		if time.Now().After(deadline) {
			t.Fatal("the closed tab's page waited for the next tab to paint")
		}
		time.Sleep(5 * time.Millisecond)
	}
	let["Runtime.evaluate"][1]()
	if err := <-closing; err != nil {
		t.Error(err)
	}
}

// Switching A to B, back to A and to B again tells whoever watches once
// the last switch's tab paints, not when the first switch's wait ends.
func TestOnlyTheLastSwitchTellsTheWatcher(t *testing.T) {
	sb, let := newScriptedBrowser(t, nil, map[string][]int{"Runtime.evaluate": {1, 2, 3}})
	m, _ := managerOn(sb)
	m.convs["c1"].tabs = append(m.convs["c1"].tabs, &Tab{conn: sb.conn, targetID: "B", session: "sB"})
	for i, id := range []string{"B", "A", "B"} {
		go func() { _ = m.ActivateTab("c1", id) }()
		sb.waitHeld(t, fmt.Sprintf("Runtime.evaluate#%d", i+1))
	}
	m.mu.Lock()
	changed := m.convs["c1"].changed
	m.mu.Unlock()
	let["Runtime.evaluate"][1]()
	select {
	case <-changed:
		t.Fatal("told by the first switch to B while the last one had not painted")
	case <-time.After(50 * time.Millisecond):
	}
	let["Runtime.evaluate"][3]()
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Error("the last switch did not tell the watcher")
	}
}

// expiresOnWake is a context that reports itself expired once told to,
// without ending Done: a caller whose limit runs out at the moment its
// turn is served.
type expiresOnWake struct {
	context.Context
	expired atomic.Bool
}

func (c *expiresOnWake) Err() error {
	if c.expired.Load() {
		return context.DeadlineExceeded
	}
	return nil
}

// The tab asked for by a caller whose limit ran out as its turn came is
// not handed over: the caller is told it stopped waiting.
func TestTheTabAskedForTooLateIsNotHandedOver(t *testing.T) {
	sb, let := newScriptedBrowser(t, nil, map[string][]int{"Target.attachToTarget": {1}})
	m, p := managerOn(sb)
	m.created(p, "A", "B")
	sb.waitHeld(t, "Target.attachToTarget#1")
	ctx := &expiresOnWake{Context: context.Background()}
	asked := make(chan error, 1)
	go func() {
		_, err := m.Tab(ctx, "c1")
		asked <- err
	}()
	queuedBehind(t, m, "c1", 2)
	ctx.expired.Store(true)
	let["Target.attachToTarget"][1]()
	if err := <-asked; !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Tab: %v, want the deadline", err)
	}
}
