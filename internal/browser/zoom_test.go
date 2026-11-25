package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// zoomPage has a field well away from the corner, so a click that is off
// by a zoom factor lands beside it.
const zoomPage = `<!doctype html><title>Zoom</title>
<style>body{margin:0}#f{position:absolute;left:700px;top:500px;width:300px;height:40px}</style>
<input id="f">`

func zoomSite(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, zoomPage)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// pageWidth is how wide the page is laid out, in its own pixels.
func pageWidth(t *testing.T, ctx context.Context, tab *Tab) float64 {
	t.Helper()
	var w float64
	if err := tab.eval(ctx, `innerWidth`, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

// A profile copied from a browser the person has zoomed, by default and
// for this site, still shows every page at 100%: laid out as wide as its
// picture. A click at a field's place in the picture, which is how the
// panel sends one, lands on the field, and what is typed after it goes
// in. At 125% the same click reached the empty page beside the field.
func TestAZoomedProfilesPagesAreAtFullSize(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a browser")
	}
	if _, err := Find(""); err != nil {
		t.Skip("no Chrome or Edge here")
	}
	dir := profileDir(t)
	if err := os.WriteFile(filepath.Join(dir, profileNameFile), []byte("Default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 1.2 to the power of the level: 125% for every page, 150% for the
	// test site's host.
	prefs := `{"partition":{"default_zoom_level":{"x":1.2239010857415449},` +
		`"per_host_zoom_levels":{"x":{"127.0.0.1":{"last_modified":"13370000000000000","zoom_level":2.2239010857415449}}}}}`
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Default", "Preferences"), []byte(prefs), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Options{DataDir: dir})
	t.Cleanup(m.Close)
	ctx := ctxFor(t)
	tab, err := m.Tab(ctx, "conv1")
	if err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(ctx, zoomSite(t)); err != nil {
		t.Fatal(err)
	}
	if w := pageWidth(t, ctx, tab); w != ViewportWidth {
		t.Fatalf("the page is laid out %v pixels wide, want %d: it is zoomed", w, ViewportWidth)
	}
	// The field's centre in the 1280x800 picture.
	for _, typ := range []string{"mousePressed", "mouseReleased"} {
		if err := tab.Mouse(ctx, MouseEvent{Type: typ, X: 850, Y: 520, Button: "left", ClickCount: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tab.Type(ctx, "typed"); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := tab.eval(ctx, `(document.activeElement && document.activeElement.id) + ':' + document.getElementById('f').value`, &got); err != nil {
		t.Fatal(err)
	}
	if got != "f:typed" {
		t.Errorf("after a click on the field and typing, focus and value are %q, want f:typed", got)
	}
}

// A key goes to the browser with the code a page reads and no native key
// code: with one, the browser runs its own shortcut for a key the page
// leaves unused.
func TestAKeyCarriesNoNativeKeyCode(t *testing.T) {
	sb, _ := newScriptedBrowser(t, nil, nil)
	tab := &Tab{conn: sb.conn, session: "sA"}
	if err := tab.KeyEvent(context.Background(), KeyEvent{Type: "rawKeyDown", Key: "F1", Code: "F1", KeyCode: 112}); err != nil {
		t.Fatal(err)
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	var sent map[string]any
	for _, c := range sb.calls {
		if c.Method == "Input.dispatchKeyEvent" {
			sent = c.Params
		}
	}
	if sent == nil {
		t.Fatal("the key was not sent")
	}
	if code, _ := sent["windowsVirtualKeyCode"].(float64); code != 112 {
		t.Errorf("windowsVirtualKeyCode = %v, want 112", sent["windowsVirtualKeyCode"])
	}
	if native, ok := sent["nativeVirtualKeyCode"]; ok {
		t.Errorf("the key carries the native key code %v", native)
	}
}

// pagesOpen is how many pages the browser has open.
func pagesOpen(t *testing.T, ctx context.Context, m *Manager) int {
	t.Helper()
	var out struct {
		TargetInfos []struct {
			Type string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := m.proc.conn.Call(ctx, "", "Target.getTargets", nil, &out); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ti := range out.TargetInfos {
		if ti.Type == "page" {
			n++
		}
	}
	return n
}

// A key the page does not use does nothing in the browser. With a native
// key code on it, the browser ran its own shortcut: a help page, a new
// tab or its history opened in front of the conversation's tab, whose
// page was then hidden and no longer drawn; the tab closed; the page
// zoomed. Which keys did it depended on the system (Shift alone and
// Escape on macOS; F1, Ctrl+T, Ctrl+H, Ctrl+W and Ctrl+plus on Windows and
// Linux), so each is pressed in a browser of its own.
func TestAKeyFromThePanelRunsNoBrowserShortcut(t *testing.T) {
	site := zoomSite(t)
	for _, k := range []KeyEvent{
		{Key: "Shift", Code: "ShiftLeft", KeyCode: 16, Modifiers: ModShift},
		{Key: "Escape", Code: "Escape", KeyCode: 27},
		{Key: "F1", Code: "F1", KeyCode: 112},
		{Key: "t", Code: "KeyT", KeyCode: 84, Modifiers: ModCtrl},
		{Key: "h", Code: "KeyH", KeyCode: 72, Modifiers: ModCtrl},
		{Key: "w", Code: "KeyW", KeyCode: 87, Modifiers: ModCtrl},
		{Key: "=", Code: "Equal", KeyCode: 187, Modifiers: ModCtrl},
	} {
		name := k.Key
		if k.Modifiers&ModCtrl != 0 {
			name = "Ctrl+" + name
		}
		t.Run(name, func(t *testing.T) {
			m := testManager(t)
			ctx := ctxFor(t)
			tab, err := m.Tab(ctx, "conv1")
			if err != nil {
				t.Fatal(err)
			}
			if err := tab.Navigate(ctx, site); err != nil {
				t.Fatal(err)
			}
			before := pagesOpen(t, ctx, m)
			for _, typ := range []string{"rawKeyDown", "keyUp"} {
				k.Type = typ
				if err := tab.KeyEvent(ctx, k); err != nil {
					t.Fatal(err)
				}
			}
			// What the browser did with the key, it did within a second.
			time.Sleep(1500 * time.Millisecond)
			if m.Existing("conv1") != tab {
				t.Fatal("the conversation's tab is gone")
			}
			var state string
			if err := tab.eval(ctx, `document.visibilityState + ' ' + innerWidth + ' ' + location.pathname`, &state); err != nil {
				t.Fatal(err)
			}
			if want := fmt.Sprintf("visible %d /", ViewportWidth); state != want {
				t.Errorf("the page is %q after the key, want %q", state, want)
			}
			if after := pagesOpen(t, ctx, m); after != before {
				t.Errorf("the browser has %d pages open after the key, and had %d", after, before)
			}
		})
	}
}
