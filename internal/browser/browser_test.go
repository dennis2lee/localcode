package browser

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testManager runs a real browser on a fresh profile directory, never the
// person's: the profile name file is written first, so nothing is imported.
func testManager(t *testing.T) *Manager {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := Find(""); err != nil {
		t.Skip("no Chrome or Edge here")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, profileNameFile), []byte("Default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Options{DataDir: dir, ChromePath: os.Getenv("LC_PROBE_BROWSER")})
	t.Cleanup(m.Close)
	return m
}

const formPage = `<!doctype html><title>Form</title>
<input id=name placeholder="Your name">
<input id=pw type=password placeholder="Password">
<button id=go onclick="document.getElementById('out').textContent = 'hello ' + document.getElementById('name').value">Greet</button>
<p id=out>nothing yet</p>
<a href="/second" target="_blank">open second</a>
<form onsubmit="document.title='submitted'; return false"><input id=q name=q placeholder="Search"></form>`

func testSite(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/second":
			fmt.Fprint(w, `<!doctype html><title>Second</title><p>the second page</p>`)
		default:
			fmt.Fprint(w, formPage)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func ctxFor(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func refNamed(t *testing.T, s Snapshot, name string) int {
	t.Helper()
	for _, e := range s.Elements {
		if e.Name == name {
			return e.Ref
		}
	}
	t.Fatalf("no element named %q in %+v", name, s.Elements)
	return 0
}

// The model's whole loop on one page: read it, click by number, type,
// press a key, and see the result.
func TestTheModelWorksAPage(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := testSite(t)
	tab, err := m.Tab(ctx, "conv1")
	if err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(ctx, site); err != nil {
		t.Fatal(err)
	}
	snap, err := tab.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Title != "Form" || !strings.Contains(snap.Text, "nothing yet") {
		t.Fatalf("snapshot = %+v", snap)
	}
	if err := tab.ClickRef(ctx, refNamed(t, snap, "Your name")); err != nil {
		t.Fatal(err)
	}
	if err := tab.Type(ctx, "Ada"); err != nil {
		t.Fatal(err)
	}
	if err := tab.ClickRef(ctx, refNamed(t, snap, "Greet")); err != nil {
		t.Fatal(err)
	}
	snap, _ = tab.Read(ctx)
	if !strings.Contains(snap.Text, "hello Ada") {
		t.Errorf("after typing and clicking, the page says %q", snap.Text)
	}

	// Select all replaces what is in the field, by Ctrl and by Cmd, on
	// every platform: the key carries the editing command, which is what
	// the browser on macOS acts on.
	for _, c := range []struct{ combo, name string }{{"ctrl+a", "Grace"}, {"cmd+a", "Lin"}} {
		if err := tab.ClickRef(ctx, refNamed(t, snap, "Your name")); err != nil {
			t.Fatal(err)
		}
		if err := tab.Press(ctx, c.combo); err != nil {
			t.Fatal(err)
		}
		tab.Type(ctx, c.name)
		snap, _ = tab.Read(ctx)
		for _, e := range snap.Elements {
			if e.Name == "Your name" && e.Value != c.name {
				t.Errorf("after %s and typing %q the field holds %q", c.combo, c.name, e.Value)
			}
		}
	}

	// A password is typed like anything else, and never read back.
	if err := tab.ClickRef(ctx, refNamed(t, snap, "Password")); err != nil {
		t.Fatal(err)
	}
	tab.Type(ctx, "s3cret")
	snap, _ = tab.Read(ctx)
	for _, e := range snap.Elements {
		if e.Kind == "input[password]" && e.Value != "(filled)" {
			t.Errorf("password field reads back as %q", e.Value)
		}
	}

	// Enter submits a form.
	tab.ClickRef(ctx, refNamed(t, snap, "Search"))
	tab.Type(ctx, "x")
	if err := tab.Press(ctx, "Enter"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if loc, _ := tab.Location(ctx); loc.Title != "submitted" {
		t.Errorf("after Enter the title is %q", loc.Title)
	}

	png, err := tab.Screenshot(ctx)
	if err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Errorf("screenshot: %d bytes, %v", len(png), err)
	}
}

// A link that opens a new tab moves the conversation to it, and the old
// one comes back when the new one closes.
func TestAConversationFollowsANewTab(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := testSite(t)
	tab, err := m.Tab(ctx, "conv1")
	if err != nil {
		t.Fatal(err)
	}
	tab.Navigate(ctx, site)
	snap, _ := tab.Read(ctx)
	if err := tab.ClickRef(ctx, refNamed(t, snap, "open second")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var cur *Tab
	for time.Now().Before(deadline) {
		if cur = m.Existing("conv1"); cur != tab {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cur == tab || cur == nil {
		t.Fatal("the conversation did not move to the tab the link opened")
	}
	cur.waitLoaded(ctx, 10*time.Second)
	if loc, _ := cur.Location(ctx); loc.Title != "Second" {
		t.Errorf("new tab is at %+v", loc)
	}
	// Closed in the browser, as the page closing itself would be.
	closeTarget(cur.conn, cur.targetID)
	for time.Now().Before(deadline) {
		if m.Existing("conv1") == tab {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("closing the new tab did not bring the conversation back to its first")
}

// Two conversations have two tabs, and the panel's stream shows frames.
func TestTabsAreSeparateAndStream(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := testSite(t)
	a, err := m.Tab(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Tab(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two conversations share a tab")
	}
	a.Navigate(ctx, site)
	b.Navigate(ctx, site+"/second")
	if la, _ := a.Location(ctx); la.Title != "Form" {
		t.Errorf("a is at %+v", la)
	}

	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	got := make(chan []byte, 1)
	go m.Watch(wctx, "a", func(f []byte) {
		select {
		case got <- f:
		default:
		}
	})
	a.Navigate(ctx, site) // a repaint, so a frame comes even if the first was missed
	select {
	case f := <-got:
		if !bytes.HasPrefix(f, []byte{0xFF, 0xD8}) {
			t.Errorf("frame is not a JPEG: % x", f[:4])
		}
	case <-wctx.Done():
		t.Fatal("no frame within 10s")
	}
}

func TestParseKeys(t *testing.T) {
	for combo, want := range map[string]struct {
		key  string
		mods int
		text string
	}{
		"Enter":     {"Enter", 0, "\r"},
		"ctrl+a":    {"a", ModCtrl, ""},
		"shift+Tab": {"Tab", ModShift, ""},
		"cmd+l":     {"l", ModMeta, ""},
		"x":         {"x", 0, "x"},
		"F5":        {"F5", 0, ""},
		// The plus key is the key after the last "+".
		"+":            {"+", 0, "+"},
		"ctrl++":       {"+", ModCtrl, ""},
		"ctrl+shift++": {"+", ModCtrl | ModShift, ""},
		"ctrl+plus":    {"+", ModCtrl, ""},
		"plus":         {"+", 0, "+"},
		"cmd+minus":    {"-", ModMeta, ""},
		// With Ctrl, Cmd or Alt a key types nothing; with Shift it does.
		"ctrl+Enter":  {"Enter", ModCtrl, ""},
		"cmd+Enter":   {"Enter", ModMeta, ""},
		"alt+space":   {" ", ModAlt, ""},
		"shift+Enter": {"Enter", ModShift, "\r"},
	} {
		def, mods, err := parseKeys(combo)
		if err != nil || def.key != want.key || mods != want.mods || def.text != want.text {
			t.Errorf("parseKeys(%q) = %+v, %d, %v", combo, def, mods, err)
		}
	}
	for _, bad := range []string{"hyper+a", "nosuchkey", "ctrl+", "ctrl+++", "+a", "++", ""} {
		if _, _, err := parseKeys(bad); err == nil {
			t.Errorf("parseKeys(%q) accepted", bad)
		}
	}
}

// A key that is neither a letter nor a digit carries where it is on the
// keyboard, as a letter does: a page that takes Ctrl+/ or Ctrl+plus as a
// shortcut reads the key code, and a key with none was not one it knew.
func TestAPunctuationKeyCarriesItsKeyCode(t *testing.T) {
	for combo, want := range map[string]keyPlace{
		"ctrl+=": {"Equal", 187}, "ctrl++": {"Equal", 187}, "ctrl+plus": {"Equal", 187},
		"ctrl+-": {"Minus", 189}, "ctrl+minus": {"Minus", 189}, "ctrl+/": {"Slash", 191},
		"cmd+.": {"Period", 190}, "ctrl+[": {"BracketLeft", 219}, "?": {"Slash", 191},
		"ctrl+shift+!": {"Digit1", 49}, "ctrl+1": {"Digit1", 49}, "ctrl+a": {"KeyA", 65},
	} {
		def, _, err := parseKeys(combo)
		if err != nil || def.code != want.code || def.keyCode != want.keyCode {
			t.Errorf("parseKeys(%q) is the key %q, %d (%v), want %q, %d", combo, def.code, def.keyCode, err, want.code, want.keyCode)
		}
	}
}

// A browser a dead daemon left running on the profile is closed by the
// next start, which would otherwise hand itself over to it and exit.
func TestAStartClosesTheBrowserAnEarlierRunLeft(t *testing.T) {
	first := testManager(t)
	ctx := ctxFor(t)
	if _, err := first.Tab(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	// The daemon that owned it is gone without closing it: nothing calls
	// first.Close, and its browser goes on running.
	left := first.proc
	t.Cleanup(left.kill) // whatever the outcome, no browser outlives the test
	first.mu.Lock()
	first.proc = nil
	first.mu.Unlock()

	second := NewManager(Options{DataDir: first.opts.DataDir})
	defer second.Close()
	if _, err := second.Tab(ctx, "c"); err != nil {
		t.Fatalf("the second start failed: %v", err)
	}
	select {
	case <-left.exited:
	case <-time.After(10 * time.Second):
		t.Error("the browser the earlier run left is still running")
	}
}

// A shortcut the model presses carries its editing command, Ctrl or Cmd
// alike, since the browser on macOS does not act on Cmd+A from the key
// alone; anything with Alt held, or no shortcut modifier, carries none.
func TestAShortcutCarriesItsEditingCommand(t *testing.T) {
	for _, c := range []struct {
		combo string
		want  string
	}{
		{"ctrl+a", "selectAll"}, {"cmd+a", "selectAll"}, {"ctrl+c", "copy"},
		{"cmd+x", "cut"}, {"ctrl+z", "undo"}, {"ctrl+shift+z", "redo"}, {"ctrl+y", "redo"},
		{"a", ""}, {"shift+a", ""}, {"ctrl+alt+a", ""}, {"ctrl+b", ""},
		// With Shift held as well, only Z is an editing shortcut.
		{"ctrl+shift+a", ""}, {"ctrl+shift+c", ""}, {"cmd+shift+x", ""}, {"ctrl+shift+y", ""}, {"cmd+shift+z", "redo"},
	} {
		def, mods, err := parseKeys(c.combo)
		if err != nil {
			t.Fatalf("parseKeys(%q): %v", c.combo, err)
		}
		got := commandsFor(def.key, mods)
		if (c.want == "" && len(got) != 0) || (c.want != "" && (len(got) != 1 || got[0] != c.want)) {
			t.Errorf("%s carries %v, want %q", c.combo, got, c.want)
		}
	}
}

// The panel's key events carry only editing commands, so a request that
// names some other command of the browser's is refused before it is sent.
func TestAKeyEventCarriesOnlyEditingCommands(t *testing.T) {
	tab := &Tab{}
	err := tab.KeyEvent(context.Background(), KeyEvent{Type: "rawKeyDown", Key: "a", Commands: []string{"insertHTML"}})
	if err == nil || !strings.Contains(err.Error(), "insertHTML") {
		t.Errorf("a key event with an unknown command: %v", err)
	}
}
