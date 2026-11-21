package browser

// Measurement only. Never committed.

import (
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

// What URL the page asks for, and what URL the page the browser makes for
// it has when it is created: the same string or not.
func TestZZProbeStray3(t *testing.T) {
	if os.Getenv("LC_PROBE") == "" {
		t.Skip("probe")
	}
	var port string
	hrefs := []string{
		"/second#frag",
		"/second?q=한글&x=a b",
		"/redir",
		"/a/../second",
		"http://LOCALHOST:PORT/second",
		"/second?" + strings.Repeat("x", 3000),
		"javascript:void(0)",
		"mailto:someone@example.invalid",
		"/file.bin",
		"about:blank",
		"about:blank#x",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redir":
			http.Redirect(w, r, "/second?redirected", http.StatusFound)
		case "/file.bin":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", "attachment; filename=file.bin")
			fmt.Fprint(w, "data")
		case "/second":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<!doctype html><title>Second</title><p>second</p>`)
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<!doctype html><title>Links</title><style>a{position:absolute;left:10px;width:200px;height:20px;display:block;overflow:hidden}</style>`)
			for i, h := range hrefs {
				fmt.Fprintf(w, `<a href="%s" style="top:%dpx">link %d</a>`, strings.ReplaceAll(h, "PORT", port), 10+30*i, i)
			}
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port = u.Port()
	mod := ModCtrl
	if runtime.GOOS == "darwin" {
		mod = ModMeta
	}
	for _, kind := range []string{"tab", "window"} {
		for i, h := range hrefs {
			t.Run(fmt.Sprintf("%s/%d", kind, i), func(t *testing.T) {
				m := testManager(t)
				ctx := ctxFor(t)
				tab, err := m.Tab(ctx, "conv1")
				if err != nil {
					t.Fatal(err)
				}
				if err := tab.Navigate(ctx, srv.URL); err != nil {
					t.Fatal(err)
				}
				conn := m.proc.conn
				var mu sync.Mutex
				var asked, made []string
				var order []string
				conn.On(tab.session, "Page.frameRequestedNavigation", func(p json.RawMessage) {
					var ev struct {
						URL         string `json:"url"`
						Disposition string `json:"disposition"`
					}
					_ = json.Unmarshal(p, &ev)
					mu.Lock()
					asked = append(asked, ev.Disposition+" "+ev.URL)
					order = append(order, "asked")
					mu.Unlock()
				})
				conn.On("", "Target.targetCreated", func(p json.RawMessage) {
					var ev struct {
						TargetInfo struct {
							Type     string `json:"type"`
							URL      string `json:"url"`
							OpenerID string `json:"openerId"`
						} `json:"targetInfo"`
					}
					_ = json.Unmarshal(p, &ev)
					if ev.TargetInfo.Type != "page" {
						return
					}
					mu.Lock()
					made = append(made, fmt.Sprintf("opener=%v %s", ev.TargetInfo.OpenerID != "", ev.TargetInfo.URL))
					order = append(order, "made")
					mu.Unlock()
				})
				mods := mod
				if kind == "window" {
					mods = ModShift
				}
				if err := probeClick(ctx, tab, float64(20+30*i), "left", mods); err != nil {
					t.Fatal(err)
				}
				time.Sleep(1500 * time.Millisecond)
				mu.Lock()
				defer mu.Unlock()
				same := "n/a"
				if len(asked) > 0 && len(made) > 0 {
					a := asked[0][strings.Index(asked[0], " ")+1:]
					b := made[0][strings.Index(made[0], " ")+1:]
					same = fmt.Sprint(a == b)
				}
				short := func(ss []string) []string {
					var out []string
					for _, s := range ss {
						s = strings.ReplaceAll(s, srv.URL, "")
						if len(s) > 90 {
							s = s[:90] + fmt.Sprintf("...(%d)", len(s))
						}
						out = append(out, s)
					}
					return out
				}
				t.Logf("RESULT url/%s/%d href=%.40q :: same=%s order=%v asked=%q made=%q pages=%d", kind, i, h, same, order, short(asked), short(made), pagesOpen(t, ctx, m))
			})
		}
	}
}
