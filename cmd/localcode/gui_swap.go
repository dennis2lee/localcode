package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"localcode/internal/daemon"
)

// The desktop window's handoff.
//
// The terminal's handoff passes a listening socket to the successor and
// the TUI keeps talking to the same port. The window has no such port to
// pass: it serves the daemon's handler in its own process, on a loopback
// listener the window itself owns and never gives up, because the page it
// is showing is connected to it. So the window's version is one step
// indirect. The successor gets a fresh loopback listener of its own; what
// the window serves is swapped from the in-process handler to a proxy
// onto that listener; and the page is reloaded so it is the new version's
// interface, not only the new version's API. Everything the terminal
// handoff does to the retiring daemon — publish what it owns, drain, end
// its streams — happens the same way here, through Retire.
//
// The alive pipe is the same one the terminal uses, and it is what ties
// the successor's life to the window's: the write end stays open in this
// process for as long as it runs, and a daemon reading EOF on the other
// end exits. A window closed an hour after an update takes its daemon
// with it, with nothing to remember to stop.

// swapHandler serves whatever handler it currently holds, and can be
// pointed at another one while requests are in flight. Requests already
// inside the old handler finish there; new ones go to the new one.
type swapHandler struct {
	current atomic.Pointer[http.Handler]
}

func newSwapHandler(h http.Handler) *swapHandler {
	s := &swapHandler{}
	s.Store(h)
	return s
}

func (s *swapHandler) Store(h http.Handler) { s.current.Store(&h) }

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*s.current.Load()).ServeHTTP(w, r)
}

// windowHandoff starts binary as the daemon behind the window, retires
// the in-process one, and points front at the new one.
//
// Returns once the successor is serving and the old daemon has retired;
// reload is called at the end so the page comes back on the new version.
func windowHandoff(d *daemon.Daemon, front *swapHandler, alive *tuiAlivePipe, cleanup func(), reload func(), version, binary string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("bind a port for the new localcode: %w", err)
	}
	// Before the successor exists, not after: it reads this file on its
	// way up, and the parent used to write it in Retire — which runs
	// after waiting for the successor to announce itself. See
	// Daemon.PublishOwned.
	if err := d.PublishOwned(); err != nil {
		fmt.Fprintf(os.Stderr, "handoff: could not publish owned sessions: %v\n", err)
	}
	pid, exited, err := spawnSuccessor(binary, ln, alive.r)
	if err != nil {
		ln.Close()
		return err
	}
	// A successor that dies later takes the window's daemon with it and
	// every request afterwards answers 502. Recording the exit is what
	// turns that into something with a cause attached.
	go func() {
		err := <-exited
		rememberSuccessorExit(pid, err)
		noteSuccessorExit(pid, err)
	}()
	addr := ln.Addr().String()
	// The successor holds its own handle on the socket; closing ours
	// here leaves the port bound to it alone.
	ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), retireTimeout)
	defer cancel()
	if !d.Retire(ctx, version, pid) {
		fmt.Fprintf(os.Stderr, "handoff: some work did not finish within %s and was stopped\n", retireTimeout)
	}
	front.Store(successorProxy(addr))
	cleanup()
	// A moment for the streams the retiring daemon just ended to close
	// on the page's side before it is told to load again, so the reload
	// lands on the proxy rather than racing the old handler's last bytes.
	time.Sleep(200 * time.Millisecond)
	reload()
	return nil
}
