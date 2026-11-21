package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
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
			fmt.Fprintf(w, `<!doctype html><title>Framed</title><iframe src="http://localhost:%s/" style="position:absolute;left:0;top:0;width:600px;height:400px;border:0"></iframe>`, port)
		default:
			fmt.Fprint(w, `<!doctype html><title>Links</title>
<style>a,button{position:absolute;left:10px;width:200px;height:30px;display:block}</style>
<a id=link href="/second" style="top:10px">second</a>
<button onclick="window.open('/second')" style="top:60px">open</button>
<form action="/second" method="post"><button style="top:110px">send</button></form>`)
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

// clickWith clicks at x, y with button, holding mods.
func clickWith(x, y float64, button string, mods int) func(context.Context, *Tab) error {
	return func(ctx context.Context, tab *Tab) error {
		if err := tab.Mouse(ctx, MouseEvent{Type: "mouseMoved", X: x, Y: y}); err != nil {
			return err
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
			t.Fatalf("the conversation has %d tabs, want %d: %+v", len(tabs), n, tabs)
		}
		time.Sleep(50 * time.Millisecond)
	}
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

func askOf(url, where string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"url": url, "disposition": where, "reason": "anchorClick"})
	return b
}

// One click is one ask, though every session of the page it is in is told
// of it: the guard's and the tab's own. Counted twice, one was left for
// the next page with that address, whoever's it was.
func TestAClickToldOnTwoSessionsIsOneAsk(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	m, p := managerOn(sb)
	const u = "http://site.test/second"
	for _, sess := range []string{"guard", "tab"} {
		m.ask(p, "A", "A", sess, askOf(u, "newTab"))
	}
	if from := m.asked(p, u); from != "A" {
		t.Fatalf("the page made was asked for by %q, want A", from)
	}
	if from := m.asked(p, u); from != "" {
		t.Fatalf("one click left a second ask, from %q", from)
	}
	// A second click is a second ask.
	for _, sess := range []string{"guard", "tab"} {
		m.ask(p, "A", "A", sess, askOf(u, "newWindow"))
	}
	if from := m.asked(p, u); from != "A" {
		t.Fatalf("the second click's page was asked for by %q, want A", from)
	}
	// A frame of the page from another site is heard on a session of its
	// own, and asks for the page it is in.
	for _, sess := range []string{"frame by guard", "frame by tab"} {
		m.ask(p, "A", "F", sess, askOf(u, "newTab"))
	}
	if from, again := m.asked(p, u), m.asked(p, u); from != "A" || again != "" {
		t.Fatalf("a frame's click was asked for by %q and %q, want A once", from, again)
	}
	// With the session it was heard on gone, another is heard.
	m.forgetDetached(p, json.RawMessage(`{"sessionId":"guard"}`))
	m.ask(p, "A", "A", "tab", askOf(u, "newTab"))
	if from := m.asked(p, u); from != "A" {
		t.Fatalf("with the first session gone, the page made was asked for by %q, want A", from)
	}
}

// What is not kept as an ask: a link opened in the page itself, a
// download, and the address this manager opens its own tabs with, which
// would have a tab it is opening taken for a page a conversation asked
// for.
func TestWhatIsNotAnAsk(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	m, p := managerOn(sb)
	for _, ev := range []json.RawMessage{
		askOf("http://site.test/second", "currentTab"),
		askOf("http://site.test/second", "download"),
		askOf(blankPage, "newTab"),
		askOf("", "newTab"),
		json.RawMessage(`not JSON`),
	} {
		m.ask(p, "A", "A", "s", ev)
	}
	for _, u := range []string{"http://site.test/second", blankPage, ""} {
		if from := m.asked(p, u); from != "" {
			t.Errorf("a page at %q was asked for by %q, want by nobody", u, from)
		}
	}
	// What is kept, on the same session, is taken by its page.
	const other = "http://site.test/other"
	m.ask(p, "A", "A", "s", askOf(other, "newTab"))
	// Not by a page of a browser that has been replaced.
	replaced := &process{conn: sb.conn}
	if from := m.asked(replaced, other); from != "" {
		t.Errorf("a page of another browser took the ask of %q", from)
	}
	if from := m.asked(p, other); from != "A" {
		t.Fatalf("the page made was asked for by %q, want A", from)
	}
	// And no ask is kept from a browser that has been replaced, or from
	// before every tab went with the browser.
	m.ask(replaced, "A", "A", "s", askOf(other, "newTab"))
	if from := m.asked(p, other); from != "" {
		t.Errorf("an ask on another browser was kept, from %q", from)
	}
	m.ask(p, "A", "A", "s", askOf(other, "newTab"))
	m.mu.Lock()
	m.dropAll()
	m.mu.Unlock()
	if from := m.asked(p, other); from != "" {
		t.Errorf("an ask was kept when every tab went, from %q", from)
	}
}

