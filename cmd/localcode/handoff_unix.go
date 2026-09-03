//go:build !windows

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
	"os/exec"
	"strings"
	"time"

	"localcode/internal/childproc"
	"localcode/internal/daemon"
)

// The process side of handing a daemon to a newer version of itself.
// The daemon package decides what a retiring daemon owes its sessions;
// this file owns what a process owes its successor: the listening socket,
// a way to say "I am serving", and a way to know when the terminal above
// both of them has gone.
//
// Three file descriptors cross to the child, at fixed numbers so neither
// side has to parse anything:
//
//	3  the listening socket, so the child accepts on the same port with
//	   no gap and no second bind
//	4  the write end of a pipe the child writes one byte to when it is
//	   serving, so the parent stops accepting only once somebody else
//	   has started
//	5  the read end of a pipe whose write end the TUI's process keeps
//	   open for as long as it lives; EOF on it is the terminal going
//	   away, and a daemon with no terminal exits. Passed down through
//	   every generation, so the third daemon under one TUI still knows
//	   which process to watch — not its parent, which was the second
//	   daemon and has since retired.
//
// exec inherits nothing by default in Go; ExtraFiles is the whole list.

const (
	fdListener = 3
	fdReady    = 4
	fdTUIAlive = 5

	envTakeover = "LOCALCODE_TAKEOVER"

	// How long the retiring daemon waits for its turns and tasks. The
	// orchestration ceiling is thirty minutes for a whole run, and a
	// handoff that outlasts that is a turn that was never going to end.
	retireTimeout = 30 * time.Minute
	// How long the successor gets to come up and say so.
	successorTimeout = 20 * time.Second
)

// inherited is what a process started by a handoff was given.
type inherited struct {
	ln    net.Listener
	ready *os.File
	alive *os.File
}

// takingOver reports whether this process was started by a handoff, and
// hands back what it inherited if so.
func takingOver() (inherited, bool) {
	if os.Getenv(envTakeover) == "" {
		return inherited{}, false
	}
	lnFile := os.NewFile(uintptr(fdListener), "listener")
	ln, err := net.FileListener(lnFile)
	// FileListener dups the descriptor, so the *os.File is not needed
	// after this either way.
	lnFile.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "takeover: the inherited listener is unusable: %v\n", err)
		return inherited{}, false
	}
	return inherited{
		ln:    ln,
		ready: os.NewFile(uintptr(fdReady), "ready"),
		alive: os.NewFile(uintptr(fdTUIAlive), "tui-alive"),
	}, true
}

// announceReady tells the parent this process is serving, and watches
// the terminal's pipe so this daemon leaves when the TUI does.
func (in inherited) announceReady() {
	if in.ready != nil {
		_, _ = in.ready.Write([]byte{1})
		in.ready.Close()
	}
	if in.alive != nil {
		go func() {
			// Nothing is ever written; only the close is meaningful.
			_, _ = io.Copy(io.Discard, in.alive)
			fmt.Fprintln(os.Stderr, "the terminal this daemon was started from has closed; stopping")
			os.Exit(0)
		}()
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

// spawnSuccessor starts the binary at this program's own path with the
// listener and the pipes, and waits until it is serving.
//
// The binary at that path is the new one: an update has replaced it by
// the time this runs. os.Args are passed through unchanged, so the
// successor is the same invocation — --agent, --listen, --config and
// all — with one environment variable telling it what it inherited.
func spawnSuccessor(ln net.Listener, alive *os.File) (pid int, err error) {
	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		return 0, fmt.Errorf("the listener is a %T, which cannot be handed to another process", ln)
	}
	lnFile, err := tcp.File()
	if err != nil {
		return 0, fmt.Errorf("get the listener's descriptor: %w", err)
	}
	defer lnFile.Close()

	readyR, readyW, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	defer readyR.Close()

	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("find this program: %w", err)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), envTakeover+"=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// A no-op here; on Windows it would keep a console from flashing
	// up, and every spawn site is held to calling it so none is the one
	// that forgot.
	childproc.Hide(cmd)
	// Index i is descriptor 3+i in the child.
	cmd.ExtraFiles = []*os.File{lnFile, readyW, alive}
	if err := cmd.Start(); err != nil {
		readyW.Close()
		return 0, fmt.Errorf("start the new localcode: %w", err)
	}
	// The parent's copy of the write end has to go, or the read below
	// never sees EOF when the child dies before writing.
	readyW.Close()

	// Reaped in the background: the child outlives this function, and
	// a zombie is what an unreaped child becomes.
	go func() { _ = cmd.Wait() }()

	readyCh := make(chan error, 1)
	go func() {
		var b [1]byte
		_, rerr := readyR.Read(b[:])
		readyCh <- rerr
	}()
	select {
	case rerr := <-readyCh:
		if rerr != nil {
			return 0, fmt.Errorf("the new localcode (pid %d) stopped before it began serving", cmd.Process.Pid)
		}
		return cmd.Process.Pid, nil
	case <-time.After(successorTimeout):
		_ = cmd.Process.Kill()
		return 0, fmt.Errorf("the new localcode (pid %d) did not begin serving within %s", cmd.Process.Pid, successorTimeout)
	}
}

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
func handoffTo(d *daemon.Daemon, srv *http.Server, ln net.Listener, alive *os.File, cleanup func(), version string) error {
	pid, err := spawnSuccessor(ln, alive)
	if err != nil {
		return err
	}
	// Serve returns as soon as its listener is closed; the connections it
	// accepted stay up. The successor holds its own descriptor for the
	// same socket, so the port stays bound.
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
