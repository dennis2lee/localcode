package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"
)

// linkSite serves a page with a link and, under it, a button that opens
// the link's page from a script and a form that is sent to it; /framed is
// a page with that page in a frame from another site, and /second what
// the link opens.
func linkSite(t *testing.T) string {
	t.Helper()
	var port string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/second":
			fmt.Fprint(w, `<!doctype html><title>Second</title><p>the second page</p>`)
		case "/framed":
			// The same server under its other name is another site, and a
			// frame from another site is a process and a target of its own.
			fmt.Fprintf(w, `<!doctype html><title>Framed</title><iframe src="http://localhost:%s/" style="position:absolute;left:0;top:0;width:600px;height:400px;border:0"></iframe>
<script>addEventListener('message', (e) => { if (e.data === 'over') document.title = 'Framed, over'; });</script>`, port)
		default:
			// The page says when the mouse is over one of its controls,
			// in its title, and from a frame to the page it is in: see
			// clickWith.
			fmt.Fprint(w, `<!doctype html><title>Links</title>
<style>a,button{position:absolute;left:10px;width:200px;height:30px;display:block}</style>
<a id=link href="/second" style="top:10px">second</a>
<button onclick="window.open('/second')" style="top:60px">open</button>
<form action="/second" method="post"><button style="top:110px">send</button></form>
<script>
for (const el of document.querySelectorAll('a, button')) {
  el.addEventListener('mouseover', () => { document.title = 'Links, over'; if (parent !== window) parent.postMessage('over', '*'); });
}
</script>`)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port = u.Port()
	return srv.URL
}

// Where linkSite's link, its button and its form's button are on the page.
const (
	linkX, linkY     = 40, 25
	buttonX, buttonY = 40, 75
	sendX, sendY     = 40, 125
)

// newTabKey is the key that, held, opens a link in a tab of its own: Cmd
// on macOS, Ctrl elsewhere.
func newTabKey() (int, string) {
	if runtime.GOOS == "darwin" {
		return ModMeta, "cmd"
	}
	return ModCtrl, "ctrl"
}

