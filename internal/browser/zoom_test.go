package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	dir := t.TempDir()
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

// The browser's zoom shortcut is not sent to the page, from the panel or
// from the model: where a browser acts on it (Windows and Linux), the
// page would be laid out narrower than its picture from then on.
func TestTheBrowsersZoomShortcutIsNotSent(t *testing.T) {
	zoom := []KeyEvent{
		{Type: "rawKeyDown", Key: "=", Code: "Equal", KeyCode: 187, Modifiers: ModCtrl},
		{Type: "rawKeyDown", Key: "+", Code: "Equal", KeyCode: 187, Modifiers: ModCtrl | ModShift},
		{Type: "rawKeyDown", Key: "-", Code: "Minus", KeyCode: 189, Modifiers: ModCtrl},
		{Type: "rawKeyDown", Key: "0", Code: "Digit0", KeyCode: 48, Modifiers: ModCtrl},
		{Type: "rawKeyDown", Key: "+", Code: "NumpadAdd", KeyCode: 107, Modifiers: ModCtrl},
		{Type: "rawKeyDown", Key: "-", Code: "NumpadSubtract", KeyCode: 109, Modifiers: ModCtrl},
		{Type: "rawKeyDown", Key: "0", Code: "Numpad0", KeyCode: 96, Modifiers: ModCtrl},
		{Type: "rawKeyDown", Key: "=", Code: "Equal", KeyCode: 187, Modifiers: ModMeta},
		{Type: "keyUp", Key: "=", Code: "Equal", KeyCode: 187, Modifiers: ModCtrl},
		// As the model's key action builds one: no key code.
		{Type: "rawKeyDown", Key: "-", Modifiers: ModCtrl},
	}
	// Nothing to send it on: a key that got past the check would not
	// return at all.
	tab := &Tab{}
	for _, e := range zoom {
		if err := tab.KeyEvent(context.Background(), e); !errors.Is(err, ErrZoomShortcut) {
			t.Errorf("%+v: %v, want the zoom shortcut refused", e, err)
		}
	}
	for _, e := range []KeyEvent{
		{Type: "keyDown", Key: "=", Code: "Equal", KeyCode: 187, Text: "="},
		{Type: "keyDown", Key: "0", Code: "Digit0", KeyCode: 48, Text: "0"},
		{Type: "keyDown", Key: "+", Code: "Equal", KeyCode: 187, Text: "+", Modifiers: ModShift},
		{Type: "rawKeyDown", Key: "a", Code: "KeyA", KeyCode: 65, Modifiers: ModCtrl},
		// Ctrl+Alt is AltGr on Windows, and a page's own shortcut elsewhere.
		{Type: "rawKeyDown", Key: "0", Code: "Digit0", KeyCode: 48, Modifiers: ModCtrl | ModAlt},
	} {
		if zoomShortcut(e) {
			t.Errorf("%+v is taken for a zoom shortcut", e)
		}
	}
}

// With the shortcut pressed, from the panel and from the model, the page
// stays as wide as its picture.
func TestAZoomShortcutLeavesThePageAtFullSize(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	tab, err := m.Tab(ctx, "conv1")
	if err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(ctx, zoomSite(t)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		for _, typ := range []string{"rawKeyDown", "keyUp"} {
			err := tab.KeyEvent(ctx, KeyEvent{Type: typ, Key: "=", Code: "Equal", KeyCode: 187, Modifiers: ModCtrl})
			if !errors.Is(err, ErrZoomShortcut) {
				t.Fatalf("Ctrl+= from the panel: %v", err)
			}
		}
	}
	if err := tab.Press(ctx, "ctrl+0"); !errors.Is(err, ErrZoomShortcut) || !strings.Contains(err.Error(), "100%") {
		t.Errorf("ctrl+0 from the model: %v", err)
	}
	if w := pageWidth(t, ctx, tab); w != ViewportWidth {
		t.Errorf("the page is laid out %v pixels wide after the shortcut, want %d", w, ViewportWidth)
	}
}
