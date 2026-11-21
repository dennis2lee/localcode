package browser

// Measurement only. Never committed.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func probeWindow(ctx context.Context, m *Manager, id string) int {
	var win struct {
		WindowID int `json:"windowId"`
	}
	_ = m.proc.conn.Call(ctx, "", "Browser.getWindowForTarget", map[string]any{"targetId": id}, &win)
	return win.WindowID
}

func probeClick(ctx context.Context, tab *Tab, y float64, button string, mods int) error {
	if err := tab.Mouse(ctx, MouseEvent{Type: "mouseMoved", X: 40, Y: y}); err != nil {
		return err
	}
	for _, typ := range []string{"mousePressed", "mouseReleased"} {
		if err := tab.Mouse(ctx, MouseEvent{Type: typ, X: 40, Y: y, Button: button, ClickCount: 1, Modifiers: mods}); err != nil {
			return err
		}
	}
	return nil
}

func TestZZProbeStray2(t *testing.T) {
	if os.Getenv("LC_PROBE") == "" {
		t.Skip("probe")
	}
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/second":
			fmt.Fprint(w, `<!doctype html><title>Second</title><p>second</p>`)
		case "/framed":
			u, _ := url.Parse(srvURL)
			fmt.Fprintf(w, `<!doctype html><title>Framed</title><iframe src="http://localhost:%s/" style="position:absolute;left:0;top:0;width:600px;height:400px;border:0"></iframe>`, u.Port())
		default:
			fmt.Fprint(w, strings.Replace(probeStrayPage, "</style>", "</style><button id=wopop2 onclick=\"window.open('/','_blank','width=300,height=200')\" style=\"top:310px\">popup root</button>", 1))
		}
	}))
	t.Cleanup(srv.Close)
	srvURL = srv.URL
	const plain, blank, wo, wopop2 = 25, 75, 175, 325
	mod := ModCtrl
	if runtime.GOOS == "darwin" {
		mod = ModMeta
	}
	two := func(t *testing.T, path string) (*Manager, context.Context, *Tab, *Tab) {
		m := testManager(t)
		ctx := ctxFor(t)
		tab, err := m.Tab(ctx, "conv1")
		if err != nil {
			t.Fatal(err)
		}
		if err := tab.Navigate(ctx, srv.URL+path); err != nil {
			t.Fatal(err)
		}
		other, err := m.Tab(ctx, "conv2")
		if err != nil {
			t.Fatal(err)
		}
		if err := other.Navigate(ctx, srv.URL+"/second"); err != nil {
			t.Fatal(err)
		}
		return m, ctx, tab, other
	}
	pages := func(t *testing.T, ctx context.Context, m *Manager, names map[string]string) string {
		var out struct {
			TargetInfos []struct {
				TargetID string `json:"targetId"`
				Type     string `json:"type"`
				URL      string `json:"url"`
				OpenerID string `json:"openerId"`
			} `json:"targetInfos"`
		}
		if err := m.proc.conn.Call(ctx, "", "Target.getTargets", nil, &out); err != nil {
			t.Fatal(err)
		}
		var s []string
		n := 0
		for _, ti := range out.TargetInfos {
			if ti.Type != "page" || ti.URL == "about:blank" {
				continue
			}
			name := names[ti.TargetID]
			if name == "" {
				n++
				name = fmt.Sprintf("N%d", n)
			}
			op := names[ti.OpenerID]
			if op == "" && ti.OpenerID != "" {
				op = "?"
			}
			s = append(s, fmt.Sprintf("%s{url=%q opener=%q window=%d}", name, strings.TrimPrefix(ti.URL, srv.URL), op, probeWindow(ctx, m, ti.TargetID)%1000))
		}
		return strings.Join(s, " ")
	}

	t.Run("popup window: where its new tabs land", func(t *testing.T) {
		for _, how := range []string{"blank link", "modified click", "shift click"} {
			t.Run(how, func(t *testing.T) {
				m, ctx, tab, other := two(t, "/")
				if err := probeClick(ctx, tab, wopop2, "left", 0); err != nil {
					t.Fatal(err)
				}
				time.Sleep(1500 * time.Millisecond)
				pop := m.Existing("conv1")
				if pop == tab {
					t.Fatal("no popup adopted")
				}
				// conv2's window is the one used last.
				front(other)
				_ = probeClick(ctx, other, 300, "left", 0)
				time.Sleep(300 * time.Millisecond)
				names := map[string]string{tab.targetID: "T1", other.targetID: "T2", pop.targetID: "POP"}
				var err error
				switch how {
				case "blank link":
					err = probeClick(ctx, pop, blank, "left", 0)
				case "modified click":
					err = probeClick(ctx, pop, plain, "left", mod)
				case "shift click":
					err = probeClick(ctx, pop, plain, "left", ModShift)
				}
				if err != nil {
					t.Fatal(err)
				}
				time.Sleep(1500 * time.Millisecond)
				var st1, st2, st3 string
				_ = tab.eval(ctx, `document.visibilityState`, &st1)
				_ = other.eval(ctx, `document.visibilityState`, &st2)
				_ = pop.eval(ctx, `document.visibilityState`, &st3)
				var c1 []string
				for _, ti := range m.Tabs(ctx, "conv1") {
					c1 = append(c1, names[ti.ID]+map[bool]string{true: "*", false: ""}[ti.Active])
				}
				t.Logf("RESULT popup/%s :: T1 %s, T2 %s, POP %s; conv1 tabs %q; %s", how, st1, st2, st3, c1, pages(t, ctx, m, names))
			})
		}
	})

	t.Run("adopted background popup", func(t *testing.T) {
		m, ctx, tab, other := two(t, "/")
		if err := probeClick(ctx, tab, wo, "left", mod); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1500 * time.Millisecond)
		pop := m.Existing("conv1")
		var st1, st3 string
		_ = tab.eval(ctx, `document.visibilityState`, &st1)
		if pop != nil {
			_ = pop.eval(ctx, `document.visibilityState`, &st3)
		}
		names := map[string]string{tab.targetID: "T1", other.targetID: "T2"}
		t.Logf("RESULT background popup :: adopted=%v T1 %s, active tab %s; %s", pop != tab, st1, st3, pages(t, ctx, m, names))
	})

	t.Run("a stray's visibility and window, at once", func(t *testing.T) {
		for _, g := range []struct {
			name   string
			button string
			mods   int
		}{{"background", "middle", 0}, {"foreground", "middle", ModShift}, {"window", "left", ModShift}} {
			t.Run(g.name, func(t *testing.T) {
				m, ctx, tab, _ := two(t, "/")
				conn := m.proc.conn
				var mu sync.Mutex
				var lines []string
				start := time.Now()
				var wg sync.WaitGroup
				conn.On("", "Target.targetCreated", func(p json.RawMessage) {
					var ev struct {
						TargetInfo struct {
							TargetID string `json:"targetId"`
							Type     string `json:"type"`
							URL      string `json:"url"`
							OpenerID string `json:"openerId"`
						} `json:"targetInfo"`
					}
					_ = json.Unmarshal(p, &ev)
					if ev.TargetInfo.Type != "page" || ev.TargetInfo.OpenerID != "" {
						return
					}
					at := time.Since(start)
					wg.Add(1)
					go func() {
						defer wg.Done()
						t0 := time.Now()
						w := probeWindow(ctx, m, ev.TargetInfo.TargetID)
						dw := time.Since(t0)
						t1 := time.Now()
						nt, err := attachTab(ctx, conn, ev.TargetInfo.TargetID)
						var st string
						if err == nil {
							err = nt.eval(ctx, `document.visibilityState + ' ' + location.href`, &st)
						}
						mu.Lock()
						lines = append(lines, fmt.Sprintf("created at %dms url=%q: window %d (same as T1: %v) in %s; attach+eval %q err=%v in %s",
							at.Milliseconds(), strings.TrimPrefix(ev.TargetInfo.URL, srv.URL), w%1000, w == probeWindow(ctx, m, tab.targetID), dw.Round(time.Millisecond), strings.ReplaceAll(st, srv.URL, ""), err, time.Since(t1).Round(time.Millisecond)))
						mu.Unlock()
					}()
				})
				start = time.Now()
				if err := probeClick(ctx, tab, plain, g.button, g.mods); err != nil {
					t.Fatal(err)
				}
				time.Sleep(1500 * time.Millisecond)
				wg.Wait()
				mu.Lock()
				defer mu.Unlock()
				t.Logf("RESULT stray/%s :: %s", g.name, strings.Join(lines, " | "))
			})
		}
	})

	t.Run("a link in a frame from another site", func(t *testing.T) {
		for _, g := range []struct {
			name string
			mods int
		}{{"modified click", mod}, {"shift click", ModShift}} {
			t.Run(g.name, func(t *testing.T) {
				m := testManager(t)
				ctx := ctxFor(t)
				conn := func() *Manager { return m }
				_ = conn
				// The browser has to be up before handlers can be put on it.
				first, err := m.Tab(ctx, "conv0")
				if err != nil {
					t.Fatal(err)
				}
				_ = first
				c := m.proc.conn
				var mu sync.Mutex
				var lines []string
				start := time.Now()
				note := func(format string, args ...any) {
					mu.Lock()
					defer mu.Unlock()
					lines = append(lines, fmt.Sprintf("%dms ", time.Since(start).Milliseconds())+fmt.Sprintf(format, args...))
				}
				var hook func(parent string) func(p json.RawMessage)
				hook = func(parent string) func(p json.RawMessage) {
					return func(p json.RawMessage) {
						var ev struct {
							SessionID  string `json:"sessionId"`
							TargetInfo struct {
								TargetID string `json:"targetId"`
								Type     string `json:"type"`
								URL      string `json:"url"`
							} `json:"targetInfo"`
						}
						_ = json.Unmarshal(p, &ev)
						if ev.TargetInfo.Type != "page" && ev.TargetInfo.Type != "iframe" {
							return
						}
						kind := ev.TargetInfo.Type
						sess := ev.SessionID
						c.On(sess, "Page.frameRequestedNavigation", func(pp json.RawMessage) {
							note("guard-session(%s) frameRequestedNavigation %s", kind, strings.ReplaceAll(string(pp), srv.URL, ""))
						})
						c.On(sess, "Target.attachedToTarget", hook(kind))
						go func() { _ = c.Call(ctx, sess, "Page.enable", nil, nil) }()
					}
				}
				c.On("", "Target.attachedToTarget", hook(""))
				c.On("", "Target.targetCreated", func(p json.RawMessage) {
					var ev struct {
						TargetInfo struct {
							Type     string `json:"type"`
							URL      string `json:"url"`
							OpenerID string `json:"openerId"`
						} `json:"targetInfo"`
					}
					_ = json.Unmarshal(p, &ev)
					if ev.TargetInfo.Type == "page" {
						note("targetCreated page url=%q opener=%q", ev.TargetInfo.URL, ev.TargetInfo.OpenerID)
					}
				})
				tab, err := m.Tab(ctx, "conv1")
				if err != nil {
					t.Fatal(err)
				}
				c.On(tab.session, "Page.frameRequestedNavigation", func(pp json.RawMessage) {
					note("tab-session frameRequestedNavigation %s", strings.ReplaceAll(string(pp), srv.URL, ""))
				})
				if err := tab.Navigate(ctx, srv.URL+"/framed"); err != nil {
					t.Fatal(err)
				}
				time.Sleep(1 * time.Second)
				mu.Lock()
				lines = nil
				start = time.Now()
				mu.Unlock()
				if err := probeClick(ctx, tab, plain, "left", g.mods); err != nil {
					t.Fatal(err)
				}
				time.Sleep(1500 * time.Millisecond)
				mu.Lock()
				defer mu.Unlock()
				t.Logf("RESULT frame/%s :: %s", g.name, strings.Join(lines, " | "))
			})
		}
	})
}
