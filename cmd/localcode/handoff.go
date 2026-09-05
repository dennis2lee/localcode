package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"localcode/internal/daemon"
)

// The process side of handing a daemon to a newer version of itself.
// The daemon package decides what a retiring daemon owes its sessions;
// this file owns what a process owes its successor: the listening socket,
// a way to say "I am serving", and a way to know when the terminal above
// both of them has gone.
//
// Three things cross to the child:
//
//   - the listening socket, so the child accepts on the same port with
//     no gap and no second bind
//   - the write end of a pipe the child writes one byte to when it is
//     serving, so the parent stops accepting only once somebody else
//     has started
//   - the read end of a pipe whose write end the TUI's process keeps
//     open for as long as it lives; EOF on it is the terminal going
//     away, and a daemon with no terminal exits. Passed down through
//     every generation, so the third daemon under one TUI still knows
//     which process to watch — not its parent, which was the second
//     daemon and has since retired.
//
// How they cross is the one thing that differs per platform: fixed
// descriptor numbers through ExtraFiles on Unix, inherited handles whose
// values travel in the environment on Windows. See handoff_unix.go and
// handoff_windows.go for takingOver and spawnSuccessor; everything else
// is here because it is the same everywhere.

const (
	envTakeover = "LOCALCODE_TAKEOVER"

	// How long the retiring daemon waits for its turns and tasks. The
	// orchestration ceiling is thirty minutes for a whole run, and a
	// handoff that outlasts that is a turn that was never going to end.
	retireTimeout = 30 * time.Minute
	// How long the successor gets to come up and say so.
	//
	// A successor builds a whole daemon before it serves: it reads the
	// config, opens the providers, loads every session from disk and
	// hands shakes with each configured MCP server. That is what the
	// desktop window's splash screen exists to cover, and on a machine
	// with several MCP servers it is not a few seconds. Twenty was a
	// terminal-sized budget and it timed out a handoff that would have
	// worked; the cost of waiting longer is a slow update, and the cost
	// of being short is an update that does not happen.
	successorTimeout = 90 * time.Second
)

// inherited is what a process started by a handoff was given.
type inherited struct {
	ln    net.Listener
	ready *os.File
	alive *os.File
}

// announceReady tells the parent this process is serving, and watches
// the terminal's pipe so this daemon leaves when the TUI does.
func (in inherited) announceReady() {
	if in.ready != nil {
		_, _ = in.ready.Write([]byte{1})
		in.ready.Close()
	}
	if in.alive != nil {
		go watchParent(in.alive, func() { os.Exit(0) })
	}
}

// watchParent stops this daemon when the process that started it goes,
// and only then.
//
// Nothing is ever written down this pipe; the close is the message. It
// used to be read with io.Copy, which reports EOF and every other read
// error identically — as nil — so any fault at all on the handle ran the
// stop path. What that looks like from outside is the daemon behind the
// window vanishing mid-turn: the indicator goes grey, the event stream
// ends, and every request afterwards is a 502 from a proxy pointing at
// nothing. A parent that has left is a specific thing and gets a specific
// test.
//
// On Windows the write end closing surfaces as ERROR_BROKEN_PIPE, which
// the os package already translates to io.EOF, so both platforms say the
// same word here.
//
// Any other error means the pipe is unusable, which says nothing about
// whether the parent is still there. Stopping on it would end a session
// somebody is in the middle of, to no purpose, so this keeps serving and
// says so — the parent tees this stream into handoff.log and into the
// message the window shows.
func watchParent(alive *os.File, stop func()) {
	buf := make([]byte, 1)
	for {
		_, err := alive.Read(buf)
		if err == nil {
			continue // not expected, and not a reason to leave
		}
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(os.Stderr, "the localcode that started this daemon has closed; stopping")
			stop()
			return
		}
		fmt.Fprintf(os.Stderr,
			"the pipe this daemon watches for its parent leaving is unreadable (%v); "+
				"it will keep serving, and will not stop on its own when the parent goes\n", err)
		return
	}
}

// tuiAlivePipe is the pipe the TUI's process holds open for as long as it
// lives. Made once, in the process that has the TUI, and its read end is
// handed to every daemon generation under it.
//
// Both ends are kept: the write end so its close means what it means,
// and the read end so it can be passed to the next successor even after
// this process has stopped being a daemon.
type tuiAlivePipe struct {
	r, w *os.File
}

func newTUIAlivePipe() (*tuiAlivePipe, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	return &tuiAlivePipe{r: r, w: w}, nil
}

// waitReady is the parent's half of the readiness pipe: one byte, or the
// child going away first, or a deadline.
func waitReady(readyR *os.File, pid int, out *successorOutput, kill func()) error {
	readyCh := make(chan error, 1)
	go func() {
		var b [1]byte
		_, rerr := readyR.Read(b[:])
		readyCh <- rerr
	}()
	select {
	case rerr := <-readyCh:
		if rerr != nil {
			return fmt.Errorf("the new localcode (pid %d) stopped before it began serving.%s", pid, out.note())
		}
		return nil
	case <-time.After(successorTimeout):
		kill()
		return fmt.Errorf("the new localcode (pid %d) did not begin serving within %s.%s",
			pid, successorTimeout, out.note())
	}
}

// successorArgs is the command line the successor is started with: this
// invocation's own arguments, so --agent, --listen, --config and all come
// back, on the binary the update produced.
func successorArgs() []string { return os.Args[1:] }

// handoffTo is what a daemon's Handoff hook does: start the successor on
// this listener, stop accepting here, and retire.
//
// The order is the whole point. The successor is serving before this
// process stops accepting, so there is no moment with nobody on the
// port; the listener is closed here only after that, which ends Serve
// without touching the connections it already accepted; and only then
// does the retiring daemon publish what it still owns and drain. srv is
// shut down last, once the streams have been ended, so Shutdown has
// nothing left to wait for.
//
// binary is the program to start: the path this process runs from, now
// holding the new file, or on Windows possibly a staged copy.
func handoffTo(d *daemon.Daemon, srv *http.Server, ln net.Listener, alive *os.File, cleanup func(), version, binary string) error {
	if binary == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("find this program: %w", err)
		}
		binary = exe
	}
	pid, _, err := spawnSuccessor(binary, ln, alive)
	if err != nil {
		return err
	}
	// Serve returns as soon as its listener is closed; the connections it
	// accepted stay up. The successor holds its own handle on the same
	// socket, so the port stays bound.
	ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), retireTimeout)
	defer cancel()
	finished := d.Retire(ctx, version, pid)
	if !finished {
		fmt.Fprintf(os.Stderr, "handoff: some work did not finish within %s and was stopped\n", retireTimeout)
	}
	// The streams have been ended and nothing new was accepted, so this
	// returns promptly; the timeout is for a client mid-write.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	cleanup()
	return nil
}

// stopDaemonAt asks the daemon now serving addr to stop. The TUI that
// was this daemon's process calls it on exit: it is the one client that
// knows the daemon under it has nobody else.
func stopDaemonAt(addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/api/daemon/shutdown", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Error == "" {
			body.Error = resp.Status
		}
		return errors.New(body.Error)
	}
	return nil
}
