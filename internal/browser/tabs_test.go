package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A conversation's tabs, as the panel's tab strip works them: a new tab is
// added and becomes the one the conversation works in, any of them can be
// made that one, and closing the active tab makes the one before it
// active.
func TestAConversationsTabsOpenSwitchAndClose(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := testSite(t)
	first, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Navigate(ctx, site); err != nil {
		t.Fatal(err)
	}
	second, err := m.NewTab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Navigate(ctx, site+"/second"); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != second {
		t.Fatal("a new tab is not the one the conversation works in")
	}
	tabs := m.Tabs(ctx, "c")
	if len(tabs) != 2 || tabs[0].Title != "Form" || tabs[1].Title != "Second" || tabs[0].Active || !tabs[1].Active {
		t.Fatalf("tabs = %+v", tabs)
	}
	// Another conversation's tabs are its own.
	if other := m.Tabs(ctx, "d"); other != nil {
		t.Errorf("a conversation with no tab lists %+v", other)
	}

	if err := m.ActivateTab("c", first.TargetID()); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != first {
		t.Error("switching to the first tab did not make it the active one")
	}
	if err := m.ActivateTab("c", "no-such-tab"); !errors.Is(err, ErrNoSuchTab) {
		t.Errorf("switching to a tab that is not there: %v", err)
	}
	if err := m.ActivateTab("d", first.TargetID()); !errors.Is(err, ErrNoSuchTab) {
		t.Errorf("another conversation switched to this one's tab: %v", err)
	}

	// Closing the active tab, the first, leaves the second active.
	if err := m.CloseTabID(ctx, "c", first.TargetID()); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != second {
		t.Error("closing the active tab did not leave the other one active")
	}
	if tabs := m.Tabs(ctx, "c"); len(tabs) != 1 || tabs[0].ID != second.TargetID() || !tabs[0].Active {
		t.Errorf("after closing one, tabs = %+v", tabs)
	}
	if err := m.CloseTabID(ctx, "c", first.TargetID()); !errors.Is(err, ErrNoSuchTab) {
		t.Errorf("closing a closed tab again: %v", err)
	}
	// Closing the last leaves the conversation with none.
	if err := m.CloseTabID(ctx, "c", second.TargetID()); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != nil || m.Tabs(ctx, "c") != nil {
		t.Error("a conversation whose last tab closed still has one")
	}
}

// Closing a tab that is not the active one leaves the active one where it
// is, and the one before the active tab stays active when it closes.
func TestClosingATabKeepsTheActiveOneInPlace(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	a, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.NewTab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.NewTab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateTab("c", b.TargetID()); err != nil {
		t.Fatal(err)
	}
	if err := m.CloseTabID(ctx, "c", a.TargetID()); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != b {
		t.Error("closing a tab before the active one moved the active one")
	}
	if err := m.CloseTabID(ctx, "c", c.TargetID()); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != b {
		t.Error("closing a tab after the active one moved the active one")
	}
}

// openedByLink clicks the link on from that opens a new tab, and returns
// the tab the conversation moved to.
func openedByLink(t *testing.T, m *Manager, ctx context.Context, from *Tab, conv string, others ...*Tab) *Tab {
	t.Helper()
	snap, err := from.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := from.ClickRef(ctx, refNamed(t, snap, "open second")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cur := m.Existing(conv)
		known := cur == from
		for _, o := range others {
			known = known || cur == o
		}
		if cur != nil && !known {
			cur.waitLoaded(ctx, 10*time.Second)
			return cur
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the link did not open a tab")
	return nil
}

// A tab a link opens sits in its opener's window, in front of it, and a
// browser with no window paints only the tab in front. Switching back to
// the opener brings it forward and lets it paint before the stream moves
// to it, or the panel had one frame of it and then nothing while the model
// worked on it. As the panel watches: one stream, running throughout.
func TestSwitchingBackToAnOpenerShowsIt(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/second":
			fmt.Fprint(w, `<!doctype html><title>Second</title><body style="background:#00f">second</body>`)
		case "/updated":
			fmt.Fprint(w, `<!doctype html><title>Updated</title><body style="background:#0a0">updated</body>`)
		default:
			fmt.Fprint(w, `<!doctype html><title>First</title><body style="background:#f00"><a href="/second" target="_blank">open second</a></body>`)
		}
	}))
	t.Cleanup(srv.Close)
	first, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Navigate(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var frames atomic.Int32
	go m.Watch(wctx, "c", func(f []byte) {
		if bytes.HasPrefix(f, []byte{0xFF, 0xD8}) {
			frames.Add(1)
		}
	})
	time.Sleep(300 * time.Millisecond)
	openedByLink(t, m, ctx, first, "c")
	if err := m.ActivateTab("c", first.TargetID()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	before := frames.Load()
	if err := first.Navigate(ctx, srv.URL+"/updated"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for frames.Load() == before && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if frames.Load() == before {
		t.Fatal("no frame of the opener after it changed, once switched back to: it was not painted")
	}
}

// Closing a tab a link opened goes back to its opener, as a browser does,
// though another tab was opened between them.
func TestClosingALinksTabGoesBackToItsOpener(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := testSite(t)
	a, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Navigate(ctx, site); err != nil {
		t.Fatal(err)
	}
	b, err := m.NewTab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateTab("c", a.TargetID()); err != nil {
		t.Fatal(err)
	}
	c := openedByLink(t, m, ctx, a, "c", b)
	if err := m.CloseTabID(ctx, "c", c.TargetID()); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != a {
		t.Error("closing the link's tab went to the tab before it, not to its opener")
	}
}

// A browser that went away took its tabs: they cannot be switched to or
// closed, and a stream watching one ends rather than waiting for frames
// that will not come.
func TestABrowserThatWentAwayHasNoTabs(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	tab, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	watching := make(chan error, 1)
	go func() { watching <- m.Watch(wctx, "c", func([]byte) {}) }()
	time.Sleep(300 * time.Millisecond)
	m.mu.Lock()
	p := m.proc
	m.mu.Unlock()
	p.kill()
	select {
	case <-p.conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the connection did not end when the browser was killed")
	}
	if err := m.ActivateTab("c", tab.TargetID()); !errors.Is(err, ErrNoSuchTab) {
		t.Errorf("switching to a tab of a browser that went away: %v", err)
	}
	if err := m.CloseTabID(ctx, "c", tab.TargetID()); !errors.Is(err, ErrNoSuchTab) {
		t.Errorf("closing a tab of a browser that went away: %v", err)
	}
	select {
	case <-watching:
	case <-time.After(3 * time.Second):
		t.Error("the stream went on waiting on a tab of a browser that went away")
	}
}
