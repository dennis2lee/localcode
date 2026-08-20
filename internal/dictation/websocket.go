package dictation

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// A WebSocket client, in about as few lines as RFC 6455 allows.
//
// Written here rather than taken from a library because of what this
// program is: one static binary that people download, with a dependency
// list that is read before it is trusted. A speech server on the LAN is
// not a reason to add a networking dependency to every build of it, and
// the part of the protocol dictation needs is small and fixed — one
// connection, binary frames out, text frames in, no extensions, no
// subprotocols, no compression.
//
// What is deliberately not here: fragmentation on the sending side (a
// chunk of audio is one frame), permessage-deflate (PCM does not
// compress and the answers are a few hundred bytes), and any server
// side at all. Fragmented *incoming* messages are reassembled, because
// a server is entitled to send them.

// wsGUID is the constant RFC 6455 appends to the client key before
// hashing, so a server proves it read the key rather than echoing it.
const wsGUID = "258EAFA5-E914-47DA-95CA-5AB0DC85B39A"

// WebSocket opcodes.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// wsMaxMessage caps one incoming message.
//
// A transcript of a sentence is a few hundred bytes. The cap is here
// because the length is whatever the other end says it is: without one,
// a server that claims a 2GB frame is an out-of-memory in this process,
// and "the speech server on the LAN" is not a reason to trust a length
// field.
const wsMaxMessage = 1 << 20

// wsConn is one WebSocket connection. Writes are serialized; reads are
// expected to happen on a single goroutine, which is how streamSession
// uses it.
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader

	wmu     sync.Mutex
	closed  bool
	closing bool
}

// dialWebSocket opens a WebSocket to rawURL, which must be ws:// or
// wss://. The context bounds the whole handshake, connect included.
func dialWebSocket(ctx context.Context, rawURL string) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("bad websocket address %q: %w", rawURL, err)
	}
	secure := false
	switch u.Scheme {
	case "ws":
	case "wss":
		secure = true
	default:
		return nil, fmt.Errorf("bad websocket address %q: want ws:// or wss://", rawURL)
	}
	host := u.Host
	if u.Port() == "" {
		if secure {
			host = net.JoinHostPort(host, "443")
		} else {
			host = net.JoinHostPort(host, "80")
		}
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if secure {
		tconn := tls.Client(conn, &tls.Config{ServerName: u.Hostname()})
		if err := tconn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tconn
	}

	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		conn.Close()
		return nil, err
	}
	nonce := base64.StdEncoding.EncodeToString(key)

	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + nonce + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		// The status is the whole diagnosis here: a 404 means this
		// server has no such endpoint, a 200 means it answered the
		// upgrade with an ordinary page, and both are "not a streaming
		// server" rather than a failure worth retrying.
		return nil, &notWebSocketError{status: resp.StatusCode, url: rawURL}
	}
	sum := sha1.Sum([]byte(nonce + wsGUID))
	if want := base64.StdEncoding.EncodeToString(sum[:]); resp.Header.Get("Sec-WebSocket-Accept") != want {
		conn.Close()
		return nil, &notWebSocketError{status: resp.StatusCode, url: rawURL,
			why: "the server accepted the upgrade without answering the key"}
	}
	// The handshake deadline must not outlive the handshake: reads after
	// this point are bounded per call by the caller.
	conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, br: br}, nil
}

// notWebSocketError says the address answered, but not as a WebSocket.
// Told apart from a transport failure because it is what a perfectly
// healthy HTTP-only speech server looks like, and the answer to it is to
// go on using HTTP rather than to report a fault.
type notWebSocketError struct {
	status int
	url    string
	why    string
}

func (e *notWebSocketError) Error() string {
	if e.why != "" {
		return fmt.Sprintf("%s is not a websocket endpoint: %s", e.url, e.why)
	}
	return fmt.Sprintf("%s is not a websocket endpoint (HTTP %d)", e.url, e.status)
}

