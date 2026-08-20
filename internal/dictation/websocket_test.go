package dictation

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// upgrade completes the server half of the handshake and returns the
// connection as a wsConn.
//
// The same type as the client uses: a server is not supposed to mask its
// frames and this one does, which is legal-adjacent rather than legal —
// but it is exactly the case the reader is written to tolerate, so the
// test exercises that too rather than pretending it cannot happen.
func upgrade(t *testing.T, w http.ResponseWriter, r *http.Request) *wsConn {
	t.Helper()
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Fatal("test server does not support hijacking")
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + wsGUID))
	fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\n"+
		"Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(sum[:]))
	return &wsConn{conn: conn, br: buf.Reader}
}

// wsAddr turns an httptest server's URL into a ws:// one.
func wsAddr(srv *httptest.Server, path string) string {
	return "ws://" + strings.TrimPrefix(srv.URL, "http://") + path
}

func TestMessagesOfEverySizeSurviveTheWire(t *testing.T) {
	t.Parallel()
	// Three lengths, because the length is encoded three different ways:
	// in the header byte, in two extra bytes, and in eight.
	sizes := []int{5, 200, 70000}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := upgrade(t, w, r)
		defer c.Close()
		for range sizes {
			op, msg, err := c.read(time.Now().Add(5 * time.Second))
			if err != nil {
				return
			}
			if err := c.write(op, msg); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := dialWebSocket(ctx, wsAddr(srv, "/"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, n := range sizes {
		sent := strings.Repeat("가", n) // multi-byte, so a lazy length is caught
		if err := c.writeText(sent); err != nil {
			t.Fatalf("%d: %v", n, err)
		}
		op, got, err := c.read(time.Now().Add(5 * time.Second))
		if err != nil {
			t.Fatalf("%d: %v", n, err)
		}
		if op != opText {
			t.Errorf("%d: opcode = %d, want text", n, op)
		}
		if string(got) != sent {
			t.Errorf("%d: message came back changed (%d bytes, want %d)", n, len(got), len(sent))
		}
	}
}

func TestAPingIsAnsweredWithoutDisturbingTheTranscript(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := upgrade(t, w, r)
		defer c.Close()
		c.write(opPing, []byte("still there?"))
		// The pong has to arrive before the client is given anything else
		// to read, or the client's reader has swallowed a frame it should
		// have answered.
		// readFrame, not read: read answers control frames and moves on,
		// which is the behaviour under test on the other side.
		c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		op, _, msg, err := c.readFrame()
		if err != nil || op != opPong || string(msg) != "still there?" {
			c.writeText("no pong")
			return
		}
		c.writeText("hello")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := dialWebSocket(ctx, wsAddr(srv, "/"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, msg, err := c.read(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if string(msg) != "hello" {
		t.Errorf("got %q, want hello — the ping was not answered", msg)
	}
}

func TestAFragmentedMessageIsReadAsOne(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := upgrade(t, w, r)
		defer c.Close()
		// Hand-built, because wsConn only ever sends whole messages: a
		// server is entitled to split one and this side has to cope.
		c.conn.Write([]byte{0x01, 0x05}) // text, no FIN, "first"
		c.conn.Write([]byte("first"))
		c.conn.Write([]byte{0x80, 0x06}) // continuation, FIN, "second"
		c.conn.Write([]byte("second"))
		time.Sleep(50 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := dialWebSocket(ctx, wsAddr(srv, "/"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	op, msg, err := c.read(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if op != opText || string(msg) != "firstsecond" {
		t.Errorf("got %q, want firstsecond", msg)
	}
}

func TestAnOrdinaryHTTPServerIsNotMistakenForAWebSocket(t *testing.T) {
	t.Parallel()
	// What a whisper.cpp server looks like from here: it answers, with a
	// page, and that has to read as "no streaming endpoint" rather than
	// as a broken connection — the difference between falling back to
	// HTTP and telling the user their speech server is down.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := dialWebSocket(ctx, wsAddr(srv, "/"))
	if err == nil {
		t.Fatal("dialed a plain HTTP server as a websocket")
	}
	if !isNotWebSocket(err) {
		t.Errorf("error %v is not recognised as an HTTP-only server", err)
	}
}

func TestAServerThatEchoesTheKeyWithoutHashingItIsRejected(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj := w.(http.Hijacker)
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			r.Header.Get("Sec-WebSocket-Key"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dialWebSocket(ctx, wsAddr(srv, "/")); err == nil {
		t.Fatal("accepted a handshake that did not answer the key")
	}
}
