package browser

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"localcode/internal/cdp"
)

// stallingBrowser is a stand-in for a browser's DevTools endpoint that
// answers every call at once, except the method named, which it holds
// until the test ends. Enough websocket to speak to cdp.Dial on loopback.
func stallingBrowser(t *testing.T, stall string) (*cdp.Conn, <-chan struct{}) {
	t.Helper()
	held := make(chan struct{}, 4)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
			base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		rw.Flush()
		var mu sync.Mutex
		write := func(p []byte) {
			hdr := []byte{0x81}
			if len(p) < 126 {
				hdr = append(hdr, byte(len(p)))
			} else {
				hdr = append(hdr, 126, byte(len(p)>>8), byte(len(p)))
			}
			mu.Lock()
			defer mu.Unlock()
			_, _ = conn.Write(append(hdr, p...))
		}
		for {
			var b [2]byte
			if _, err := io.ReadFull(rw, b[:]); err != nil {
				return
			}
			n := int(b[1] & 0x7F)
			if n == 126 {
				var ext [2]byte
				if _, err := io.ReadFull(rw, ext[:]); err != nil {
					return
				}
				n = int(binary.BigEndian.Uint16(ext[:]))
			}
			var mask [4]byte
			if _, err := io.ReadFull(rw, mask[:]); err != nil {
				return
			}
			p := make([]byte, n)
			if _, err := io.ReadFull(rw, p); err != nil {
				return
			}
			for i := range p {
				p[i] ^= mask[i%4]
			}
			if b[0]&0x0F == 8 {
				return
			}
			var req struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			if json.Unmarshal(p, &req) != nil {
				continue
			}
			answer := func() {
				resp, _ := json.Marshal(map[string]any{"id": req.ID, "result": map[string]any{}})
				write(resp)
			}
			if req.Method == stall {
				held <- struct{}{}
				go func() {
					<-release
					answer()
				}()
				continue
			}
			answer()
		}
	}))
	conn, err := cdp.Dial("ws://" + strings.TrimPrefix(srv.URL, "http://") + "/devtools/browser/stand-in")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(release)
		conn.Close()
		srv.Close()
	})
	return conn, held
}

// prompt fails the test if f takes longer than a lock held for no reason
// would let it.
func prompt(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Errorf("%s waited on another conversation's call to the browser", what)
	}
}

// One conversation waiting on the browser holds no other conversation
// up: the manager's lock is not held across a call to the browser.
func TestASlowCallToTheBrowserHoldsNoOtherConversation(t *testing.T) {
	for _, tc := range []struct {
		name, stall string
		slow        func(m *Manager, p *process)
	}{
		{"a link's tab attaching", "Target.attachToTarget", func(m *Manager, p *process) { m.adopt(p, "opener", "popup") }},
		{"a new tab opening", "Target.createTarget", func(m *Manager, p *process) { m.NewTab(context.Background(), "c1") }},
		{"a first tab opening", "Target.createTarget", func(m *Manager, p *process) { m.Tab(context.Background(), "c3") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, held := stallingBrowser(t, tc.stall)
			p := &process{conn: conn, exited: make(chan struct{})}
			m := NewManager(Options{})
			m.proc = p
			m.convs["c1"] = &convTabs{tabs: []*Tab{{conn: conn, targetID: "opener", session: "s1"}}, changed: make(chan struct{})}
			other := &Tab{conn: conn, targetID: "other", session: "s2"}
			m.convs["c2"] = &convTabs{tabs: []*Tab{other}, changed: make(chan struct{})}
			go tc.slow(m, p)
			select {
			case <-held:
			case <-time.After(3 * time.Second):
				t.Fatalf("the slow call never reached %s", tc.stall)
			}
			prompt(t, "Existing", func() {
				if m.Existing("c2") != other {
					t.Error("the other conversation's tab is not its own")
				}
			})
			prompt(t, "Tabs", func() { m.Tabs(context.Background(), "c2") })
			prompt(t, "ActivateTab", func() { _ = m.ActivateTab("c2", "other") })
		})
	}
}

// Two calls for the same conversation's first tab at once open one tab
// for it: the second to finish closes its own and returns the first's.
func TestTwoFirstTabsAtOnceAreOne(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	tabs := make(chan *Tab, 2)
	for i := 0; i < 2; i++ {
		go func() {
			tab, err := m.Tab(ctx, "c")
			if err != nil {
				t.Error(err)
			}
			tabs <- tab
		}()
	}
	a, b := <-tabs, <-tabs
	if a == nil || a != b {
		t.Fatalf("two calls returned two tabs: %p and %p", a, b)
	}
	if got := m.Tabs(ctx, "c"); len(got) != 1 {
		t.Errorf("the conversation has %d tabs", len(got))
	}
}
