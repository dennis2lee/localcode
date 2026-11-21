package browser

// Measurement only. Never committed.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestZZProbePopup(t *testing.T) {
	if os.Getenv("LC_PROBE") == "" {
		t.Skip("probe")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/second" {
			fmt.Fprint(w, `<!doctype html><title>Second</title><p>second</p>`)
			return
		}
		fmt.Fprint(w, `<!doctype html><title>Links</title><style>a,button{position:absolute;left:10px;width:200px;height:30px;display:block}</style>
<a href="/second" style="top:10px">plain</a>
<a href="/second" target="_blank" style="top:60px">blank</a>
<button onclick="window.open('/','_blank','width=300,height=200')" style="top:110px">popup</button>
<button onclick="window.open('/second')" style="top:160px">open tab</button>`)
	}))
	t.Cleanup(srv.Close)
	win := func(ctx context.Context, m *Manager, id string) int {
		var w struct {
			WindowID int `json:"windowId"`
		}
		_ = m.proc.conn.Call(ctx, "", "Browser.getWindowForTarget", map[string]any{"targetId": id}, &w)
		return w.WindowID
	}
	click := func(ctx context.Context, tab *Tab, y float64, mods int) {
		for _, typ := range []string{"mousePressed", "mouseReleased"} {
			if err := tab.Mouse(ctx, MouseEvent{Type: typ, X: 40, Y: y, Button: "left", ClickCount: 1, Modifiers: mods}); err != nil {
				t.Fatal(err)
			}
		}
	}
	key, _ := newTabKey()
	for _, v := range []string{"nothing", "other conversation made first", "activate own tab", "bringToFront own tab", "click in own tab", "activate the popup", "activate other then own", "activate own, then blank link with key", "window.open tab from popup"} {
		t.Run(v, func(t *testing.T) {
			m := testManager(t)
			ctx := ctxFor(t)
			var t1, t2 *Tab
			var err error
			open := func(conv string) *Tab {
				tab, err := m.Tab(ctx, conv)
				if err != nil {
					t.Fatal(err)
				}
				if err := tab.Navigate(ctx, srv.URL); err != nil {
					t.Fatal(err)
				}
				return tab
			}
			if v == "other conversation made first" {
				t2 = open("c2")
				t1 = open("c1")
			} else {
				t1 = open("c1")
			}
			click(ctx, t1, 125, 0)
			var pop *Tab
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
				if cur := m.Existing("c1"); cur != nil && cur != t1 {
					pop = cur
					break
				}
			}
			if pop == nil {
				t.Fatal("no popup")
			}
			pop.waitLoaded(ctx, 10*time.Second)
			if t2 == nil {
				t2 = open("c2")
			}
			var bars string
			_ = pop.eval(ctx, `[toolbar.visible, menubar.visible, locationbar.visible, innerWidth, outerWidth].join(',')`, &bars)
			var bars1 string
			_ = t1.eval(ctx, `[toolbar.visible, menubar.visible, locationbar.visible, innerWidth, outerWidth].join(',')`, &bars1)
			conn := m.proc.conn
			y, mods := 75.0, 0
			switch v {
			case "activate own tab":
				err = conn.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": t1.targetID}, nil)
			case "bringToFront own tab":
				err = conn.Call(ctx, t1.session, "Page.bringToFront", nil, nil)
			case "click in own tab":
				click(ctx, t1, 400, 0)
			case "activate the popup":
				err = conn.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": pop.targetID}, nil)
			case "activate other then own":
				_ = conn.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": t2.targetID}, nil)
				err = conn.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": t1.targetID}, nil)
			case "activate own, then blank link with key":
				err = conn.Call(ctx, "", "Target.activateTarget", map[string]any{"targetId": t1.targetID}, nil)
				y, mods = 25, key
			case "window.open tab from popup":
				y = 175
			}
			if err != nil {
				t.Logf("steer: %v", err)
			}
			before := map[string]bool{}
			var out struct {
				TargetInfos []struct {
					TargetID string `json:"targetId"`
					Type     string `json:"type"`
					URL      string `json:"url"`
				} `json:"targetInfos"`
			}
			_ = conn.Call(ctx, "", "Target.getTargets", nil, &out)
			for _, ti := range out.TargetInfos {
				before[ti.TargetID] = true
			}
			click(ctx, pop, y, mods)
			time.Sleep(1500 * time.Millisecond)
			_ = conn.Call(ctx, "", "Target.getTargets", nil, &out)
			where := "no new page"
			w1, w2, wp := win(ctx, m, t1.targetID), win(ctx, m, t2.targetID), win(ctx, m, pop.targetID)
			for _, ti := range out.TargetInfos {
				if ti.Type == "page" && !before[ti.TargetID] {
					w := win(ctx, m, ti.TargetID)
					switch w {
					case w1:
						where = "in its own conversation's tabbed window"
					case w2:
						where = "in the OTHER conversation's window"
					case wp:
						where = "in the popup's window"
					default:
						where = "in a new window"
					}
				}
			}
			var s1, s2 string
			_ = t1.eval(ctx, `document.visibilityState`, &s1)
			_ = t2.eval(ctx, `document.visibilityState`, &s2)
			t.Logf("RESULT %-40s :: new page %s; own tab %s, other's tab %s; popup bars[%s] tab bars[%s]", v, where, s1, s2, bars, bars1)
		})
	}
}
