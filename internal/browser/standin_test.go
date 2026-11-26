package browser

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/cdp"
)

// command is one call a stand-in browser got.
type command struct {
	Method string
	Params map[string]any
}

// devTools is a stand-in for a browser's DevTools endpoint: enough
// websocket to speak to cdp.Dial on loopback. Each command it gets goes to
// handle, on the connection's reading goroutine, with answer to reply with
// a result or, when fail is not empty, an error. handle may keep answer and
// call it later from any goroutine.
func devTools(t *testing.T, handle func(c command, answer func(result map[string]any, fail string))) *cdp.Conn {
	t.Helper()
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
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal(p, &req) != nil {
				continue
			}
			id := req.ID
			handle(command{Method: req.Method, Params: req.Params}, func(result map[string]any, fail string) {
				msg := map[string]any{"id": id, "result": result}
				if fail != "" {
					msg = map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": fail}}
				}
				resp, _ := json.Marshal(msg)
				write(resp)
			})
		}
	}))
	conn, err := cdp.Dial("ws://" + strings.TrimPrefix(srv.URL, "http://") + "/devtools/browser/stand-in")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Close()
		srv.Close()
	})
	return conn
}

// stallingBrowser is a stand-in browser that answers every call at once,
// except the method named, which it holds until release is called (or the
// test ends).
func stallingBrowser(t *testing.T, stall string) (*cdp.Conn, <-chan struct{}, func()) {
	t.Helper()
	held := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	t.Cleanup(let)
	conn := devTools(t, func(c command, answer func(map[string]any, string)) {
		if c.Method != stall {
			answer(map[string]any{}, "")
			return
		}
		held <- struct{}{}
		go func() {
			<-release
			answer(map[string]any{}, "")
		}()
	})
	return conn, held, let
}

// scriptedBrowser is a stand-in browser that holds chosen calls of a
// method (the first, the second, ...) until they are let go, fails every
// call of a method with an error, gives each new page and session an id of
// its own, and keeps every call it got.
type scriptedBrowser struct {
	conn *cdp.Conn
	held chan string // "Method#n" as each held call arrives

	mu     sync.Mutex
	calls  []command
	counts map[string]int
	ids    atomic.Int64
	// says is what the browser answers a method with, beside the ids it
	// makes up: says["Runtime.evaluate"] is what a script in a page gives.
	says map[string]map[string]any
}

// answers makes the browser answer every call of method with result.
func (sb *scriptedBrowser) answers(method string, result map[string]any) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.says == nil {
		sb.says = map[string]map[string]any{}
	}
	sb.says[method] = result
}

// newScriptedBrowser holds call n of method for each n in hold[method] and
// fails every call of a method in fail with its message. The returned let
// lets each held call go: let[method][n]().
func newScriptedBrowser(t *testing.T, fail map[string]string, hold map[string][]int) (*scriptedBrowser, map[string]map[int]func()) {
	t.Helper()
	sb := &scriptedBrowser{held: make(chan string, 16), counts: map[string]int{}}
	gates := map[string]map[int]chan struct{}{}
	let := map[string]map[int]func(){}
	for method, ns := range hold {
		gates[method] = map[int]chan struct{}{}
		let[method] = map[int]func(){}
		for _, n := range ns {
			gate := make(chan struct{})
			var once sync.Once
			gates[method][n] = gate
			let[method][n] = func() { once.Do(func() { close(gate) }) }
		}
	}
	t.Cleanup(func() {
		for _, fns := range let {
			for _, fn := range fns {
				fn()
			}
		}
	})
	sb.conn = devTools(t, func(c command, answer func(map[string]any, string)) {
		sb.mu.Lock()
		sb.calls = append(sb.calls, c)
		sb.counts[c.Method]++
		n := sb.counts[c.Method]
		said := sb.says[c.Method]
		sb.mu.Unlock()
		reply := func() {
			result := map[string]any{}
			for k, v := range said {
				result[k] = v
			}
			switch c.Method {
			case "Target.createTarget":
				result["targetId"] = fmt.Sprintf("T%d", sb.ids.Add(1))
			case "Target.attachToTarget":
				result["sessionId"] = fmt.Sprintf("S%d", sb.ids.Add(1))
			}
			answer(result, fail[c.Method])
		}
		if gate := gates[c.Method][n]; gate != nil {
			sb.held <- fmt.Sprintf("%s#%d", c.Method, n)
			go func() {
				<-gate
				reply()
			}()
			return
		}
		reply()
	})
	return sb, let
}

// waitHeld waits for the held call named "Method#n" to arrive.
func (sb *scriptedBrowser) waitHeld(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-sb.held:
		if got != want {
			t.Fatalf("the browser holds %s, want %s", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%s never reached the browser", want)
	}
}

// count is how many calls of method the browser got.
func (sb *scriptedBrowser) count(method string) int {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.counts[method]
}

// closed is the pages the browser was asked to close, in order.
func (sb *scriptedBrowser) closed() []string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	var out []string
	for _, c := range sb.calls {
		if c.Method == "Target.closeTarget" {
			if id, ok := c.Params["targetId"].(string); ok {
				out = append(out, id)
			}
		}
	}
	return out
}

// managerOn is a manager on sb with conversation c1 on tab "A".
func managerOn(sb *scriptedBrowser) (*Manager, *process) {
	p := &process{conn: sb.conn, exited: make(chan struct{})}
	m := NewManager(Options{})
	m.proc = p
	m.convs["c1"] = &convTabs{tabs: []*Tab{{conn: sb.conn, targetID: "A", session: "sA"}}, changed: make(chan struct{})}
	return m, p
}

// queuedBehind waits until conv has n pieces of work asked for.
func queuedBehind(t *testing.T, m *Manager, conv string, n uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		q := m.queues[conv]
		got := q != nil && q.next == n
		m.mu.Unlock()
		if got {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("conversation %s never had %d pieces of work queued", conv, n)
}

func contains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// waitClosed waits for the browser to be asked to close every page in ids.
func (sb *scriptedBrowser) waitClosed(t *testing.T, ids ...string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		closed := sb.closed()
		all := true
		for _, id := range ids {
			if !contains(closed, id) {
				all = false
			}
		}
		if all {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("closed %v; want %v among them", closed, ids)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
