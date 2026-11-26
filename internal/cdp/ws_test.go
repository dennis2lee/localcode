package cdp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// wsServer is the server half, enough to test the client against: it
// upgrades, then hands each connection to serve with frame helpers.
type serverConn struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

// writeFrame sends an unmasked frame, as a server does.
func (s *serverConn) writeFrame(fin bool, op byte, p []byte) {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	h := []byte{b0}
	switch n := len(p); {
	case n < 126:
		h = append(h, byte(n))
	case n <= 0xFFFF:
		h = append(h, 126, byte(n>>8), byte(n))
	default:
		h = append(h, 127)
		h = binary.BigEndian.AppendUint64(h, uint64(n))
	}
	if _, err := s.conn.Write(append(h, p...)); err != nil {
		s.t.Errorf("server write: %v", err)
	}
}

// readFrame reads one client frame and checks that it was masked.
func (s *serverConn) readFrame() (op byte, p []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(s.r, h[:]); err != nil {
		return
	}
	op = h[0] & 0x0F
	if h[1]&0x80 == 0 {
		s.t.Error("client frame was not masked")
	}
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		io.ReadFull(s.r, b[:])
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		io.ReadFull(s.r, b[:])
		n = binary.BigEndian.Uint64(b[:])
	}
	var mask [4]byte
	io.ReadFull(s.r, mask[:])
	p = make([]byte, n)
	if _, err = io.ReadFull(s.r, p); err != nil {
		return
	}
	for i := range p {
		p[i] ^= mask[i%4]
	}
	return
}

func newWSServer(t *testing.T, serve func(*serverConn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" || r.Header.Get("Sec-WebSocket-Version") != "13" {
			http.Error(w, "not a websocket request", http.StatusBadRequest)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		accept := acceptKey(r.Header.Get("Sec-WebSocket-Key"))
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
		rw.Flush()
		serve(&serverConn{t: t, conn: conn, r: rw.Reader})
	}))
	t.Cleanup(srv.Close)
	return "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/devtools/browser/x"
}

func TestAcceptKeyIsTheRFCExample(t *testing.T) {
	// RFC 6455 section 1.3.
	if got := acceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Errorf("acceptKey = %q", got)
	}
}

func TestDialRefusesAnythingButLoopback(t *testing.T) {
	for _, u := range []string{"ws://example.com/x", "wss://127.0.0.1/x", "http://127.0.0.1/x", "ws://10.0.0.1:9222/x"} {
		if _, err := dialWS(u, time.Second); err == nil {
			t.Errorf("dialWS(%q) succeeded; only a loopback ws:// URL is a browser this process started", u)
		}
	}
}

func TestMessagesBothWays(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 70000) // the 64-bit length form
	mid := bytes.Repeat([]byte("y"), 300)   // the 16-bit form
	url := newWSServer(t, func(s *serverConn) {
		for i := 0; i < 3; i++ {
			op, p, err := s.readFrame()
			if err != nil || op != opText {
				t.Errorf("server read %d: op %#x err %v", i, op, err)
				return
			}
			s.writeFrame(true, opText, p) // echo
		}
		// A ping between the fragments of a message: the client answers it
		// and still reassembles the message.
		s.writeFrame(false, opText, []byte("frag-"))
		s.writeFrame(true, opPing, []byte("hi"))
		s.writeFrame(true, opContinuation, []byte("ment"))
		if op, p, _ := s.readFrame(); op != opPong || string(p) != "hi" {
			t.Errorf("pong: op %#x payload %q", op, p)
		}
		s.writeFrame(true, opClose, []byte{0x03, 0xE8})
	})
	c, err := dialWS(url, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	for _, p := range [][]byte{[]byte("small"), mid, big} {
		if err := c.writeText(p); err != nil {
			t.Fatal(err)
		}
		got, err := c.readMessage()
		if err != nil || !bytes.Equal(got, p) {
			t.Fatalf("echo of %d bytes: got %d bytes, err %v", len(p), len(got), err)
		}
	}
	if got, err := c.readMessage(); err != nil || string(got) != "frag-ment" {
		t.Fatalf("fragmented message = %q, %v", got, err)
	}
	if _, err := c.readMessage(); !errors.Is(err, errClosed) {
		t.Fatalf("after close: %v, want errClosed", err)
	}
	if err := c.writeText([]byte("late")); !errors.Is(err, errClosed) {
		t.Errorf("write after close: %v, want errClosed", err)
	}
}

func TestAWrongAcceptKeyIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, _ := w.(http.Hijacker).Hijack()
		defer conn.Close()
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: wrong\r\n\r\n")
		rw.Flush()
	}))
	defer srv.Close()
	if _, err := dialWS("ws://"+strings.TrimPrefix(srv.URL, "http://")+"/", 5*time.Second); err == nil {
		t.Error("a handshake with the wrong accept key was accepted")
	}
}

// A command gets its own reply, an error reply is an *Error naming the
// method, and events reach the handler for their session and method only.
func TestCallsAndEvents(t *testing.T) {
	url := newWSServer(t, func(s *serverConn) {
		for {
			op, p, err := s.readFrame()
			if err != nil || op == opClose {
				return
			}
			var m struct {
				ID        int64  `json:"id"`
				Method    string `json:"method"`
				SessionID string `json:"sessionId"`
			}
			json.Unmarshal(p, &m)
			switch m.Method {
			case "Page.navigate":
				// An event for another session first, then this one's, then
				// the reply.
				s.writeFrame(true, opText, []byte(`{"method":"Page.loadEventFired","sessionId":"other","params":{"n":1}}`))
				s.writeFrame(true, opText, []byte(`{"method":"Page.loadEventFired","sessionId":"`+m.SessionID+`","params":{"n":2}}`))
				out, _ := json.Marshal(map[string]any{"id": m.ID, "sessionId": m.SessionID, "result": map[string]string{"frameId": "f1"}})
				s.writeFrame(true, opText, out)
			case "Bad.method":
				out, _ := json.Marshal(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "not found"}})
				s.writeFrame(true, opText, out)
			}
		}
	})
	c, err := Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got := make(chan int, 4)
	c.On("s1", "Page.loadEventFired", func(p json.RawMessage) {
		var v struct{ N int }
		json.Unmarshal(p, &v)
		got <- v.N
	})
	var res struct{ FrameID string }
	if err := c.Call(context.Background(), "s1", "Page.navigate", map[string]string{"url": "about:blank"}, &res); err != nil {
		t.Fatal(err)
	}
	if res.FrameID != "f1" {
		t.Errorf("result = %+v", res)
	}
	select {
	case n := <-got:
		if n != 2 {
			t.Errorf("handler got the event for session %d's page; want only s1's", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the event never reached its handler")
	}
	err = c.Call(context.Background(), "", "Bad.method", nil, nil)
	var cdpErr *Error
	if !errors.As(err, &cdpErr) || cdpErr.Method != "Bad.method" || cdpErr.Code != -32601 {
		t.Errorf("error reply = %v, want *Error for Bad.method", err)
	}
}

// A call waiting on a connection that ends returns, rather than hanging.
func TestACallReturnsWhenTheConnectionEnds(t *testing.T) {
	url := newWSServer(t, func(s *serverConn) {
		s.readFrame()
		// Never answers; closes the TCP connection.
	})
	c, err := Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Call(context.Background(), "", "Browser.getVersion", nil, nil) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the call succeeded with no reply")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call hung after the connection ended")
	}
	<-c.Done()
	if c.Err() == nil {
		t.Error("Err is nil after the connection ended")
	}
}
