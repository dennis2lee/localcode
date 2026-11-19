package browser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// guardSite serves a start page with every route a denied page can be
// reached by, and records what reached it and with which user agent.
type guardSite struct {
	*httptest.Server
	mu     sync.Mutex
	seen   []string
	agents []string
}

func newGuardSite(t *testing.T) *guardSite {
	g := &guardSite{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.seen = append(g.seen, r.URL.Path)
		g.agents = append(g.agents, r.UserAgent())
		g.mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/bank/home", http.StatusFound)
		case "/framed":
			fmt.Fprint(w, `<!doctype html><title>Framed</title><iframe src="/bank/frame"></iframe>`)
		case "/ahead":
			// Every way a page has the browser contact a URL other than
			// loading it: ahead of a click, as a resource, from script.
			fmt.Fprint(w, `<!doctype html><title>Ahead</title>
<link rel="prefetch" href="/bank/link-prefetch">
<link rel="preload" as="fetch" href="/bank/preload" crossorigin>
<script type="speculationrules">{"prefetch": [{"source": "list", "urls": ["/bank/spec-prefetch"]}], "prerender": [{"source": "list", "urls": ["/bank/spec-prerender"]}]}</script>
<img src="/bank/img.png">
<script>fetch('/bank/fetch').catch(() => {}); navigator.sendBeacon('/bank/beacon', 'x');
try { new EventSource('/bank/events'); } catch (e) {}</script>`)
		case "/sw.js":
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, `self.addEventListener('fetch', (e) => e.respondWith(fetch(e.request)));`)
		case "/np-sw.js":
			// Navigation preload: the browser sends the navigation's
			// request itself while the worker starts, and the worker
			// answers with that response.
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, `self.addEventListener('activate', (e) => e.waitUntil(self.registration.navigationPreload.enable().then(() => self.clients.claim())));
self.addEventListener('fetch', (e) => { if (e.request.mode === 'navigate') e.respondWith(e.preloadResponse.then((r) => r || fetch(e.request))); });`)
		case "/np-worker":
			fmt.Fprint(w, `<!doctype html><title>Preload</title><script>
navigator.serviceWorker.register('/np-sw.js', {scope: '/np/'}).then((reg) => new Promise((done) => {
  const sw = reg.installing || reg.waiting || reg.active;
  if (sw.state === 'activated') return done();
  sw.addEventListener('statechange', () => { if (sw.state === 'activated') done(); });
})).then(() => fetch('/after-np'));
</script>`)
		case "/worker":
			// A service worker: its script is fetched before the worker
			// is a target the guard can set up, and it then answers the
			// page's own requests.
			fmt.Fprint(w, `<!doctype html><title>Worker</title><script>
navigator.serviceWorker.register('/sw.js').then(() => navigator.serviceWorker.ready)
  .then(() => fetch('/after-worker')).then(() => { document.title = 'worked'; });
</script>`)
		case "/tall":
			fmt.Fprint(w, `<!doctype html><title>Tall</title><style>body{margin:0}</style><div style="height:3000px;background:linear-gradient(red,blue)"></div><button id=b style="width:120px;height:40px">Small</button>`)
		default:
			if strings.HasPrefix(r.URL.Path, "/bank") {
				fmt.Fprint(w, `<!doctype html><title>Bank</title><p>balance SECRET</p>`)
				return
			}
			fmt.Fprint(w, `<!doctype html><title>Start</title><a href="/bank/x" target="_blank">popup</a><a href="/bank/y">same</a>`)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *guardSite) reached(prefix string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.seen {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func guardedManager(t *testing.T) *Manager {
	m := testManager(t)
	m.opts.Block = func(url string) bool { return strings.Contains(url, "/bank") }
	return m
}

// A page a rule denies is never loaded, whatever leads to it.
func TestADeniedPageIsNeverLoaded(t *testing.T) {
	m := guardedManager(t)
	ctx := ctxFor(t)
	site := newGuardSite(t)
	tab, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}

	// A navigate straight there fails.
	if err := tab.Navigate(ctx, site.URL+"/bank/direct"); err == nil {
		t.Error("navigating to a denied page succeeded")
	}
	// A redirect there stops at the redirect.
	tab.Navigate(ctx, site.URL+"/redirect")
	// A frame there is not loaded.
	tab.Navigate(ctx, site.URL+"/framed")
	time.Sleep(300 * time.Millisecond)
	// A link there, in the same tab and in a new one.
	tab.Navigate(ctx, site.URL+"/start")
	snap, _ := tab.Read(ctx)
	for _, name := range []string{"same", "popup"} {
		for _, e := range snap.Elements {
			if e.Name == name {
				tab.Click(ctx, float64(e.X), float64(e.Y), 1)
				time.Sleep(500 * time.Millisecond)
			}
		}
	}
	// A navigation a service worker answers with its own fetch: the page
	// is under an allowed origin's worker, and the denied path is not.
	tab.Navigate(ctx, site.URL+"/worker")
	deadline := time.Now().Add(5 * time.Second)
	for !site.reached("/after-worker") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !site.reached("/after-worker") {
		t.Fatal("the service worker never answered; the test is not testing it")
	}
	if err := tab.Navigate(ctx, site.URL+"/bank/under-worker"); err == nil {
		t.Error("navigating to a denied page a service worker answers succeeded")
	}
	// A worker that answers navigations from navigation preload.
	tab.Navigate(ctx, site.URL+"/np-worker")
	deadline = time.Now().Add(5 * time.Second)
	for !site.reached("/after-np") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !site.reached("/after-np") {
		t.Fatal("the preloading worker never activated; the test is not testing it")
	}
	if err := tab.Navigate(ctx, site.URL+"/np/bank/preloaded"); err == nil {
		t.Error("navigating to a denied page under a preloading worker succeeded")
	}
	if site.reached("/np/bank") {
		t.Errorf("a denied page was requested by navigation preload: %v", site.seen)
	}
	// Background Fetch, which downloads outside the page, is not there.
	var bgf string
	if err := tab.eval(ctx, "typeof BackgroundFetchManager", &bgf); err != nil || bgf != "undefined" {
		t.Errorf("Background Fetch is available to pages: %q, %v", bgf, err)
	}
	if site.reached("/bank") {
		t.Errorf("a denied page was requested: %v", site.seen)
	}
}

// A denied site gets no HTTP request from the browser, not only no page
// load: no prefetch or prerender a page asks for (the browser makes those
// itself, outside the guard, so preloading is off), and no image, fetch,
// beacon or event stream. A WebSocket is not covered: the interception the
// guard uses does not see one, and USAGE.md says so.
func TestADeniedSiteGetsNoRequestAtAll(t *testing.T) {
	m := guardedManager(t)
	ctx := ctxFor(t)
	site := newGuardSite(t)
	tab, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(ctx, site.URL+"/ahead"); err != nil {
		t.Fatal(err)
	}
	// Prefetch waits for an idle moment.
	time.Sleep(1500 * time.Millisecond)
	if site.reached("/bank") {
		site.mu.Lock()
		t.Errorf("the denied site was sent a request: %v", site.seen)
		site.mu.Unlock()
	}

	// Again after a restart, on the profile the browser has now written
	// itself, Secure Preferences included: preloading stays off.
	m.Close()
	tab, err = m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(ctx, site.URL+"/ahead"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if site.reached("/bank") {
		site.mu.Lock()
		t.Errorf("after a restart the denied site was sent a request: %v", site.seen)
		site.mu.Unlock()
	}
}

// Every request the browser makes carries the mark the daemon refuses,
// and none says it is headless.
func TestEveryRequestCarriesTheMark(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := newGuardSite(t)
	tab, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	tab.Navigate(ctx, site.URL+"/framed")
	tab.Navigate(ctx, site.URL+"/worker")
	deadline := time.Now().Add(5 * time.Second)
	for !site.reached("/after-worker") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !site.reached("/sw.js") {
		t.Fatal("the service worker's script was never fetched; the test is not testing it")
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.agents) < 2 {
		t.Fatalf("only %d request(s) seen", len(site.agents))
	}
	for i, ua := range site.agents {
		if !strings.Contains(ua, UserAgentMark) || strings.Contains(ua, "HeadlessChrome") {
			t.Errorf("request %s had user agent %q", site.seen[i], ua)
		}
	}
}

func TestCaptures(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := newGuardSite(t)
	tab, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	tab.Navigate(ctx, site.URL+"/tall")
	size := func(b []byte) (int, int) {
		cfg, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("not a PNG: %v", err)
		}
		return cfg.Width, cfg.Height
	}
	view, err := tab.Capture(ctx, Shot{})
	if err != nil {
		t.Fatal(err)
	}
	if w, h := size(view.PNG); w != ViewportWidth || h != ViewportHeight {
		t.Errorf("visible capture is %dx%d", w, h)
	}
	full, err := tab.Capture(ctx, Shot{FullPage: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, h := size(full.PNG); h < 3000 || full.Height < 3000 {
		t.Errorf("full page capture is %d high (reported %d); the page is over 3000", h, full.Height)
	}
	snap, _ := tab.Read(ctx)
	ref := 0
	for _, e := range snap.Elements {
		if e.Name == "Small" {
			ref = e.Ref
		}
	}
	el, err := tab.Capture(ctx, Shot{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	if w, h := size(el.PNG); w != 120 || h != 40 {
		t.Errorf("element capture is %dx%d, want the button's 120x40", w, h)
	}
	if _, err := tab.Capture(ctx, Shot{Ref: 999}); err == nil {
		t.Error("capturing an element that is not there succeeded")
	}
}

// A browser on this profile that another live localcode is using is that
// localcode's, and is not closed.
func TestABrowserAnotherLocalcodeUsesIsLeftAlone(t *testing.T) {
	first := testManager(t)
	ctx := ctxFor(t)
	if _, err := first.Tab(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	// As if another process owned it: this test's parent is alive.
	if err := os.WriteFile(filepath.Join(first.opts.DataDir, ownerFile), []byte(fmt.Sprint(os.Getppid())), 0o600); err != nil {
		t.Fatal(err)
	}
	second := NewManager(Options{DataDir: first.opts.DataDir})
	defer second.Close()
	_, err := second.Tab(ctx, "c")
	if _, ok := err.(ErrInUse); !ok {
		t.Fatalf("second start: %v, want ErrInUse", err)
	}
	if first.Existing("c") == nil {
		t.Error("the first localcode's browser was closed")
	}
}

func TestLaunchArgsKeepAFreshProfileOffTheKeychain(t *testing.T) {
	has := func(args []string, flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	want := map[string]string{"darwin": "--use-mock-keychain", "linux": "--password-store=basic"}[runtime.GOOS]
	if want == "" {
		t.Skip("no system secret store to keep a fresh profile off here")
	}
	if !has(launchArgs("/d", "Default", false), want) {
		t.Errorf("a fresh profile is started without %s, so it asks the system store for a key it does not have", want)
	}
	if has(launchArgs("/d", "Default", true), want) {
		t.Errorf("an imported profile is started with %s, so its cookies cannot be decrypted", want)
	}
}

// The guard registers handlers for every target it attaches: each frame
// from another site, each worker. They go when the target does, so a
// page that keeps making and dropping frames does not grow them.
func TestTheGuardForgetsTargetsThatAreGone(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	// Another site: "localhost" and "127.0.0.1" are different sites, so
	// its frame is a process and a target of its own.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><p>framed</p>`)
	}))
	defer other.Close()
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><title>Churn</title><script>
async function churn(n) {
  for (let i = 0; i < n; i++) {
    const f = document.createElement('iframe');
    f.src = %q;
    document.body.appendChild(f);
    await new Promise((r) => f.onload = r);
    const w = new Worker(URL.createObjectURL(new Blob(['postMessage(1)'])));
    await new Promise((r) => w.onmessage = r);
    w.terminate();
    f.remove();
  }
  document.title = 'done ' + n;
}
</script>`, otherURL)
	}))
	defer site.Close()

	tab, err := m.Tab(ctx, "churn")
	if err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(ctx, site.URL); err != nil {
		t.Fatal(err)
	}
	conn := m.proc.conn
	run := func(n int) {
		t.Helper()
		if err := tab.eval(ctx, fmt.Sprintf("churn(%d)", n), nil); err != nil {
			t.Fatal(err)
		}
	}
	settled := func() int {
		// Detach events trail the page's own work a little.
		time.Sleep(500 * time.Millisecond)
		return conn.HandlerCount()
	}
	run(2)
	before := settled()
	run(20)
	after := settled()
	if after > before+2 {
		t.Errorf("handlers went from %d to %d over 20 frames and workers that came and went", before, after)
	}
}

// Preloading goes off in the profile, and nothing else in it changes:
// not a setting, not a number too long for a float64.
func TestNoPreloadingLeavesTheRestOfThePreferences(t *testing.T) {
	dir := t.TempDir()
	in := `{"net":{"network_prediction_options":0,"other":true},"profile":{"last_engagement_time":"13350000000000001","exit_type":"Normal"},"big":13350000000000001}`
	if err := os.WriteFile(filepath.Join(dir, "Preferences"), []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := noPreloading(dir); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "Preferences"))
	for _, want := range []string{`"network_prediction_options":2`, `"other":true`, `"exit_type":"Normal"`, `"big":13350000000000001`, `"last_engagement_time":"13350000000000001"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("Preferences lack %s: %s", want, out)
		}
	}
	// A file that is not an object is started over, null included.
	for _, bad := range []string{"null", "[]", `"x"`, "", "{"} {
		os.WriteFile(filepath.Join(dir, "Preferences"), []byte(bad), 0o600)
		if err := noPreloading(dir); err != nil {
			t.Errorf("Preferences %q: %v", bad, err)
		}
		if out, _ := os.ReadFile(filepath.Join(dir, "Preferences")); !strings.Contains(string(out), `"network_prediction_options":2`) {
			t.Errorf("Preferences %q became %s", bad, out)
		}
	}
	// A profile with no Preferences yet gets one.
	fresh := filepath.Join(t.TempDir(), "Default")
	if err := noPreloading(fresh); err != nil {
		t.Fatal(err)
	}
	if out, _ := os.ReadFile(filepath.Join(fresh, "Preferences")); !strings.Contains(string(out), `"network_prediction_options":2`) {
		t.Errorf("a fresh profile's Preferences: %s", out)
	}
}

// The browser's own interface pages are not attached, so none is held
// paused: each new window makes one (the address bar's suggestion popup),
// and on a loaded machine one held paused stalled the page in its window
// for a minute, both in a new tab a click opened and in a tab opened
// beside another.
func TestTheBrowsersOwnInterfaceIsNotHeld(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	if _, err := m.Tab(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	attached := map[string]int{}
	m.proc.conn.On("", "Target.attachedToTarget", func(p json.RawMessage) {
		var ev struct {
			TargetInfo struct {
				Type string `json:"type"`
			} `json:"targetInfo"`
		}
		json.Unmarshal(p, &ev)
		mu.Lock()
		attached[ev.TargetInfo.Type]++
		mu.Unlock()
	})
	for _, conv := range []string{"second", "third", "fourth"} {
		if _, err := m.Tab(ctx, conv); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if attached["page"] == 0 {
		t.Fatalf("no page was attached for three new tabs; the test is not testing anything: %v", attached)
	}
	if n := attached["browser_ui"]; n != 0 {
		t.Errorf("the browser's own interface was attached %d time(s): %v", n, attached)
	}
}