func isNotWebSocket(err error) bool {
	var e *notWebSocketError
	return errors.As(err, &e)
}

// writeText and writeBinary send one whole message.
func (c *wsConn) writeText(s string) error   { return c.write(opText, []byte(s)) }
func (c *wsConn) writeBinary(b []byte) error { return c.write(opBinary, b) }

func (c *wsConn) write(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return net.ErrClosed
	}

	var head []byte
	head = append(head, 0x80|opcode) // FIN, one frame per message.
	n := len(payload)
	switch {
	case n < 126:
		head = append(head, byte(0x80|n))
	case n <= 0xFFFF:
		head = append(head, 0x80|126, byte(n>>8), byte(n))
	default:
		head = append(head, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		head = append(head, ext[:]...)
	}
	// Every client frame is masked. Not for secrecy — the key travels
	// with the frame — but because proxies drop frames that are not.
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	head = append(head, mask[:]...)

	body := make([]byte, n)
	for i := range payload {
		body[i] = payload[i] ^ mask[i%4]
	}
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.conn.Write(head); err != nil {
		return err
	}
	_, err := c.conn.Write(body)
	return err
}

// read returns the next data message, answering pings on the way.
// deadline bounds the wait; zero means wait indefinitely.
func (c *wsConn) read(deadline time.Time) (opcode byte, payload []byte, err error) {
	c.conn.SetReadDeadline(deadline)
	var (
		msg      []byte
		msgOp    byte
		assembly bool
	)
	for {
		op, fin, frame, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case opPing:
			if err := c.write(opPong, frame); err != nil {
				return 0, nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			c.Close()
			return 0, nil, io.EOF
		case opContinuation:
			if !assembly {
				return 0, nil, errors.New("websocket: continuation frame with nothing to continue")
			}
		case opText, opBinary:
			if assembly {
				return 0, nil, errors.New("websocket: new message before the last one finished")
			}
			assembly, msgOp = true, op
		default:
			return 0, nil, fmt.Errorf("websocket: unknown opcode %d", op)
		}
		msg = append(msg, frame...)
		if len(msg) > wsMaxMessage {
			return 0, nil, fmt.Errorf("websocket: message over %d bytes", wsMaxMessage)
		}
		if fin {
			return msgOp, msg, nil
		}
	}
}

func (c *wsConn) readFrame() (opcode byte, fin bool, payload []byte, err error) {
	var head [2]byte
	if _, err := io.ReadFull(c.br, head[:]); err != nil {
		return 0, false, nil, err
	}
	fin = head[0]&0x80 != 0
	opcode = head[0] & 0x0F
	masked := head[1]&0x80 != 0
	n := uint64(head[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, false, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, false, nil, err
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	if n > wsMaxMessage {
		return 0, false, nil, fmt.Errorf("websocket: frame of %d bytes", n)
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return 0, false, nil, err
		}
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return 0, false, nil, err
	}
	// A server is not supposed to mask, but unmasking one that does
	// costs four lines and is the difference between reading its
	// transcript and reporting the connection as broken.
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, fin, payload, nil
}

// Close ends the connection, sending a close frame first so the server
// sees a hangup rather than a reset.
func (c *wsConn) Close() error {
	c.wmu.Lock()
	if c.closing {
		c.wmu.Unlock()
		return nil
	}
	c.closing = true
	c.wmu.Unlock()

	// Best effort, and before the socket is marked closed so the frame
	// can still go out: 1000, "normal closure". A server that never sees
	// it loses nothing but the reason.
	c.write(opClose, []byte{0x03, 0xE8})

	c.wmu.Lock()
	c.closed = true
	c.wmu.Unlock()
	return c.conn.Close()
}

// wsURL turns the host:port and scheme this package already normalizes
// into a WebSocket address for one path.
func wsURL(scheme, host, path string) string {
	ws := "ws"
	if scheme == "https" || scheme == "wss" {
		ws = "wss"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return ws + "://" + host + path
}
