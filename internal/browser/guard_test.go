package browser

import (
	"bytes"
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
	if site.reached("/bank") {
		t.Errorf("a denied page was requested: %v", site.seen)
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
	time.Sleep(300 * time.Millisecond)
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
