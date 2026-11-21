package browser

import (
	"context"
	"errors"
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
			m.CloseTab("c1")
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
// left open in the browser.
func TestAPopupOfADeletedConversationsPopupIsClosed(t *testing.T) {
	sb, let := newScriptedBrowser(t, nil, map[string][]int{"Target.attachToTarget": {1, 2}})
	m, p := managerOn(sb)
	m.created(p, "A", "B")
	sb.waitHeld(t, "Target.attachToTarget#1")
	deleted := make(chan struct{})
	go func() {
		m.CloseTab("c1")
		close(deleted)
	}()
	queuedBehind(t, m, "c1", 2)
	m.created(p, "B", "C")
	let["Target.attachToTarget"][1]()
	<-deleted
	sb.waitHeld(t, "Target.attachToTarget#2")
	// C, still open while it attaches, opens E.
	m.created(p, "C", "E")
	let["Target.attachToTarget"][2]()
	drained(t, m, "c1")
	got, _ := tabsOf(m, "c1")
	if closed := sb.closed(); got != nil || !contains(closed, "E") {
		t.Errorf("tabs %v, closed %v; want no tabs and E closed", got, closed)
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
	prompt(t, "closing the conversation", func() { m.CloseTab("c1") })
	let["Runtime.evaluate"][1]()
	if err := <-switched; err != nil {
		t.Errorf("the switch: %v", err)
	}
}
