package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// popupSite serves a page with a link, a link that opens in a new tab,
// and a button that opens the page again in a popup window; /second is
// what the links open. Each control says when the mouse is over it, as
// linkSite's do: see clickWith.
func popupSite(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/second" {
			fmt.Fprint(w, `<!doctype html><title>Second</title><p>the second page</p>`)
			return
		}
		fmt.Fprint(w, `<!doctype html><title>Popups</title>
<style>a,button{position:absolute;left:10px;width:200px;height:30px;display:block}</style>
<a href="/second" style="top:10px">second</a>
<a href="/second" target="_blank" style="top:60px">second, in a new tab</a>
<button onclick="window.open('/', '_blank', 'width=300,height=200')" style="top:110px">popup</button>
<script>
for (const el of document.querySelectorAll('a, button')) {
  el.addEventListener('mouseover', () => { document.title = 'Popups, over'; });
}
</script>`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Where popupSite's link, its new-tab link and its popup button are.
const (
	plainY = 25
	blankY = 75
	popupY = 125
)

// activeWhen waits for conv to work in a tab other than each of not, and
// returns it, loaded.
func activeWhen(t *testing.T, ctx context.Context, m *Manager, conv string, not ...*Tab) *Tab {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		cur := m.Existing(conv)
		fresh := cur != nil
		for _, n := range not {
			fresh = fresh && cur != n
		}
		if fresh {
			cur.waitLoaded(ctx, 10*time.Second)
			return cur
		}
		if time.Now().After(deadline) {
			t.Fatalf("the conversation works in no new tab: %+v\n%s", m.Tabs(ctx, conv), whatTheManagerKnows(ctx, m))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// What a page in a popup window opens covers no other conversation's
// page. A popup window has no tab strip, so the browser puts a tab its
// page opens in the tabbed window that was made last, and that was
// whichever conversation's had opened a tab last: its page was covered,
// the browser stopped drawing it, and its panel showed a still picture.
// The window made last is now one kept for this, and each page that
// lands there has a window to itself.
func TestWhatAPopupsPageOpensCoversNoOtherConversation(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := popupSite(t)
	key, _ := newTabKey()
	open := func(conv string) *Tab {
		t.Helper()
		tab, err := m.Tab(ctx, conv)
		if err != nil {
			t.Fatal(err)
		}
		if err := tab.Navigate(ctx, site); err != nil {
			t.Fatal(err)
		}
		return tab
	}
	window := func(tab *Tab) int {
		t.Helper()
		w := windowOf(ctx, m.proc.conn, tab.TargetID())
		if w == 0 {
			t.Fatalf("the browser does not say which window %s is in", tab.TargetID())
		}
		return w
	}

	// One conversation has a page in a popup window.
	first := open("one")
	if err := clickWith(40, popupY, "left", 0)(ctx, first); err != nil {
		t.Fatal(err)
	}
	popup := activeWhen(t, ctx, m, "one", first)
	// Another opens a tab after it: the tabbed window made last is its.
	other := open("two")

	// A link in the popup that opens in a new tab.
	if err := clickWith(40, blankY, "left", 0)(ctx, popup); err != nil {
		t.Fatal(err)
	}
	opened := activeWhen(t, ctx, m, "one", first, popup)
	drawn(t, ctx, opened, "the page the popup's link opened")
	drawn(t, ctx, other, "the other conversation's page, once the popup's link has opened a tab")
	if window(opened) == window(other) {
		t.Error("the page the popup's link opened is in the other conversation's window")
	}

	// A link in the popup opened behind, with the key held.
	if err := m.ActivateTab("one", popup.TargetID()); err != nil {
		t.Fatal(err)
	}
	if err := clickWith(40, plainY, "left", key)(ctx, popup); err != nil {
		t.Fatal(err)
	}
	tabs := tabsWhen(t, ctx, m, "one", 4)
	if tabs[3].Active {
		t.Errorf("the page opened behind is the tab the conversation works in: %+v", tabs)
	}
	drawn(t, ctx, other, "the other conversation's page, once the popup's link has opened a tab behind")
	if err := m.ActivateTab("one", tabs[3].ID); err != nil {
		t.Fatal(err)
	}
	behind := m.Existing("one")
	drawn(t, ctx, behind, "the page opened behind, switched to")
	drawn(t, ctx, other, "the other conversation's page, once the page opened behind is switched to")

	// The other conversation's own popup opens a tab: it does not land
	// where the first conversation's page did.
	if err := clickWith(40, popupY, "left", 0)(ctx, other); err != nil {
		t.Fatal(err)
	}
	popupTwo := activeWhen(t, ctx, m, "two", other)
	if err := clickWith(40, blankY, "left", 0)(ctx, popupTwo); err != nil {
		t.Fatal(err)
	}
	openedTwo := activeWhen(t, ctx, m, "two", other, popupTwo)
	drawn(t, ctx, openedTwo, "the page the second popup's link opened")
	drawn(t, ctx, behind, "the first conversation's page, once the second popup's link has opened a tab")
	for _, mine := range []*Tab{first, popup, opened, behind} {
		if window(openedTwo) == window(mine) {
			t.Errorf("the page the second popup's link opened is in a window of the first conversation's")
		}
	}

	// A third conversation opens a tab, and then the first popup a link:
	// not in the third's new window.
	third := open("three")
	if err := m.ActivateTab("one", popup.TargetID()); err != nil {
		t.Fatal(err)
	}
	if err := clickWith(40, blankY, "left", 0)(ctx, popup); err != nil {
		t.Fatal(err)
	}
	last := activeWhen(t, ctx, m, "one", first, popup, opened, behind)
	drawn(t, ctx, last, "the page the popup's link opened last")
	drawn(t, ctx, third, "the third conversation's page")
	drawn(t, ctx, openedTwo, "the second conversation's page")

	// Closed, the conversations leave nothing but the page the browser
	// starts with and the one landing window.
	for _, conv := range []string{"one", "two", "three"} {
		<-m.CloseTab(conv)
	}
	deadline := time.Now().Add(10 * time.Second)
	for pagesOpen(t, ctx, m) != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the browser has %d pages open once every conversation is closed, want 2\n%s", pagesOpen(t, ctx, m), whatTheManagerKnows(ctx, m))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The first popup window is what a landing window is made for: before any
// tab is opened after it, what its page opens lands there, and not in the
// window another conversation made before the popup.
func TestTheFirstPopupMakesTheLandingWindow(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := popupSite(t)
	tabs := map[string]*Tab{}
	for _, conv := range []string{"one", "two"} {
		tab, err := m.Tab(ctx, conv)
		if err != nil {
			t.Fatal(err)
		}
		if err := tab.Navigate(ctx, site); err != nil {
			t.Fatal(err)
		}
		tabs[conv] = tab
	}
	// The tabbed window made last is the second conversation's.
	if err := clickWith(40, popupY, "left", 0)(ctx, tabs["one"]); err != nil {
		t.Fatal(err)
	}
	popup := activeWhen(t, ctx, m, "one", tabs["one"])
	if err := clickWith(40, blankY, "left", 0)(ctx, popup); err != nil {
		t.Fatal(err)
	}
	opened := activeWhen(t, ctx, m, "one", tabs["one"], popup)
	drawn(t, ctx, opened, "the page the popup's link opened")
	drawn(t, ctx, tabs["two"], "the other conversation's page")
}

// landingsOf is the landing windows the manager lists.
func landingsOf(m *Manager) map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for target, window := range m.landings {
		out[target] = window
	}
	return out
}

// The landing window is the window made last. Two made at once are told
// apart by their numbers and the later kept; and one whose page closes
// under this manager is made again.
func TestTheLandingWindowIsTheOneMadeLast(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	m, p := managerOn(sb)
	is := func(what string, want map[string]int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for fmt.Sprint(landingsOf(m)) != fmt.Sprint(want) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: the landing windows are %v, want %v", what, landingsOf(m), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// None is kept before a popup.
	sb.answers("Browser.getWindowForTarget", map[string]any{"windowId": 7})
	m.land(p)
	is("with no popup", map[string]int{})
	sb.waitClosed(t, "T1")

	m.mu.Lock()
	m.popups = true
	m.mu.Unlock()
	m.land(p)
	is("the first", map[string]int{"T2": 7})
	// One made before it, and answered after: not kept.
	sb.answers("Browser.getWindowForTarget", map[string]any{"windowId": 5})
	m.land(p)
	is("one made earlier", map[string]int{"T2": 7})
	sb.waitClosed(t, "T3")
	// One made after it takes its place, and the old one's page closes.
	sb.answers("Browser.getWindowForTarget", map[string]any{"windowId": 9})
	m.land(p)
	is("one made later", map[string]int{"T4": 9})
	sb.waitClosed(t, "T2")
	// Its page closes, by something else: another is made.
	sb.answers("Browser.getWindowForTarget", map[string]any{"windowId": 11})
	m.destroyed(p, "T4")
	is("after its page closed", map[string]int{"T5": 11})
	// A tab this manager opens is followed by a new one.
	sb.answers("Browser.getWindowForTarget", map[string]any{"windowId": 13})
	if _, err := m.NewTab(ctxFor(t), "c1"); err != nil {
		t.Fatal(err)
	}
	is("after a tab of the manager's own", map[string]int{"T7": 13})
	sb.waitClosed(t, "T5")
	// One the browser does not give a number for is no use, and closed;
	// the one there is stays.
	sb.answers("Browser.getWindowForTarget", map[string]any{"windowId": 0})
	m.land(p)
	is("one with no number", map[string]int{"T7": 13})
	sb.waitClosed(t, "T9")
}

// A landing window is one from the moment the browser has made it: a
// page that lands in it before its number is known has landed, the window
// is not kept as the landing window, and another is made.
func TestAPageThatLandsWhileTheLandingWindowIsMadeHasLanded(t *testing.T) {
	// The number of the first landing window (the first question of its
	// kind) is held; every window is number 7.
	sb, let := newScriptedBrowser(t, nil, map[string][]int{"Browser.getWindowForTarget": {1}})
	sb.answers("Browser.getWindowForTarget", map[string]any{"windowId": 7})
	m, p := managerOn(sb)
	m.mu.Lock()
	m.popups = true
	m.convs["c2"] = &convTabs{tabs: []*Tab{{conn: sb.conn, targetID: "C", session: "sC"}}, changed: make(chan struct{})}
	m.mu.Unlock()
	made := make(chan struct{})
	go func() {
		m.land(p)
		close(made)
	}()
	sb.waitHeld(t, "Browser.getWindowForTarget#1")
	// A page of another conversation's popup lands in it now.
	m.created(p, "C", "B")
	m.bornAt(p, "B", "visible tabbed")
	drained(t, m, "c2")
	let["Browser.getWindowForTarget"][1]()
	<-made
	if got := landingsOf(m); len(got) != 1 || got["T1"] != 0 {
		if _, kept := got["T1"]; kept {
			t.Fatalf("the window the page landed in is kept as a landing window: %v", got)
		}
	}
	sb.waitClosed(t, "T1")
	if ids, _ := tabsOf(m, "c2"); fmt.Sprint(ids) != "[C B]" {
		t.Errorf("the page that landed is not its conversation's tab: %v", ids)
	}
}

// A landing window that could not be made is made at the next page in a
// popup, and one whose page has closed by the time its number is known
// is not kept.
func TestALandingWindowThatFailedIsMadeAgain(t *testing.T) {
	t.Run("no number for it", func(t *testing.T) {
		sb, _ := newScriptedBrowser(t, nil, nil)
		sb.answers("Browser.getWindowForTarget", map[string]any{"windowId": 0})
		m, p := managerOn(sb)
		m.created(p, "A", "P1")
		m.bornAt(p, "P1", "visible popup")
		drained(t, m, "c1")
		if got := landingsOf(m); len(got) != 0 {
			t.Fatalf("a window with no number is kept as a landing window: %v", got)
		}
		sb.answers("Browser.getWindowForTarget", map[string]any{"windowId": 9})
		m.created(p, "A", "P2")
		m.bornAt(p, "P2", "visible popup")
		drained(t, m, "c1")
		if got := landingsOf(m); len(got) != 1 {
			t.Fatalf("after the next popup the landing windows are %v, want one", got)
		}
	})
	t.Run("its page closed before it was listed", func(t *testing.T) {
		sb, _ := newScriptedBrowser(t, nil, nil)
		sb.answers("Browser.getWindowForTarget", map[string]any{"windowId": 7})
		m, p := managerOn(sb)
		m.mu.Lock()
		m.popups = true
		// The browser said the page had closed before its making was
		// answered: T1 is the next page it makes.
		m.markGone("T1")
		m.mu.Unlock()
		m.land(p)
		if got := landingsOf(m); len(got) != 0 {
			t.Fatalf("a closed page is kept as a landing window: %v", got)
		}
	})
	t.Run("its page closed while it was made", func(t *testing.T) {
		sb, let := newScriptedBrowser(t, nil, map[string][]int{"Browser.getWindowForTarget": {1}})
		sb.answers("Browser.getWindowForTarget", map[string]any{"windowId": 7})
		m, p := managerOn(sb)
		m.mu.Lock()
		m.popups = true
		m.mu.Unlock()
		made := make(chan struct{})
		go func() {
			m.land(p)
			close(made)
		}()
		sb.waitHeld(t, "Browser.getWindowForTarget#1")
		m.destroyed(p, "T1")
		let["Browser.getWindowForTarget"][1]()
		<-made
		deadline := time.Now().Add(2 * time.Second)
		for {
			got := landingsOf(m)
			if _, dead := got["T1"]; !dead && len(got) == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the landing windows are %v, want one that is not the closed T1", got)
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

// A link opened in a window of its own, with Shift held, is in a tabbed
// window the browser made: the window made last. A new landing window is
// made after it, or what a popup's page opened next landed in that
// window, over its conversation's page.
func TestAWindowTheBrowserMadeIsFollowedByALandingWindow(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := popupSite(t)
	open := func(conv string) *Tab {
		t.Helper()
		tab, err := m.Tab(ctx, conv)
		if err != nil {
			t.Fatal(err)
		}
		if err := tab.Navigate(ctx, site); err != nil {
			t.Fatal(err)
		}
		return tab
	}
	first := open("one")
	if err := clickWith(40, popupY, "left", 0)(ctx, first); err != nil {
		t.Fatal(err)
	}
	popup := activeWhen(t, ctx, m, "one", first)
	// The other conversation opens a link in a window of its own.
	other := open("two")
	if err := clickWith(40, plainY, "left", ModShift)(ctx, other); err != nil {
		t.Fatal(err)
	}
	inWindow := activeWhen(t, ctx, m, "two", other)
	drained(t, m, "two")
	// And then the popup's page opens a tab.
	if err := clickWith(40, blankY, "left", 0)(ctx, popup); err != nil {
		t.Fatal(err)
	}
	opened := activeWhen(t, ctx, m, "one", first, popup)
	drawn(t, ctx, opened, "the page the popup's link opened")
	drawn(t, ctx, inWindow, "the other conversation's page, in the window the browser made for it")
	if a, b := windowOf(ctx, m.proc.conn, opened.TargetID()), windowOf(ctx, m.proc.conn, inWindow.TargetID()); a == b {
		t.Errorf("the page the popup's link opened is in the other conversation's window (%d)", a)
	}
}

// With no popup window there is no landing window, and nothing is made
// for one.
func TestNoLandingWindowBeforeAPopup(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := popupSite(t)
	for _, conv := range []string{"one", "two"} {
		tab, err := m.Tab(ctx, conv)
		if err != nil {
			t.Fatal(err)
		}
		if err := tab.Navigate(ctx, site); err != nil {
			t.Fatal(err)
		}
	}
	// A link that opens in a new tab, from a tabbed window.
	one := m.Existing("one")
	if err := clickWith(40, blankY, "left", 0)(ctx, one); err != nil {
		t.Fatal(err)
	}
	activeWhen(t, ctx, m, "one", one)
	if n := pagesOpen(t, ctx, m); n != 4 {
		t.Errorf("the browser has %d pages open, want the three tabs and the page it starts with", n)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.landings) != 0 || m.popups {
		t.Errorf("a landing window is kept with no popup: %v", m.landings)
	}
}
