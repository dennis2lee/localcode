package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Conn is one DevTools connection to a browser. Commands to the browser
// itself use session ""; commands to a page use the session id
// Target.attachToTarget returned for it (flat sessions, one socket).
type Conn struct {
	ws *wsConn

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]pendingCall
	handlers map[string][]func(json.RawMessage) // key: sessionID + "\x00" + method
	err      error
	done     chan struct{}
}

type pendingCall struct {
	method string
	ch     chan reply
}

type reply struct {
	result json.RawMessage
	err    error
}

// Error is an error the browser answered a command with.
type Error struct {
	Method  string
	Code    int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s (%d)", e.Method, e.Message, e.Code)
}

// Dial connects to a browser's DevTools WebSocket URL.
func Dial(wsURL string) (*Conn, error) {
	ws, err := dialWS(wsURL, 10*time.Second)
	if err != nil {
		return nil, err
	}
	c := &Conn{
		ws:       ws,
		pending:  map[int64]pendingCall{},
		handlers: map[string][]func(json.RawMessage){},
		done:     make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Done is closed when the connection has ended; Err then says why.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is why the connection ended, nil while it is open.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection.
func (c *Conn) Close() error { return c.ws.close() }

// On registers fn for every event named method on session ("" for the
// browser). Handlers run on the reading goroutine, so they must not block
// and must not call Call.
func (c *Conn) On(session, method string, fn func(params json.RawMessage)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := session + "\x00" + method
	c.handlers[key] = append(c.handlers[key], fn)
}

// HandlerCount is how many event handlers are registered, over every
// session: what a test watches to see handlers go with their targets.
func (c *Conn) HandlerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, fns := range c.handlers {
		n += len(fns)
	}
	return n
}

// Forget drops every handler registered for session, for a page that is
// gone.
func (c *Conn) Forget(session string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := session + "\x00"
	for key := range c.handlers {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(c.handlers, key)
		}
	}
}

// Go sends method with params to session now, and returns a channel that
// gets the answer (nil, or the error) later. Commands sent with Go reach
// the browser in the order they were sent, which is what a target paused
// before its first script needs: its setup is answered only once it runs,
// so the setup and the command that lets it run go out together, and the
// answers are collected after.
func (c *Conn) Go(ctx context.Context, session, method string, params any) <-chan error {
	done := make(chan error, 1)
	ch, id, err := c.send(session, method, params)
	if err != nil {
		done <- err
		return done
	}
	go func() {
		done <- c.await(ctx, id, ch, nil)
	}()
	return done
}

// Call sends method with params to session and decodes the result into out
// (which may be nil). It returns when the browser answers, ctx ends, or the
// connection does.
func (c *Conn) Call(ctx context.Context, session, method string, params, out any) error {
	ch, id, err := c.send(session, method, params)
	if err != nil {
		return err
	}
	return c.await(ctx, id, ch, out)
}

// send writes one command and registers for its answer.
func (c *Conn) send(session, method string, params any) (chan reply, int64, error) {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, 0, err
	}
	c.nextID++
	id := c.nextID
	ch := make(chan reply, 1)
	c.pending[id] = pendingCall{method: method, ch: ch}
	// Written while the lock is held, so two sends reach the browser in
	// the order they were made.
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if session != "" {
		msg["sessionId"] = session
	}
	data, err := json.Marshal(msg)
	if err == nil {
		err = c.ws.writeText(data)
	}
	c.mu.Unlock()
	if err != nil {
		c.drop(id)
		return nil, 0, err
	}
	return ch, id, nil
}

// await waits for the answer to command id.
func (c *Conn) await(ctx context.Context, id int64, ch chan reply, out any) error {
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if out != nil && len(r.result) > 0 {
			return json.Unmarshal(r.result, out)
		}
		return nil
	case <-ctx.Done():
		c.drop(id)
		return ctx.Err()
	case <-c.done:
		return c.Err()
	}
}

func (c *Conn) drop(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Conn) readLoop() {
	var err error
	defer func() {
		c.mu.Lock()
		if err == nil || errors.Is(err, errClosed) {
			err = errors.New("browser connection closed")
		}
		c.err = err
		c.pending = map[int64]pendingCall{}
		c.mu.Unlock()
		close(c.done)
	}()
	for {
		var data []byte
		data, err = c.ws.readMessage()
		if err != nil {
			return
		}
		var msg struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Session string `json:"sessionId"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.ID != 0 {
			c.mu.Lock()
			call, ok := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if !ok {
				continue
			}
			if msg.Error != nil {
				call.ch <- reply{err: &Error{Method: call.method, Code: msg.Error.Code, Message: msg.Error.Message}}
			} else {
				call.ch <- reply{result: msg.Result}
			}
			continue
		}
		if msg.Method == "" {
			continue
		}
		c.mu.Lock()
		fns := append([]func(json.RawMessage){}, c.handlers[msg.Session+"\x00"+msg.Method]...)
		c.mu.Unlock()
		for _, fn := range fns {
			fn(msg.Params)
		}
	}
}
