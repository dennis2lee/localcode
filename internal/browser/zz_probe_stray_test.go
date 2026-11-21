package browser

// Measurement only. Never committed.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const probeStrayPage = `<!doctype html><title>Links</title>
<style>a,button{position:absolute;left:10px;width:200px;height:30px;display:block}</style>
<a id=plain href="/second" style="top:10px">plain</a>
<a id=blank href="/second" target="_blank" style="top:60px">blank</a>
<a id=noop href="/second" target="_blank" rel="noopener noreferrer" style="top:110px">noopener</a>
<button id=wo onclick="window.open('/second')" style="top:160px">open</button>
<button id=wonoop onclick="window.open('/second','_blank','noopener')" style="top:210px">open noopener</button>
<button id=wopop onclick="window.open('/second','_blank','width=300,height=200')" style="top:260px">open popup</button>`

type probeLog struct {
	mu     sync.Mutex
	start  time.Time
	lines  []string
	labels map[string]string
	next   int
}

func (l *probeLog) label(id string) string {
	if id == "" {
		return "-"
	}
	if s, ok := l.labels[id]; ok {
		return s
	}
	l.next++
	s := fmt.Sprintf("N%d", l.next)
	l.labels[id] = s
	return s
}

func (l *probeLog) add(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf("%5dms ", time.Since(l.start).Milliseconds())+fmt.Sprintf(format, args...))
}

func TestZZProbeStray(t *testing.T) {
	if os.Getenv("LC_PROBE") == "" {
		t.Skip("probe")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/second" {
			fmt.Fprint(w, `<!doctype html><title>Second</title><p>second</p>`)
			return
		}
		fmt.Fprint(w, probeStrayPage)
	}))
	t.Cleanup(srv.Close)

	click := func(y float64, button string, mods int) func(ctx context.Context, tab *Tab) error {
		return func(ctx context.Context, tab *Tab) error {
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
	}
	enter := func(mods int) func(ctx context.Context, tab *Tab) error {
		return func(ctx context.Context, tab *Tab) error {
			if err := tab.eval(ctx, `document.getElementById('plain').focus()`, nil); err != nil {
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
	press := func(combo string) func(ctx context.Context, tab *Tab) error {
		return func(ctx context.Context, tab *Tab) error {
			if err := tab.eval(ctx, `document.getElementById('plain').focus()`, nil); err != nil {
				return err
			}
			return tab.Press(ctx, combo)
		}
	}
	const plain, blank, noop, wo, wonoop, wopop = 25, 75, 125, 175, 225, 275
	cases := []struct {
		name string
		do   func(ctx context.Context, tab *Tab) error
	}{
		{"control: left on target=_blank", click(blank, "left", 0)},
		{"control: left on rel=noopener", click(noop, "left", 0)},
		{"control: window.open", click(wo, "left", 0)},
		{"control: window.open noopener", click(wonoop, "left", 0)},
		{"control: window.open popup features", click(wopop, "left", 0)},
		{"control: left on plain link", click(plain, "left", 0)},
		{"middle", click(plain, "middle", 0)},
		{"ctrl+left", click(plain, "left", ModCtrl)},
		{"meta+left", click(plain, "left", ModMeta)},
		{"shift+left", click(plain, "left", ModShift)},
		{"alt+left", click(plain, "left", ModAlt)},
		{"shift+middle", click(plain, "middle", ModShift)},
		{"ctrl+shift+left", click(plain, "left", ModCtrl|ModShift)},
		{"meta+shift+left", click(plain, "left", ModMeta|ModShift)},
		{"ctrl+left on target=_blank", click(blank, "left", ModCtrl)},
		{"meta+left on target=_blank", click(blank, "left", ModMeta)},
		{"shift+left on target=_blank", click(blank, "left", ModShift)},
		{"ctrl+left on window.open", click(wo, "left", ModCtrl)},
		{"meta+left on window.open", click(wo, "left", ModMeta)},
		{"shift+left on window.open", click(wo, "left", ModShift)},
		{"middle on window.open button", click(wo, "middle", 0)},
		{"enter", enter(0)},
		{"ctrl+enter", enter(ModCtrl)},
		{"meta+enter", enter(ModMeta)},
		{"shift+enter", enter(ModShift)},
		{"alt+enter", enter(ModAlt)},
		{"ctrl+shift+enter", enter(ModCtrl | ModShift)},
		{"meta+shift+enter", enter(ModMeta | ModShift)},
		{"press ctrl+Enter", press("ctrl+Enter")},
		{"press cmd+Enter", press("cmd+Enter")},
		{"press shift+Enter", press("shift+Enter")},
	}
	only := os.Getenv("LC_PROBE_ONLY")
	for _, c := range cases {
		if only != "" && !strings.Contains(c.name, only) {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			m := testManager(t)
			ctx := ctxFor(t)
			tab, err := m.Tab(ctx, "conv1")
			if err != nil {
				t.Fatal(err)
			}
			if err := tab.Navigate(ctx, srv.URL); err != nil {
				t.Fatal(err)
			}
			other, err := m.Tab(ctx, "conv2")
			if err != nil {
				t.Fatal(err)
			}
			if err := other.Navigate(ctx, srv.URL+"/second"); err != nil {
				t.Fatal(err)
			}
			conn := m.proc.conn
			l := &probeLog{start: time.Now(), labels: map[string]string{tab.targetID: "T1", other.targetID: "T2"}}
			type info struct {
				TargetInfo struct {
					TargetID         string `json:"targetId"`
					Type             string `json:"type"`
					URL              string `json:"url"`
					OpenerID         string `json:"openerId"`
					OpenerFrameID    string `json:"openerFrameId"`
					CanAccessOpener  bool   `json:"canAccessOpener"`
					BrowserContextID string `json:"browserContextId"`
					Subtype          string `json:"subtype"`
				} `json:"targetInfo"`
			}
			for _, ev := range []string{"Target.targetCreated", "Target.targetInfoChanged", "Target.attachedToTarget"} {
				conn.On("", ev, func(p json.RawMessage) {
					var i info
					_ = json.Unmarshal(p, &i)
					if i.TargetInfo.Type != "page" && i.TargetInfo.Type != "tab" {
						return
					}
					l.mu.Lock()
					defer l.mu.Unlock()
					l.add("%s %s type=%s url=%q opener=%s openerFrame=%s", strings.TrimPrefix(ev, "Target."), l.label(i.TargetInfo.TargetID), i.TargetInfo.Type,
						strings.TrimPrefix(i.TargetInfo.URL, srv.URL), l.label(i.TargetInfo.OpenerID), l.label(i.TargetInfo.OpenerFrameID))
				})
			}
			conn.On("", "Target.targetDestroyed", func(p json.RawMessage) {
				var d struct {
					TargetID string `json:"targetId"`
				}
				_ = json.Unmarshal(p, &d)
				l.mu.Lock()
				defer l.mu.Unlock()
				if _, known := l.labels[d.TargetID]; known {
					l.add("targetDestroyed %s", l.label(d.TargetID))
				}
			})
			for _, ev := range []string{"Page.frameRequestedNavigation", "Page.windowOpen", "Page.frameStartedNavigating", "Page.frameScheduledNavigation", "Page.frameNavigated"} {
				conn.On(tab.session, ev, func(p json.RawMessage) {
					l.mu.Lock()
					defer l.mu.Unlock()
					s := strings.ReplaceAll(string(p), srv.URL, "")
					if len(s) > 260 {
						s = s[:260] + "..."
					}
					l.add("T1 %s %s", strings.TrimPrefix(ev, "Page."), s)
				})
			}
			l.mu.Lock()
			l.start = time.Now()
			l.mu.Unlock()
			if err := c.do(ctx, tab); err != nil {
				t.Fatal(err)
			}
			time.Sleep(2 * time.Second)

			var out struct {
				TargetInfos []struct {
					TargetID string `json:"targetId"`
					Type     string `json:"type"`
					URL      string `json:"url"`
					OpenerID string `json:"openerId"`
				} `json:"targetInfos"`
			}
			if err := conn.Call(ctx, "", "Target.getTargets", nil, &out); err != nil {
				t.Fatal(err)
			}
			var pages []string
			l.mu.Lock()
			for _, ti := range out.TargetInfos {
				if ti.Type != "page" {
					continue
				}
				var win struct {
					WindowID int `json:"windowId"`
					Bounds   struct {
						WindowState string `json:"windowState"`
					} `json:"bounds"`
				}
				l.mu.Unlock()
				werr := conn.Call(ctx, "", "Browser.getWindowForTarget", map[string]any{"targetId": ti.TargetID}, &win)
				l.mu.Lock()
				pages = append(pages, fmt.Sprintf("%s{url=%q opener=%s window=%d err=%v}", l.label(ti.TargetID), strings.TrimPrefix(ti.URL, srv.URL), l.label(ti.OpenerID), win.WindowID, werr))
			}
			lines := append([]string(nil), l.lines...)
			l.mu.Unlock()
			sort.Strings(pages)
			var state string
			_ = tab.eval(ctx, `document.visibilityState + ' ' + location.pathname`, &state)
			var tabs []string
			for _, ti := range m.Tabs(ctx, "conv1") {
				l.mu.Lock()
				s := l.label(ti.ID)
				l.mu.Unlock()
				if ti.Active {
					s += "*"
				}
				tabs = append(tabs, s)
			}
			order := ""
			for _, line := range lines {
				switch {
				case strings.Contains(line, "frameRequestedNavigation") && !strings.Contains(line, `"currentTab"`):
					order += "asked "
				case strings.Contains(line, "targetCreated") && strings.Contains(line, "opener=- "):
					order += "made "
				}
			}
			t.Logf("RESULT %s :: order[%s] T1 is %q; conv1 tabs %v; pages %s", c.name, strings.TrimSpace(order), state, tabs, strings.Join(pages, " "))
			for _, line := range lines {
				t.Logf("   EV %s", line)
			}
		})
	}
}