// The oldest ask for an address is answered first, an ask waits only so
// long for its page, and only so many are kept.
func TestAsksAreAnsweredOldestFirstAndNotForEver(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	m, p := managerOn(sb)
	const u = "http://site.test/second"
	m.ask(p, "A", "A", "sA", askOf(u, "newTab"))
	m.ask(p, "C", "C", "sC", askOf(u, "newTab"))
	m.ask(p, "A", "A", "sA", askOf(u+"?other", "newTab"))
	if first, second := m.asked(p, u), m.asked(p, u); first != "A" || second != "C" {
		t.Fatalf("the pages were asked for by %q then %q, want A then C", first, second)
	}
	if from := m.asked(p, u+"?other"); from != "A" {
		t.Fatalf("the page at the other address was asked for by %q, want A", from)
	}

	m.ask(p, "A", "A", "sA", askOf(u, "newTab"))
	m.mu.Lock()
	m.asks[0].at = time.Now().Add(-askFor - time.Second)
	m.mu.Unlock()
	if from := m.asked(p, u); from != "" {
		t.Fatalf("an ask older than %s was answered, from %q", askFor, from)
	}

	for i := 0; i < askMax+5; i++ {
		m.ask(p, "A", "A", "sA", askOf(fmt.Sprintf("%s?%d", u, i), "newTab"))
	}
	m.mu.Lock()
	kept := len(m.asks)
	m.mu.Unlock()
	if kept != askMax {
		t.Fatalf("%d asks are kept, want %d", kept, askMax)
	}
	if from := m.asked(p, u+"?0"); from != "" {
		t.Errorf("the oldest ask was kept past the limit")
	}
	if from := m.asked(p, fmt.Sprintf("%s?%d", u, askMax+4)); from != "A" {
		t.Errorf("the newest ask was asked for by %q, want A", from)
	}
}

// A page a closed conversation's page asked for is closed, as one its
// page opened is: nobody's, and not left open where nothing reaches it.
func TestAPageAskedForByAClosedConversationIsClosed(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	m, p := managerOn(sb)
	const u = "http://site.test/second"
	m.ask(p, "A", "A", "sA", askOf(u, "newTab"))
	<-m.CloseTab("c1")
	from := m.asked(p, u)
	if from != "A" {
		t.Fatalf("the page made was asked for by %q, want A", from)
	}
	m.created(p, from, "B")
	sb.waitClosed(t, "A", "B")
	if ids, _ := tabsOf(m, "c1"); len(ids) != 0 {
		t.Errorf("the closed conversation has the tabs %v", ids)
	}
}

// A page the browser opened behind is added behind: the conversation
// goes on in the tab it was in, nobody watching it is told of a change,
// and nothing is put in front.
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
	if n := sb.count("Target.activateTarget"); n != 0 {
		t.Errorf("a tab was put in front %d times", n)
	}
	// One in front is the tab it works in, as before.
	sb.answers("Runtime.evaluate", map[string]any{"result": map[string]any{"value": "visible"}})
	m.created(p, "A", "C")
	drained(t, m, "c1")
	if ids, active := tabsOf(m, "c1"); fmt.Sprint(ids) != "[A B C]" || active != "C" {
		t.Fatalf("the tabs are %v and the conversation works in %s, want [A B C] and C", ids, active)
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
	var fronted []string
	sb.mu.Lock()
	for _, c := range sb.calls {
		if c.Method == "Target.activateTarget" {
			fronted = append(fronted, fmt.Sprint(c.Params["targetId"]))
		}
	}
	sb.mu.Unlock()
	if fmt.Sprint(fronted) != "[B]" {
		t.Errorf("put in front: %v, want [B]", fronted)
	}
}
