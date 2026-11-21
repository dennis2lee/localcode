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
	"strings"
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
	width := func() string {
		var w string
		_ = tab.eval(ctx, `innerWidth + ' (devicePixelRatio ' + devicePixelRatio + ', pinch scale ' + visualViewport.scale + ', visual viewport ' + Math.round(visualViewport.width) + ' wide at ' + Math.round(visualViewport.offsetLeft) + ',' + Math.round(visualViewport.offsetTop) + ')'`, &w)
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
	for i := 0; i < 2; i++ {
		for _, typ := range []string{"rawKeyDown", "keyUp"} {
			_ = tab.KeyEvent(ctx, KeyEvent{Type: typ, Key: "=", Code: "Equal", KeyCode: 187, Modifiers: 4})
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Logf("innerWidth after two Cmd+= : %v", width())
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

// Which keys, sent the way the panel sends them, leave the page unable to
// take typing or a click afterwards? One browser per key.
const keysPage = `<!doctype html><title>keys</title>
<style>body{margin:0}#f{position:absolute;left:100px;top:100px;width:600px;height:60px}
#b{position:absolute;left:800px;top:500px;width:300px;height:100px}</style>
<input id="f"><button id="b" onclick="window.clicked=(window.clicked||0)+1">press</button>`

type probeKey struct {
	name, key, code string
	vk, mods        int
}

func probeOneKey(t *testing.T, k probeKey) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, profileNameFile), []byte("Default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Options{DataDir: dir, ChromePath: os.Getenv("LC_PROBE_BROWSER")})
	defer m.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, keysPage)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tab, err := m.Tab(ctx, "c")
	if err != nil {
		t.Logf("RESULT %s: no tab: %v", k.name, err)
		return
	}
	if err := tab.Navigate(ctx, srv.URL); err != nil {
		t.Logf("RESULT %s: navigate: %v", k.name, err)
		return
	}
	short := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, 4*time.Second) }
	click := func(x, y float64) {
		for _, typ := range []string{"mousePressed", "mouseReleased"} {
			c, done := short()
			_ = tab.Mouse(c, MouseEvent{Type: typ, X: x, Y: y, Button: "left", ClickCount: 1})
			done()
		}
	}
	typeChar := func(ch string, vk int) {
		c, done := short()
		_ = tab.KeyEvent(c, KeyEvent{Type: "keyDown", Key: ch, Code: "Key" + string(rune(vk)), KeyCode: vk, Text: ch})
		_ = tab.KeyEvent(c, KeyEvent{Type: "keyUp", Key: ch, Code: "Key" + string(rune(vk)), KeyCode: vk})
		done()
	}
	click(404, 133)
	typeChar("a", 65)
	if k.key != "" {
		c, done := short()
		_ = tab.KeyEvent(c, KeyEvent{Type: "rawKeyDown", Key: k.key, Code: k.code, KeyCode: k.vk, Modifiers: k.mods})
		_ = tab.KeyEvent(c, KeyEvent{Type: "keyUp", Key: k.key, Code: k.code, KeyCode: k.vk, Modifiers: k.mods})
		done()
	}
	time.Sleep(1200 * time.Millisecond)
	typeChar("b", 66)
	click(950, 550)
	time.Sleep(300 * time.Millisecond)
	var state string
	c, done := short()
	if err := tab.eval(c, `'value=' + document.getElementById('f').value + ' clicked=' + (window.clicked||0) + ' hasFocus=' + document.hasFocus() + ' vis=' + document.visibilityState + ' width=' + innerWidth + ' at=' + location.pathname`, &state); err != nil {
		state = "the page did not answer: " + err.Error()
	}
	done()
	var out struct {
		TargetInfos []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"targetInfos"`
	}
	c, done = short()
	_ = m.proc.conn.Call(c, "", "Target.getTargets", nil, &out)
	done()
	pages := ""
	n := 0
	for _, ti := range out.TargetInfos {
		if ti.Type == "page" || ti.Type == "other" {
			n++
			u := ti.URL
			if len(u) > 48 {
				u = u[:48]
			}
			pages += " " + ti.Type + "(" + u + ")"
		}
	}
	verdict := "ok"
	if !strings.Contains(state, "clicked=1") || !strings.Contains(state, "vis=visible") || !strings.Contains(state, "width=1280") || !strings.HasSuffix(state, "at=/") || n != 2 {
		verdict = "BROKEN"
	}
	t.Logf("RESULT %-18s %-6s %s; still the conversation's=%v; %d pages:%s", k.name, verdict, state, m.Existing("c") != nil, n, pages)
}

func TestZZProbeKeys(t *testing.T) {
	keys := []probeKey{
		{"nothing", "", "", 0, 0},
		{"Shift", "Shift", "ShiftLeft", 16, 8}, {"Control", "Control", "ControlLeft", 17, 2}, {"Alt", "Alt", "AltLeft", 18, 1}, {"Meta", "Meta", "MetaLeft", 91, 4},
		{"Escape", "Escape", "Escape", 27, 0}, {"Tab", "Tab", "Tab", 9, 0}, {"Shift+Tab", "Tab", "Tab", 9, 8}, {"Backspace", "Backspace", "Backspace", 8, 0},
		{"ArrowLeft", "ArrowLeft", "ArrowLeft", 37, 0}, {"Home", "Home", "Home", 36, 0}, {"PageDown", "PageDown", "PageDown", 34, 0}, {"ContextMenu", "ContextMenu", "ContextMenu", 93, 0},
		{"F1", "F1", "F1", 112, 0}, {"F3", "F3", "F3", 114, 0}, {"F5", "F5", "F5", 116, 0}, {"F6", "F6", "F6", 117, 0}, {"F7", "F7", "F7", 118, 0},
		{"F10", "F10", "F10", 121, 0}, {"F11", "F11", "F11", 122, 0}, {"F12", "F12", "F12", 123, 0},
		{"Ctrl+T", "t", "KeyT", 84, 2}, {"Ctrl+N", "n", "KeyN", 78, 2}, {"Ctrl+W", "w", "KeyW", 87, 2}, {"Ctrl+Shift+N", "N", "KeyN", 78, 10}, {"Ctrl+Shift+T", "T", "KeyT", 84, 10},
		{"Ctrl+P", "p", "KeyP", 80, 2}, {"Ctrl+S", "s", "KeyS", 83, 2}, {"Ctrl+O", "o", "KeyO", 79, 2}, {"Ctrl+F", "f", "KeyF", 70, 2}, {"Ctrl+G", "g", "KeyG", 71, 2},
		{"Ctrl+L", "l", "KeyL", 76, 2}, {"Ctrl+E", "e", "KeyE", 69, 2}, {"Ctrl+K", "k", "KeyK", 75, 2}, {"Ctrl+H", "h", "KeyH", 72, 2}, {"Ctrl+J", "j", "KeyJ", 74, 2},
		{"Ctrl+U", "u", "KeyU", 85, 2}, {"Ctrl+D", "d", "KeyD", 68, 2}, {"Ctrl+R", "r", "KeyR", 82, 2}, {"Ctrl+Tab", "Tab", "Tab", 9, 2}, {"Ctrl+1", "1", "Digit1", 49, 2},
		{"Ctrl+Shift+I", "I", "KeyI", 73, 10}, {"Ctrl+Shift+Delete", "Delete", "Delete", 46, 10}, {"Ctrl+Shift+B", "B", "KeyB", 66, 10}, {"Ctrl+Shift+O", "O", "KeyO", 79, 10},
		{"Alt+Left", "ArrowLeft", "ArrowLeft", 37, 1}, {"Alt+Home", "Home", "Home", 36, 1}, {"Alt+D", "d", "KeyD", 68, 1}, {"Alt+F", "f", "KeyF", 70, 1}, {"Alt+E", "e", "KeyE", 69, 1},
		{"Cmd+T", "t", "KeyT", 84, 4}, {"Cmd+W", "w", "KeyW", 87, 4}, {"Cmd+L", "l", "KeyL", 76, 4}, {"Cmd+F", "f", "KeyF", 70, 4},
	}
	only := os.Getenv("LC_PROBE_KEY")
	for _, k := range keys {
		if only != "" && k.name != only {
			continue
		}
		probeOneKey(t, k)
	}
}
