package browser

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Probe, not a test of the product: where a click sent the way the panel
// sends it lands, under a profile whose page zoom is not 100%.
const probePage = `<!doctype html><title>ready</title>
<style>body{margin:0}#f{position:absolute;left:100px;top:100px;width:600px;height:60px}
#b{position:absolute;left:800px;top:500px;width:300px;height:100px}</style>
<input id="f"><button id="b">press</button>
<script>
window.log = [];
document.addEventListener('mousedown', e => window.log.push('down@' + e.clientX + ',' + e.clientY + ' on ' + (e.target.id || e.target.tagName)));
</script>`

func probeZoom(t *testing.T, name, prefs string) {
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
	if prefs != "" {
		if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Default", "Preferences"), []byte(prefs), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(Options{DataDir: dir, ChromePath: os.Getenv("LC_PROBE_BROWSER")})
	t.Cleanup(m.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, probePage)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	tab, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}
	var metrics struct {
		InnerW, InnerH, DPR, VVScale float64
		FL, FT, FR, FB               float64
		BL, BT, BR, BB               float64
	}
	if err := tab.eval(ctx, `(() => { const f = document.getElementById('f').getBoundingClientRect(), b = document.getElementById('b').getBoundingClientRect();
	  return {InnerW: innerWidth, InnerH: innerHeight, DPR: devicePixelRatio, VVScale: visualViewport.scale,
	    FL: f.left, FT: f.top, FR: f.right, FB: f.bottom, BL: b.left, BT: b.top, BR: b.right, BB: b.bottom}; })()`, &metrics); err != nil {
		t.Fatal(err)
	}
	frames, stop, err := tab.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var fw, fh int
	select {
	case f := <-frames:
		if cfg, err := jpeg.DecodeConfig(bytes.NewReader(f)); err == nil {
			fw, fh = cfg.Width, cfg.Height
		}
	case <-time.After(5 * time.Second):
	}
	stop()
	t.Logf("[%s] innerWidth x innerHeight = %vx%v, devicePixelRatio %v, frame %dx%d", name, metrics.InnerW, metrics.InnerH, metrics.DPR, fw, fh)
	t.Logf("[%s] in the page's own coordinates: field %v,%v..%v,%v  button %v,%v..%v,%v", name, metrics.FL, metrics.FT, metrics.FR, metrics.FB, metrics.BL, metrics.BT, metrics.BR, metrics.BB)

	// Where the panel aims: the element's place in the picture the person
	// sees, as a point on a 1280x800 page (browser.js pagePoint).
	seen := func(l, top, r, b float64) (float64, float64) {
		cx, cy := (l+r)/2, (top+b)/2
		return cx * float64(ViewportWidth) / metrics.InnerW, cy * float64(ViewportHeight) / metrics.InnerH
	}
	click := func(what string, x, y float64) {
		_ = tab.eval(ctx, `window.log = []`, nil)
		for _, typ := range []string{"mousePressed", "mouseReleased"} {
			if err := tab.Mouse(ctx, MouseEvent{Type: typ, X: x, Y: y, Button: "left", ClickCount: 1}); err != nil {
				t.Logf("[%s] %s: %v", name, typ, err)
			}
		}
		var got []string
		_ = tab.eval(ctx, `window.log`, &got)
		t.Logf("[%s] click on the %s as the panel sends it (%.0f, %.0f): page got %v", name, what, x, y, got)
	}
	x, y := seen(metrics.FL, metrics.FT, metrics.FR, metrics.FB)
	click("field", x, y)
	x, y = seen(metrics.BL, metrics.BT, metrics.BR, metrics.BB)
	click("button", x, y)
}

func TestZZProbeZoom(t *testing.T) {
	probeZoom(t, "no zoom set", "")
	probeZoom(t, "default zoom 125%", `{"partition":{"default_zoom_level":{"x":1.2239010857415449}}}`)
	probeZoom(t, "default zoom 150%", `{"partition":{"default_zoom_level":{"x":2.2239010857415449}}}`)
	probeZoom(t, "this host at 125%", `{"partition":{"per_host_zoom_levels":{"x":{"127.0.0.1":{"last_modified":"13370000000000000","zoom_level":1.2239010857415449}}}}}`)
}

// Can input the panel forwards change the page's zoom while the browser
// runs? Ctrl+wheel and Ctrl+plus, as the panel sends them.
func TestZZProbeRuntimeZoom(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, profileNameFile), []byte("Default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Options{DataDir: dir, ChromePath: os.Getenv("LC_PROBE_BROWSER")})
	t.Cleanup(m.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, probePage)
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
	width := func() float64 {
		var w float64
		_ = tab.eval(ctx, `innerWidth`, &w)
		return w
	}
	t.Logf("innerWidth at the start: %v", width())
	for i := 0; i < 3; i++ {
		if err := tab.Mouse(ctx, MouseEvent{Type: "mouseWheel", X: 640, Y: 400, DeltaY: -120, Modifiers: 2}); err != nil {
			t.Logf("wheel: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Logf("innerWidth after three Ctrl+wheel up: %v", width())
	for i := 0; i < 2; i++ {
		for _, typ := range []string{"rawKeyDown", "keyUp"} {
			if err := tab.KeyEvent(ctx, KeyEvent{Type: typ, Key: "=", Code: "Equal", KeyCode: 187, Modifiers: 2}); err != nil {
				t.Logf("key: %v", err)
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Logf("innerWidth after two Ctrl+= : %v", width())
	for i := 0; i < 2; i++ {
		for _, typ := range []string{"rawKeyDown", "keyUp"} {
			_ = tab.KeyEvent(ctx, KeyEvent{Type: typ, Key: "+", Code: "NumpadAdd", KeyCode: 107, Modifiers: 2})
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Logf("innerWidth after two Ctrl+numpad plus: %v", width())
	for i := 0; i < 3; i++ {
		_ = tab.Mouse(ctx, MouseEvent{Type: "mouseWheel", X: 640, Y: 400, DeltaY: 120, Modifiers: 2})
		time.Sleep(300 * time.Millisecond)
	}
	t.Logf("innerWidth after three Ctrl+wheel down: %v", width())
	if err := tab.Navigate(ctx, srv.URL+"/again"); err != nil {
		t.Fatal(err)
	}
	t.Logf("innerWidth after loading the same site again: %v", width())
}

// The display's own scale, as the browser takes it from Windows: the
// flag stands in for a 125% or 150% display.
func TestZZProbeDisplayScale(t *testing.T) {
	for _, scale := range []string{"1.25", "1.5"} {
		t.Setenv("LC_PROBE_ARGS", "--force-device-scale-factor="+scale)
		probeZoom(t, "display scale "+scale, "")
	}
}
