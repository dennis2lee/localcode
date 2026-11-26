// Package cdp speaks the Chrome DevTools Protocol to a browser localcode
// started itself, over the browser's local WebSocket endpoint.
//
// The WebSocket client here is the part of RFC 6455 that conversation
// needs and no more: a client handshake, masked text frames out, text and
// binary messages in (fragmented or not), ping and close. It talks to
// 127.0.0.1 and nothing else, so there is no TLS, no proxy, no extension
// negotiation and no subprotocol. Written rather than imported because the
// default build has no WebSocket dependency and this is a few hundred
// lines against a protocol that has not changed since 2011.
package cdp

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// maxMessage bounds one incoming message. A full-page screenshot of a long
// page is the largest thing Chrome sends, base64 inside JSON; 256 MiB is
// far past any of those and still a bound.
const maxMessage = 256 << 20

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// wsConn is one client WebSocket connection.
type wsConn struct {
	conn net.Conn
	r    *bufio.Reader

	writeMu sync.Mutex
	closed  bool
}

// errClosed is returned by readMessage once the peer has closed.
var errClosed = errors.New("websocket closed")

// dialWS opens a WebSocket to a ws:// URL on the local machine.
func dialWS(rawURL string, timeout time.Duration) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("websocket: scheme %q, want ws", u.Scheme)
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		// The only endpoint this client is for is a browser this process
		// started, listening on loopback. Anything else is a mistake.
		return nil, fmt.Errorf("websocket: host %q is not loopback", host)
	}
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		return nil, err
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	path := u.RequestURI()
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	resp, err := http.ReadResponse(r, &http.Request{Method: "GET"})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake: %s", resp.Status)
	}
	if got, want := resp.Header.Get("Sec-WebSocket-Accept"), acceptKey(key); got != want {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake: accept key %q, want %q", got, want)
	}
	_ = conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, r: r}, nil
}

// acceptKey is the Sec-WebSocket-Accept a server must answer key with.
func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}

// writeText sends one text message as a single masked frame.
func (c *wsConn) writeText(p []byte) error {
	return c.writeFrame(opText, p)
}

func (c *wsConn) writeFrame(op byte, p []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return errClosed
	}
	header := make([]byte, 0, 14)
	header = append(header, 0x80|op)
	switch n := len(p); {
	case n < 126:
		header = append(header, 0x80|byte(n))
	case n <= 0xFFFF:
		header = append(header, 0x80|126, byte(n>>8), byte(n))
	default:
		header = append(header, 0x80|127)
		header = binary.BigEndian.AppendUint64(header, uint64(n))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	header = append(header, mask[:]...)
	masked := make([]byte, len(p))
	for i := range p {
		masked[i] = p[i] ^ mask[i%4]
	}
	if _, err := c.conn.Write(append(header, masked...)); err != nil {
		return err
	}
	return nil
}

// readMessage returns the next text or binary message, answering pings and
// reassembling fragments on the way. errClosed once the peer has closed.
func (c *wsConn) readMessage() ([]byte, error) {
	var msg []byte
	started := false
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			_ = c.writeFrame(opClose, payload)
			c.markClosed()
			return nil, errClosed
		case opText, opBinary:
			if started {
				return nil, errors.New("websocket: new message inside a fragmented one")
			}
			started = true
			msg = append(msg[:0], payload...)
		case opContinuation:
			if !started {
				return nil, errors.New("websocket: continuation with no message")
			}
			if len(msg)+len(payload) > maxMessage {
				return nil, errors.New("websocket: message too large")
			}
			msg = append(msg, payload...)
		default:
			return nil, fmt.Errorf("websocket: unknown opcode %#x", op)
		}
		if fin {
			return msg, nil
		}
	}
}

func (c *wsConn) readFrame() (fin bool, op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.r, h[:]); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	op = h[0] & 0x0F
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(c.r, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(c.r, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > maxMessage {
		err = errors.New("websocket: frame too large")
		return
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.r, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.r, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return
}

func (c *wsConn) markClosed() {
	c.writeMu.Lock()
	c.closed = true
	c.writeMu.Unlock()
}

// close sends a close frame and shuts the connection.
func (c *wsConn) close() error {
	_ = c.writeFrame(opClose, []byte{0x03, 0xE8}) // 1000, normal closure
	c.markClosed()
	return c.conn.Close()
}