// clickWith clicks linkSite's control at x, y with button, holding mods,
// once the control has the mouse over it. A frame from another site is
// drawn and takes the mouse a moment after its page has loaded: a click
// sent before then went to the page around it, and opened nothing (seen
// on the macOS runner, twice in twenty-four runs).
func clickWith(x, y float64, button string, mods int) func(context.Context, *Tab) error {
	return func(ctx context.Context, tab *Tab) error {
		deadline := time.Now().Add(10 * time.Second)
		for over := false; !over; {
			if err := tab.Mouse(ctx, MouseEvent{Type: "mouseMoved", X: x, Y: y}); err != nil {
				return err
			}
			if err := tab.eval(ctx, `document.title.endsWith('over')`, &over); err != nil {
				return err
			}
			if !over {
				if time.Now().After(deadline) {
					return fmt.Errorf("the control at (%v, %v) never had the mouse over it", x, y)
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		for _, typ := range []string{"mousePressed", "mouseReleased"} {
			if err := tab.Mouse(ctx, MouseEvent{Type: typ, X: x, Y: y, Button: button, ClickCount: 1, Modifiers: mods}); err != nil {
				return err
			}
		}
		return nil
	}
}

// tabsWhen waits for conv to have n tabs, and returns them.
func tabsWhen(t *testing.T, ctx context.Context, m *Manager, conv string, n int) []TabInfo {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		tabs := m.Tabs(ctx, conv)
		if len(tabs) == n {
			return tabs
		}
		if time.Now().After(deadline) {
			t.Fatalf("the conversation has %d tabs, want %d: %+v\n%s", len(tabs), n, tabs, whatTheManagerKnows(ctx, m))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// whatTheManagerKnows is, for a failure, what tells why a page is not a
// tab: the asks still waiting, the pages claimed and not yet attached,
// the pages that closed unlisted, and every page the browser has open. A
// page that was never asked for, one that was asked for and never made,
// one made and not taken for its ask, one still attaching and one closed
// for failing to attach each read differently here.
func whatTheManagerKnows(ctx context.Context, m *Manager) string {
	short := func(id string) string {
		if len(id) > 8 {
			return id[:8]
		}
		return id
	}
	var b strings.Builder
	m.mu.Lock()
	for _, a := range m.asks {
		fmt.Fprintf(&b, "  ask from %s for %s, %s ago\n", short(a.from), a.url, time.Since(a.at).Round(time.Millisecond))
	}
	for target, conv := range m.adopting {
		fmt.Fprintf(&b, "  claimed for %q, not attached: %s\n", conv, short(target))
	}
	for target, at := range m.gone {
		fmt.Fprintf(&b, "  closed unlisted %s ago: %s\n", time.Since(at).Round(time.Millisecond), short(target))
	}
	var conn = m.connLocked()
	m.mu.Unlock()
	if conn == nil {
		return b.String() + "  no browser\n"
	}
	var out struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			URL      string `json:"url"`
			OpenerID string `json:"openerId"`
		} `json:"targetInfos"`
	}
	if err := conn.Call(ctx, "", "Target.getTargets", nil, &out); err != nil {
		return b.String() + "  the browser's pages: " + err.Error() + "\n"
	}
	for _, ti := range out.TargetInfos {
		if ti.Type == "page" {
			fmt.Fprintf(&b, "  page %s at %q, opener %q\n", short(ti.TargetID), ti.URL, short(ti.OpenerID))
		}
	}
	return b.String()
}

// drawn waits for tab to be the page in front of its window, the only
// one the browser draws, and fails the test if it does not become it.
func drawn(t *testing.T, ctx context.Context, tab *Tab, what string) {
	t.Helper()
	var state string
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := tab.eval(ctx, `document.visibilityState`, &state)
		if err == nil && state == "visible" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s is %q (%v), want visible: the browser does not draw it", what, state, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A link opened in a tab or a window of its own is a tab of the
// conversation: in its tab strip, where it can be switched to and closed.
// The browser makes such a page with no opener, and it was no
// conversation's: nothing listed it, and one opened in front covered the
// conversation's page, which the browser then stopped drawing, so the
// panel showed a still picture that took no click.
//
// It is where the browser put it. Opened behind, it is behind, and the
// conversation goes on in the tab it was in, which is still drawn. Opened
// in front or in a window, it is the tab the conversation works in.
// Closing it goes back to the tab it was opened from.
func TestALinkOpenedInATabOfItsOwnIsTheConversations(t *testing.T) {
	m := testManager(t)
	site := linkSite(t)
	key, keyName := newTabKey()
	enter := func(mods int) func(context.Context, *Tab) error {
		return func(ctx context.Context, tab *Tab) error {
			if err := tab.eval(ctx, `document.getElementById('link').focus()`, nil); err != nil {
				return err
			}
			for _, typ := range []string{"rawKeyDown", "keyUp"} {
				if err := tab.KeyEvent(ctx, KeyEvent{Type: typ, Key: "Enter", Code: "Enter", KeyCode: 13, Modifiers: mods}); err != nil {
					return err
				}
			}
			return nil
		}
	}
	// A conversation that does none of it: nothing of the others' is its.
	bystander, err := m.Tab(ctxFor(t), "bystander")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		page    string
		inFront bool
		// onward is to go on to switch to the tab opened and close it:
		// once for each place the browser puts a page, since each switch
		// waits a second for the browser to draw.
		onward bool
		do     func(context.Context, *Tab) error
	}{
		{"the wheel", "/", false, true, clickWith(linkX, linkY, "middle", 0)},
		{"Ctrl or Cmd", "/", false, false, clickWith(linkX, linkY, "left", key)},
		{"Shift and the wheel", "/", true, true, clickWith(linkX, linkY, "middle", ModShift)},
		{"Ctrl or Cmd, and Shift", "/", true, false, clickWith(linkX, linkY, "left", key|ModShift)},
		{"Shift", "/", true, true, clickWith(linkX, linkY, "left", ModShift)},
		{"Enter with Ctrl or Cmd", "/", false, false, enter(key)},
		{"Enter with Ctrl or Cmd, and Shift", "/", true, false, enter(key | ModShift)},
		{"Enter with Shift", "/", true, false, enter(ModShift)},
		{"the model's key", "/", false, false, func(ctx context.Context, tab *Tab) error {
			if err := tab.eval(ctx, `document.getElementById('link').focus()`, nil); err != nil {
				return err
			}
			return tab.Press(ctx, keyName+"+Enter")
		}},
		// A form sent with the key held is asked for as a link is.
		{"a form, Ctrl or Cmd", "/", false, false, clickWith(sendX, sendY, "left", key)},
		{"in a frame from another site, Ctrl or Cmd", "/framed", false, false, clickWith(linkX, linkY, "left", key)},
		{"in a frame from another site, Shift", "/framed", true, false, clickWith(linkX, linkY, "left", ModShift)},
		// Not a link: the page opens it from a script, and the browser
		// says which page did. Opened behind all the same.
		{"a script's window.open, Ctrl or Cmd", "/", false, false, clickWith(buttonX, buttonY, "left", key)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ctxFor(t)
			conv := tc.name
			first, err := m.Tab(ctx, conv)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { <-m.CloseTab(conv) })
			if err := first.Navigate(ctx, site+tc.page); err != nil {
				t.Fatal(err)
			}
			if err := tc.do(ctx, first); err != nil {
				t.Fatal(err)
			}
			tabs := tabsWhen(t, ctx, m, conv, 2)
			if tabs[0].ID != first.TargetID() {
				t.Fatalf("the tabs are %+v, want the one it had first", tabs)
			}
			opened := tabs[1]
			if u, err := url.Parse(opened.URL); err != nil || u.Path != "/second" {
				t.Errorf("the tab opened is at %q, want the link's page", opened.URL)
			}
			if opened.Active != tc.inFront {
				t.Errorf("the tab opened is the one the conversation works in: %v, want %v", opened.Active, tc.inFront)
			}
			// The tab it works in is the one the browser draws.
			cur := m.Existing(conv)
			if want := map[bool]string{true: opened.ID, false: first.TargetID()}[tc.inFront]; cur == nil || cur.TargetID() != want {
				t.Fatalf("the conversation works in %v, want %s", cur, want)
			}
			drawn(t, ctx, cur, "the tab the conversation works in")
			// Every ask was answered by its page: none is left for the
			// next page with that address.
			m.mu.Lock()
			left := len(m.asks)
			m.mu.Unlock()
			if left != 0 {
				t.Errorf("%d asks are left once the page is a tab", left)
			}
			if !tc.onward {
				return
			}
			// The tab opened can be switched to, and is then drawn.
			if err := m.ActivateTab(conv, opened.ID); err != nil {
				t.Fatal(err)
			}
			if cur = m.Existing(conv); cur == nil || cur.TargetID() != opened.ID {
				t.Fatalf("switched to the tab opened, the conversation works in %v", cur)
			}
			drawn(t, ctx, cur, "the tab opened, switched to")
			// Closed, the conversation is back in the tab it came from.
			if err := m.CloseTabID(ctx, conv, opened.ID); err != nil {
				t.Fatal(err)
			}
			if cur = m.Existing(conv); cur != first {
				t.Fatalf("the tab opened closed, the conversation works in %v, want the tab it was opened from", cur)
			}
			drawn(t, ctx, first, "the tab it was opened from, gone back to")
			if tabs := m.Tabs(ctx, conv); len(tabs) != 1 {
				t.Errorf("the conversation has %d tabs once the one opened is closed, want 1", len(tabs))
			}
		})
	}
	ctx := ctxFor(t)
	if tabs := m.Tabs(ctx, "bystander"); len(tabs) != 1 || tabs[0].ID != bystander.TargetID() {
		t.Errorf("a conversation that opened nothing has the tabs %+v", tabs)
	}
	drawn(t, ctx, bystander, "the tab of a conversation that opened nothing")
	// Nothing is left open in the browser: the bystander's tab and the
	// page the browser starts with.
	deadline := time.Now().Add(10 * time.Second)
	for pagesOpen(t, ctx, m) != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the browser has %d pages open once every conversation is closed, want 2", pagesOpen(t, ctx, m))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Two conversations that open the same link each get their own page.
func TestTheSameLinkOpenedByTwoConversationsIsATabOfEach(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := linkSite(t)
	key, _ := newTabKey()
	for _, conv := range []string{"one", "two"} {
		tab, err := m.Tab(ctx, conv)
		if err != nil {
			t.Fatal(err)
		}
		if err := tab.Navigate(ctx, site); err != nil {
			t.Fatal(err)
		}
		if err := clickWith(linkX, linkY, "left", key)(ctx, tab); err != nil {
			t.Fatal(err)
		}
		tabsWhen(t, ctx, m, conv, 2)
	}
	if tabs := m.Tabs(ctx, "one"); len(tabs) != 2 {
		t.Errorf("the first conversation has %d tabs once the second opened the link too, want 2", len(tabs))
	}
}

// A script can send a link a click of its own making, with Shift or Ctrl
// set on it. The page then asks the browser to open the link elsewhere,
// and the browser opens nothing for a click nobody made. That ask is not
// kept: kept, it was answered by the next page with the address, and
// another conversation's page was taken for this one's.
func TestAClickAScriptMadeTakesNoOtherConversationsPage(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := linkSite(t)
	key, _ := newTabKey()
	one, err := m.Tab(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := one.Navigate(ctx, site); err != nil {
		t.Fatal(err)
	}
	// After a click of the person's own with no key held, as a page that
	// waits for one would do it.
	for _, typ := range []string{"mousePressed", "mouseReleased"} {
		if err := one.Mouse(ctx, MouseEvent{Type: typ, X: 600, Y: 400, Button: "left", ClickCount: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, mods := range []string{"shiftKey: true", "ctrlKey: true, metaKey: true"} {
		if err := one.eval(ctx, `document.getElementById('link').dispatchEvent(new MouseEvent('click', {`+mods+`, bubbles: true, cancelable: true}))`, nil); err != nil {
			t.Fatal(err)
		}
	}
	// The page has said it by the time it answers the next question.
	if err := one.eval(ctx, `1`, nil); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	kept := len(m.asks)
	m.mu.Unlock()
	if kept != 0 {
		t.Errorf("%d asks were kept from a click a script made", kept)
	}
	two, err := m.Tab(ctx, "two")
	if err != nil {
		t.Fatal(err)
	}
	if err := two.Navigate(ctx, site); err != nil {
		t.Fatal(err)
	}
	if err := clickWith(linkX, linkY, "left", key)(ctx, two); err != nil {
		t.Fatal(err)
	}
	tabsWhen(t, ctx, m, "two", 2)
	if tabs := m.Tabs(ctx, "one"); len(tabs) != 1 {
		t.Errorf("the conversation whose script clicked has the tabs %+v, want the one it had", tabs)
	}
}

// A page that opens a dialog as it loads is answered though no tab is
// attached to it yet. A tab answers its page's dialogs, but this page's
// tab was still waiting its turn behind another that was attaching: the
// dialog had nobody to answer it, the page stood still, its tab could not
// be attached, and it was closed.
func TestADialogAPageOpensBeforeItIsATabIsAnswered(t *testing.T) {
	m := testManager(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/busy":
			fmt.Fprint(w, `<!doctype html><script>const end = Date.now() + 400; while (Date.now() < end) {}</script><title>Busy</title>`)
		case "/asks":
			fmt.Fprint(w, `<!doctype html><script>alert('on load'); document.title = 'Answered ' + confirm('and again')</script><p>ready</p>`)
		default:
			fmt.Fprint(w, `<!doctype html><title>Start</title>
<style>a{position:absolute;left:10px;width:200px;height:30px;display:block}</style>
<a href="/busy" target="_blank" style="top:10px">busy</a>
<a href="/asks" target="_blank" style="top:50px">asks</a>`)
		}
	}))
	t.Cleanup(srv.Close)
	ctx := ctxFor(t)
	tab, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}
	// Two links, one after the other: the second page is made while the
	// first is still being attached.
	for _, y := range []float64{25, 65} {
		for _, typ := range []string{"mousePressed", "mouseReleased"} {
			if err := tab.Mouse(ctx, MouseEvent{Type: typ, X: 40, Y: y, Button: "left", ClickCount: 1}); err != nil {
				t.Fatal(err)
			}
		}
	}
	tabs := tabsWhen(t, ctx, m, "c", 3)
	deadline := time.Now().Add(10 * time.Second)
	for tabs[2].Title != "Answered true" {
		if time.Now().After(deadline) {
			t.Fatalf("the page that opened dialogs is %+v, want both answered OK", tabs[2])
		}
		time.Sleep(50 * time.Millisecond)
		tabs = tabsWhen(t, ctx, m, "c", 3)
	}
}

// Closing a tab lets go of every handler its page had, the handlers of
// each frame in it from another site among them. A tab's own session was
// set up as the guard's is, so each such frame was attached a second time
// through it, and those sessions' handlers stayed when the tab closed.
func TestClosingATabLetsGoOfItsFramesHandlers(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := linkSite(t)
	if _, err := m.Tab(ctx, "keep"); err != nil {
		t.Fatal(err)
	}
	conn := m.proc.conn
	openAndClose := func(n int) {
		for i := 0; i < n; i++ {
			conv := fmt.Sprintf("framed %d", i)
			tab, err := m.Tab(ctx, conv)
			if err != nil {
				t.Fatal(err)
			}
			if err := tab.Navigate(ctx, site+"/framed"); err != nil {
				t.Fatal(err)
			}
			<-m.CloseTab(conv)
		}
	}
	// settled is the handler count once it has stopped changing.
	settled := func() int {
		n, since := conn.HandlerCount(), time.Now()
		for deadline := time.Now().Add(10 * time.Second); time.Since(since) < 500*time.Millisecond && time.Now().Before(deadline); {
			time.Sleep(50 * time.Millisecond)
			if now := conn.HandlerCount(); now != n {
				n, since = now, time.Now()
			}
		}
		return n
	}
	openAndClose(1)
	before := settled()
	openAndClose(8)
	if after := settled(); after > before {
		t.Fatalf("handlers went from %d to %d over 8 closed tabs with a frame from another site", before, after)
	}
}

// A key that opens nothing elsewhere lets no script's ask through. Shift
// is held for every capital letter typed, and each such key let whatever
// the page's script said in the next seconds pass for a click.
func TestAKeyThatOpensNothingLetsNoAskThrough(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := linkSite(t)
	key, _ := newTabKey()
	one, err := m.Tab(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := one.Navigate(ctx, site); err != nil {
		t.Fatal(err)
	}
	for _, keys := range []string{"shift+Tab", "ctrl+a", "shift+A"} {
		if err := one.Press(ctx, keys); err != nil {
			t.Fatal(err)
		}
	}
	if err := one.eval(ctx, `
		const ask = () => document.getElementById('link').dispatchEvent(new MouseEvent('click', {ctrlKey: true, metaKey: true, bubbles: true, cancelable: true}));
		ask();
		setInterval(ask, 10);
	`, nil); err != nil {
		t.Fatal(err)
	}
	if err := one.eval(ctx, `1`, nil); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	kept := len(m.asks)
	m.mu.Unlock()
	if kept != 0 {
		t.Errorf("%d asks were kept from a click a script made after keys that open nothing", kept)
	}
	two, err := m.Tab(ctx, "two")
	if err != nil {
		t.Fatal(err)
	}
	if err := two.Navigate(ctx, site); err != nil {
		t.Fatal(err)
	}
	if err := clickWith(linkX, linkY, "left", key)(ctx, two); err != nil {
		t.Fatal(err)
	}
	tabsWhen(t, ctx, m, "two", 2)
	if tabs := m.Tabs(ctx, "one"); len(tabs) != 1 {
		t.Errorf("the conversation whose script clicked has the tabs %+v, want the one it had", tabs)
	}
}

// Pages opened one straight after another end where the browser put
// them: the last one opened in front is the tab the conversation works
// in, and is the one drawn; one opened behind is behind. Where a page is
// when its turn comes to be added is not where the browser put it: one
// opened in front is behind the next by then, and this manager putting
// the tab it works in back in front, after a page added behind, covered
// one opened in front that was still waiting, which was then listed
// behind and left there.
func TestPagesOpenedOneAfterAnotherEndWhereTheBrowserPutThem(t *testing.T) {
	m := testManager(t)
	site := linkSite(t)
	type open struct {
		name   string
		button string
		mods   int
		front  bool
	}
	behind := open{"behind", "middle", 0, false}
	inFront := open{"in front", "middle", ModShift, true}
	window := open{"in a window", "left", ModShift, true}
	for _, seq := range [][]open{
		{behind, inFront}, {inFront, behind}, {behind, behind}, {inFront, inFront},
		{behind, window}, {window, behind}, {inFront, window}, {window, inFront},
		{behind, inFront, behind}, {inFront, behind, inFront},
	} {
		name := ""
		for i, o := range seq {
			if i > 0 {
				name += ", then "
			}
			name += o.name
		}
		t.Run(name, func(t *testing.T) {
			ctx := ctxFor(t)
			first, err := m.Tab(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { <-m.CloseTab(name) })
			if err := first.Navigate(ctx, site); err != nil {
				t.Fatal(err)
			}
			// The mouse is over the link before the first click, and the
			// clicks follow each other with nothing waited for between.
			if err := clickWith(linkX, linkY, seq[0].button, seq[0].mods)(ctx, first); err != nil {
				t.Fatal(err)
			}
			for _, o := range seq[1:] {
				for _, typ := range []string{"mousePressed", "mouseReleased"} {
					if err := first.Mouse(ctx, MouseEvent{Type: typ, X: linkX, Y: linkY, Button: o.button, ClickCount: 1, Modifiers: o.mods}); err != nil {
						t.Fatal(err)
					}
				}
			}
			tabs := tabsWhen(t, ctx, m, name, 1+len(seq))
			want := 0
			for i, o := range seq {
				if o.front {
					want = i + 1
				}
			}
			// The last of them is added a moment after it is listed.
			deadline := time.Now().Add(10 * time.Second)
			for !tabs[want].Active {
				if time.Now().After(deadline) {
					t.Fatalf("the tabs are %+v, want tab %d the one the conversation works in", tabs, want)
				}
				time.Sleep(50 * time.Millisecond)
				tabs = m.Tabs(ctx, name)
			}
			drained(t, m, name)
			if tabs = m.Tabs(ctx, name); len(tabs) != 1+len(seq) || !tabs[want].Active {
				t.Fatalf("once every page is added the tabs are %+v, want tab %d the one the conversation works in", tabs, want)
			}
			drawn(t, ctx, m.Existing(name), "the tab the conversation works in")
		})
	}
}

// A page says where the browser put it as it starts, before its turn to
// be added comes: by then it may be somewhere else.
func TestAPageSaysWhereItIsAsItStarts(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := linkSite(t)
	for _, tc := range []struct {
		name, want string
		mods       int
	}{{"behind", "hidden tabbed", 0}, {"in front", "visible tabbed", ModShift}} {
		t.Run(tc.name, func(t *testing.T) {
			first, err := m.Tab(ctx, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { <-m.CloseTab(tc.name) })
			if err := first.Navigate(ctx, site); err != nil {
				t.Fatal(err)
			}
			// The conversation's turn is held: the page waits to be added.
			held, err := m.queued(ctx, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if err := clickWith(linkX, linkY, "middle", tc.mods)(ctx, first); err != nil {
				held()
				t.Fatal(err)
			}
			said, deadline := "", time.Now().Add(10*time.Second)
			for said == "" && time.Now().Before(deadline) {
				m.mu.Lock()
				for _, s := range m.born {
					said = s
				}
				m.mu.Unlock()
				time.Sleep(20 * time.Millisecond)
			}
			held()
			if said != tc.want {
				t.Fatalf("the page said %q as it started, want %q", said, tc.want)
			}
			tabsWhen(t, ctx, m, tc.name, 2)
			drained(t, m, tc.name)
			m.mu.Lock()
			left := len(m.born)
			m.mu.Unlock()
			if left != 0 {
				t.Errorf("%d pages' words are kept once the page is a tab", left)
			}
		})
	}
}

func askOf(url, where string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"url": url, "disposition": where, "reason": "anchorClick"})
	return b
}

// keyClicked marks conv's tab id as one a click with a key held was just
// sent to: the click an ask is taken for.
func keyClicked(t *testing.T, m *Manager, id string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	_, tab := m.listed(id)
	if tab == nil {
		t.Fatalf("no tab %s to click", id)
	}
	tab.clicked("left", ModCtrl)
}

// fronted is the pages the browser was asked to put in front, in order.
func (sb *scriptedBrowser) fronted() []string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	var out []string
	for _, c := range sb.calls {
		if c.Method == "Target.activateTarget" {
			out = append(out, fmt.Sprint(c.Params["targetId"]))
		}
	}
	return out
}

// tabs is conv's tab ids, once its work is done.
func tabsDone(t *testing.T, m *Manager, conv string) string {
	t.Helper()
	drained(t, m, conv)
	ids, _ := tabsOf(m, conv)
	return fmt.Sprint(ids)
}

// What is not kept as an ask: a link opened in the page itself, a
// download, the address this manager opens its own tabs with (which would
// have a tab it is opening taken for a page a conversation asked for),
// and anything a page says when no click or key that can open a link
// elsewhere was just sent to it. A script can have a page say it with no
// such click, and the browser then opens nothing: kept, that ask was
// answered by the next page with its address, whoever's it was.
func TestWhatIsNotAnAsk(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	m, p := managerOn(sb)
	const u = "http://site.test/second"
	n := 0
	notMade := func(what, url string) {
		t.Helper()
		n++
		m.made(p, fmt.Sprintf("P%d", n), url)
		if got := tabsDone(t, m, "c1"); got != "[A]" {
			t.Fatalf("%s: a page at %q is a tab of the conversation: %s", what, url, got)
		}
	}
	// Before any click with a key held.
	m.ask(p, "A", askOf(u, "newTab"))
	notMade("an ask with no click before it", u)
	// After a click or a key that opens a link in its own page, or
	// nothing: a plain click, the right button, a letter typed with Shift
	// held, Tab with Shift, a shortcut.
	m.mu.Lock()
	a := m.convs["c1"].tabs[0]
	a.clicked("left", 0)
	a.clicked("right", ModCtrl)
	a.pressed("A", ModShift)
	a.pressed("Tab", ModShift)
	a.pressed("a", ModCtrl)
	a.pressed("Enter", 0)
	m.mu.Unlock()
	m.ask(p, "A", askOf(u, "newTab"))
	notMade("an ask after a click or a key that opens nothing elsewhere", u)
	// After each that can: the wheel, a click with a key held, Enter or
	// Space with one held.
	for i, opens := range []func(){
		func() { a.clicked("middle", 0) },
		func() { a.clicked("left", ModShift) },
		func() { a.clicked("", ModMeta) },
		func() { a.pressed("Enter", ModCtrl) },
		func() { a.pressed(" ", ModShift) },
	} {
		a.opens.Store(0)
		opens()
		m.ask(p, "A", askOf(u, "newTab"))
		m.mu.Lock()
		kept := len(m.asks)
		m.asks = nil
		m.mu.Unlock()
		if kept != 1 {
			t.Fatalf("after click or key %d that can open a link elsewhere, %d asks are kept, want 1", i, kept)
		}
	}
	a.opens.Store(0)
	// Long after one.
	keyClicked(t, m, "A")
	m.mu.Lock()
	m.convs["c1"].tabs[0].opens.Store(time.Now().Add(-askAfter - time.Second).UnixNano())
	m.mu.Unlock()
	m.ask(p, "A", askOf(u, "newTab"))
	notMade("an ask long after the click", u)

	keyClicked(t, m, "A")
	for _, ev := range []json.RawMessage{
		askOf(u, "currentTab"),
		askOf(u, "download"),
		askOf(blankPage, "newTab"),
		askOf("", "newTab"),
		json.RawMessage(`not JSON`),
	} {
		m.ask(p, "A", ev)
	}
	for _, url := range []string{u, blankPage, ""} {
		notMade("what is not an ask", url)
	}
	// From a page that is no conversation's tab.
	m.ask(p, "Z", askOf(u, "newTab"))
	notMade("an ask from a page that is no tab", u)
	// On a browser that has been replaced, and by a page of one.
	replaced := &process{conn: sb.conn}
	m.ask(replaced, "A", askOf(u, "newTab"))
	notMade("an ask on another browser", u)
	m.ask(p, "A", askOf(u, "newTab"))
	m.made(replaced, "Q", u)
	if got := tabsDone(t, m, "c1"); got != "[A]" {
		t.Fatalf("a page of another browser took the ask: %s", got)
	}
	// From before every tab went with the browser.
	m.mu.Lock()
	m.dropAll()
	m.mu.Unlock()
	m.made(p, "R", u)
	if got := tabsDone(t, m, "c1"); got != "[]" {
		t.Errorf("an ask was kept when every tab went: %s", got)
	}
}

// An ask is answered once, by the page with its address; the newest ask
// for an address is answered first, so one left behind does not come
// before the ask a click has just made; an ask waits only so long; and
// only so many are kept.
func TestAsksAreAnsweredNewestFirstAndNotForEver(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	m, p := managerOn(sb)
	m.mu.Lock()
	m.convs["c2"] = &convTabs{tabs: []*Tab{{conn: sb.conn, targetID: "C", session: "sC"}}, changed: make(chan struct{})}
	m.mu.Unlock()
	const u = "http://site.test/second"
	keyClicked(t, m, "A")
	keyClicked(t, m, "C")

	m.ask(p, "A", askOf(u, "newTab"))
	m.ask(p, "C", askOf(u, "newWindow"))
	m.ask(p, "A", askOf(u+"?other", "newTab"))
	m.made(p, "P1", u)
	if one, two := tabsDone(t, m, "c1"), tabsDone(t, m, "c2"); one != "[A]" || two != "[C P1]" {
		t.Fatalf("the first page made is in %s and %s, want in the conversation that asked last", one, two)
	}
	m.made(p, "P2", u)
	m.made(p, "P3", u)
	if one, two := tabsDone(t, m, "c1"), tabsDone(t, m, "c2"); one != "[A P2]" || two != "[C P1]" {
		t.Fatalf("after two more pages the tabs are %s and %s: each ask is answered once", one, two)
	}
	m.made(p, "P4", u+"?other")
	if one := tabsDone(t, m, "c1"); one != "[A P2 P4]" {
		t.Fatalf("the page at the other address: %s", one)
	}

	m.ask(p, "A", askOf(u, "newTab"))
	m.mu.Lock()
	m.asks[0].at = time.Now().Add(-askFor - time.Second)
	m.mu.Unlock()
	m.made(p, "P5", u)
	if one := tabsDone(t, m, "c1"); one != "[A P2 P4]" {
		t.Fatalf("an ask older than %s was answered: %s", askFor, one)
	}

	for i := 0; i < askMax+5; i++ {
		m.ask(p, "A", askOf(fmt.Sprintf("%s?%d", u, i), "newTab"))
	}
	m.mu.Lock()
	kept := len(m.asks)
	m.mu.Unlock()
	if kept != askMax {
		t.Fatalf("%d asks are kept, want %d", kept, askMax)
	}
	m.made(p, "P6", u+"?0")
	m.made(p, "P7", fmt.Sprintf("%s?%d", u, askMax+4))
	if one := tabsDone(t, m, "c1"); one != "[A P2 P4 P7]" {
		t.Errorf("past the limit the oldest ask goes and the newest stays: %s", one)
	}
}

// A page a closed conversation's page asked for is closed, as one its
// page opened is: nobody's, and not left open where nothing reaches it.
func TestAPageAskedForByAClosedConversationIsClosed(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	m, p := managerOn(sb)
	const u = "http://site.test/second"
	keyClicked(t, m, "A")
	m.ask(p, "A", askOf(u, "newTab"))
	<-m.CloseTab("c1")
	m.made(p, "B", u)
	sb.waitClosed(t, "A", "B")
	if got := tabsDone(t, m, "c1"); got != "[]" {
		t.Errorf("the closed conversation has the tabs %s", got)
	}
}

// What a tab asked for is the conversation's though the tab has closed
// since: the page is a tab of the conversation, not one left open in the
// browser with nobody's name on it, nor closed.
func TestAPageAskedForByATabThatHasClosedIsTheConversations(t *testing.T) {
	const u = "http://site.test/second"
	t.Run("one of its tabs", func(t *testing.T) {
		sb, _ := newScriptedBrowser(t, nil, nil)
		m, p := managerOn(sb)
		m.mu.Lock()
		m.convs["c1"].tabs = append(m.convs["c1"].tabs, &Tab{conn: sb.conn, targetID: "A2", session: "sA2"})
		m.mu.Unlock()
		keyClicked(t, m, "A")
		m.ask(p, "A", askOf(u, "newTab"))
		m.destroyed(p, "A")
		drained(t, m, "c1")
		m.made(p, "B", u)
		if got := tabsDone(t, m, "c1"); got != "[A2 B]" {
			t.Fatalf("the tabs are %s, want [A2 B]", got)
		}
		if closed := sb.closed(); contains(closed, "B") {
			t.Errorf("the page asked for was closed: %v", closed)
		}
	})
	t.Run("its only tab", func(t *testing.T) {
		sb, _ := newScriptedBrowser(t, nil, nil)
		m, p := managerOn(sb)
		keyClicked(t, m, "A")
		m.ask(p, "A", askOf(u, "newTab"))
		m.destroyed(p, "A")
		drained(t, m, "c1")
		m.made(p, "B", u)
		if got := tabsDone(t, m, "c1"); got != "[B]" {
			t.Fatalf("the tabs are %s, want [B]", got)
		}
	})
}

// A page the browser opened behind is added behind: the conversation
// goes on in the tab it was in, and nobody watching it is told of a
// change. The tab it works in is put in front either way, whatever has
// happened in its window since the page said where it was.
func TestAPageOpenedBehindIsATabBehind(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	sb.answers("Runtime.evaluate", map[string]any{"result": map[string]any{"value": "hidden"}})
	m, p := managerOn(sb)
	m.mu.Lock()
	watching := m.convs["c1"].changed
	m.mu.Unlock()
	m.created(p, "A", "B")
	drained(t, m, "c1")
	if ids, active := tabsOf(m, "c1"); fmt.Sprint(ids) != "[A B]" || active != "A" {
		t.Fatalf("the tabs are %v and the conversation works in %s, want [A B] and A", ids, active)
	}
	select {
	case <-watching:
		t.Error("whoever watches the conversation was told of a change, and the tab it works in is the same")
	default:
	}
	if got := fmt.Sprint(sb.fronted()); got != "[A]" {
		t.Errorf("put in front: %s, want the tab it works in, [A]", got)
	}
	// One in front is the tab it works in, as before.
	sb.answers("Runtime.evaluate", map[string]any{"result": map[string]any{"value": "visible"}})
	m.created(p, "A", "C")
	drained(t, m, "c1")
	if ids, active := tabsOf(m, "c1"); fmt.Sprint(ids) != "[A B C]" || active != "C" {
		t.Fatalf("the tabs are %v and the conversation works in %s, want [A B C] and C", ids, active)
	}
	if got := fmt.Sprint(sb.fronted()); got != "[A C]" {
		t.Errorf("put in front: %s, want [A C]", got)
	}
}

// A page that does not say where it is becomes the tab the conversation
// works in, as every page a link opened did, and is put in front: taken
// to be in front and left where it was, one opened behind was a tab the
// browser did not draw.
func TestAPageThatDoesNotSayWhereItIsIsPutInFront(t *testing.T) {
	sb, _ := newScriptedBrowser(t, map[string]string{"Runtime.evaluate": "Execution context was destroyed"}, nil)
	m, p := managerOn(sb)
	m.created(p, "A", "B")
	drained(t, m, "c1")
	if ids, active := tabsOf(m, "c1"); fmt.Sprint(ids) != "[A B]" || active != "B" {
		t.Fatalf("the tabs are %v and the conversation works in %s, want [A B] and B", ids, active)
	}
	if got := fmt.Sprint(sb.fronted()); got != "[B]" {
		t.Errorf("put in front: %s, want [B]", got)
	}
}

// And whoever watches the conversation is told of that tab once it has
// been brought forward and has painted, not when it is listed: a stream
// started on a tab the browser is not drawing stays on one picture.
func TestAPageThatDoesNotSayWhereItIsIsToldOfOncePainted(t *testing.T) {
	// The page answers nothing it is asked: where it is (three tries),
	// then whether it has painted, which is held.
	sb, let := newScriptedBrowser(t, map[string]string{"Runtime.evaluate": "Execution context was destroyed"}, map[string][]int{"Runtime.evaluate": {4}})
	m, p := managerOn(sb)
	m.mu.Lock()
	watching := m.convs["c1"].changed
	m.mu.Unlock()
	m.created(p, "A", "B")
	sb.waitHeld(t, "Runtime.evaluate#4")
	if got := fmt.Sprint(sb.fronted()); got != "[B]" {
		t.Fatalf("put in front before it is waited on to paint: %s, want [B]", got)
	}
	select {
	case <-watching:
		t.Fatal("whoever watches the conversation was told before the tab had painted")
	default:
	}
	let["Runtime.evaluate"][4]()
	select {
	case <-watching:
	case <-time.After(2 * time.Second):
		t.Error("whoever watches the conversation was not told once the tab had painted")
	}
}

// With more of its pages still to be added, nothing of the conversation's
// is brought forward: that would cover one opened in front that waits.
// The last of them settles what is in front.
func TestNothingIsBroughtForwardWhileMorePagesWait(t *testing.T) {
	sb, let := newScriptedBrowser(t, nil, map[string][]int{"Target.attachToTarget": {1}})
	sb.answers("Runtime.evaluate", map[string]any{"result": map[string]any{"value": "hidden"}})
	m, p := managerOn(sb)
	m.created(p, "A", "B")
	sb.waitHeld(t, "Target.attachToTarget#1")
	m.created(p, "A", "C")
	queuedBehind(t, m, "c1", 2)
	// The second page said, as it started, that it was in front.
	m.bornAt(p, "C", "visible")
	let["Target.attachToTarget"][1]()
	drained(t, m, "c1")
	if ids, active := tabsOf(m, "c1"); fmt.Sprint(ids) != "[A B C]" || active != "C" {
		t.Fatalf("the tabs are %v and the conversation works in %s, want [A B C] and C", ids, active)
	}
	if got := fmt.Sprint(sb.fronted()); got != "[C]" {
		t.Errorf("put in front: %s, want only the last, [C]", got)
	}
}

// A page that closes before it is attached was never a tab, but it may
// have been in front, and the browser then brings another tab of its
// window forward: the tab the conversation works in is put back in
// front.
func TestAPageThatClosesUnattachedLeavesTheActiveTabInFront(t *testing.T) {
	sb, let := newScriptedBrowser(t, nil, map[string][]int{"Target.attachToTarget": {1}})
	m, p := managerOn(sb)
	m.created(p, "A", "B")
	sb.waitHeld(t, "Target.attachToTarget#1")
	m.destroyed(p, "B")
	let["Target.attachToTarget"][1]()
	drained(t, m, "c1")
	if ids, active := tabsOf(m, "c1"); fmt.Sprint(ids) != "[A]" || active != "A" {
		t.Fatalf("the tabs are %v and the conversation works in %s, want [A] and A", ids, active)
	}
	if got := fmt.Sprint(sb.fronted()); got != "[A]" {
		t.Errorf("put in front: %s, want [A]", got)
	}
}

// And so does a page that could not be attached, which is closed.
func TestAPageThatCouldNotBeAttachedLeavesTheActiveTabInFront(t *testing.T) {
	sb, _ := newScriptedBrowser(t, map[string]string{"Runtime.enable": "the page does not answer"}, nil)
	m, p := managerOn(sb)
	m.created(p, "A", "B")
	drained(t, m, "c1")
	sb.waitClosed(t, "B")
	if ids, active := tabsOf(m, "c1"); fmt.Sprint(ids) != "[A]" || active != "A" {
		t.Fatalf("the tabs are %v and the conversation works in %s, want [A] and A", ids, active)
	}
	if got := fmt.Sprint(sb.fronted()); got != "[A]" {
		t.Errorf("put in front: %s, want [A]", got)
	}
}

// A page opened behind that is the only tab the conversation has left is
// the tab it works in, and is put in front: behind, nothing draws it.
func TestAPageOpenedBehindThatIsTheOnlyTabIsPutInFront(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	sb.answers("Runtime.evaluate", map[string]any{"result": map[string]any{"value": "hidden"}})
	m, p := managerOn(sb)
	ctx := ctxFor(t)
	// The tab it is opened from is closed first, and the page is made
	// before that close has its turn.
	held, err := m.queued(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- m.CloseTabID(ctx, "c1", "A") }()
	queuedBehind(t, m, "c1", 2)
	m.created(p, "A", "B")
	queuedBehind(t, m, "c1", 3)
	held()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	drained(t, m, "c1")
	if ids, active := tabsOf(m, "c1"); fmt.Sprint(ids) != "[B]" || active != "B" {
		t.Fatalf("the tabs are %v and the conversation works in %q, want [B] and B", ids, active)
	}
	if got := fmt.Sprint(sb.fronted()); got != "[B]" {
		t.Errorf("put in front: %s, want [B]", got)
	}
}

// A page is set up through one session, the first attached to it: the
// tab's own session, attached after, is not set up again. A worker is,
// through each of its sessions.
func TestAPageIsSetUpThroughOneSession(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	m, p := managerOn(sb)
	attach := func(session, target, kind string) {
		ev, _ := json.Marshal(map[string]any{"sessionId": session, "targetInfo": map[string]string{"targetId": target, "type": kind}})
		m.attached(p, ev, "")
	}
	setUp := func(want int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for sb.count("Fetch.enable") != want {
			if time.Now().After(deadline) {
				t.Fatalf("%d targets were set up, want %d", sb.count("Fetch.enable"), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
		// And no more than that, a moment later.
		time.Sleep(50 * time.Millisecond)
		if got := sb.count("Fetch.enable"); got != want {
			t.Fatalf("%d targets were set up, want %d", got, want)
		}
	}
	attach("guard", "P", "page")
	setUp(1)
	attach("tab", "P", "page")
	setUp(1)
	attach("guard 2", "Q", "page")
	setUp(2)
	attach("w1", "W", "service_worker")
	attach("w2", "W", "service_worker")
	setUp(4)
	// With its session gone, a page is set up through the next.
	gone, _ := json.Marshal(map[string]string{"sessionId": "guard"})
	forgetDetached(p, gone)
	attach("tab again", "P", "page")
	setUp(5)
}
