//go:build windows

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"localcode/internal/childproc"
)

// Windows has no descriptor numbers to promise, so the three handles are
// marked inheritable, listed for the new process, and their values —
// which are the same in the child, since an inherited handle keeps its
// value — travel in the environment: LOCALCODE_TAKEOVER=<ln>,<ready>,<alive>.
//
// A socket handle can be inherited the same way a pipe handle can. Go
// creates its sockets non-inheritable (WSA_FLAG_NO_HANDLE_INHERIT), and
// TCPListener.File duplicates the socket through WSADuplicateSocket into
// a fresh handle that is ours to mark; net.FileListener on the other side
// wraps it back into a listener. Both halves exist in the standard library
// since the net package's file_posix.go became "unix || windows".
//
// What Windows still cannot do is replace a running .exe in place, which
// is a different problem with its own answer (rename; see
// internal/update/selfinstall.go), and put a console program back in the
// terminal it was started from after a restart — which is the reason a
// handoff, where nothing restarts, is worth more here than anywhere.

// takingOver reports whether this process was started by a handoff, and
// hands back what it inherited if so.
func takingOver() (inherited, bool) {
	spec := os.Getenv(envTakeover)
	if spec == "" {
		return inherited{}, false
	}
	parts := strings.Split(spec, ",")
	if len(parts) != 3 {
		fmt.Fprintf(os.Stderr, "takeover: %s=%q is not three handles\n", envTakeover, spec)
		return inherited{}, false
	}
	var h [3]uintptr
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "takeover: %s=%q: %v\n", envTakeover, spec, err)
			return inherited{}, false
		}
		h[i] = uintptr(v)
	}
	lnFile := os.NewFile(h[0], "listener")
	ln, err := net.FileListener(lnFile)
	lnFile.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "takeover: the inherited listener is unusable: %v\n", err)
		return inherited{}, false
	}
	return inherited{
		ln:    ln,
		ready: os.NewFile(h[1], "ready"),
		alive: os.NewFile(h[2], "tui-alive"),
	}, true
}

// inheritable marks a handle so a child started with it listed receives
// it. Go marks everything it opens non-inheritable, which is the right
// default and the reason this has to be said per handle.
func inheritable(f *os.File) (syscall.Handle, error) {
	h := syscall.Handle(f.Fd())
	if err := syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
		return 0, fmt.Errorf("mark %s inheritable: %w", f.Name(), err)
	}
	return h, nil
}

// spawnSuccessor starts binary with the listener and the pipes, and waits
// until it is serving.
func spawnSuccessor(binary string, ln net.Listener, alive *os.File) (pid int, exited <-chan error, err error) {
	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		return 0, nil, fmt.Errorf("the listener is a %T, which cannot be handed to another process", ln)
	}
	lnFile, err := tcp.File()
	if err != nil {
		return 0, nil, fmt.Errorf("get the listener's handle: %w", err)
	}
	defer lnFile.Close()

	readyR, readyW, err := os.Pipe()
	if err != nil {
		return 0, nil, err
	}
	defer readyR.Close()

	hLn, err := inheritable(lnFile)
	if err != nil {
		readyW.Close()
		return 0, nil, err
	}
	hReady, err := inheritable(readyW)
	if err != nil {
		readyW.Close()
		return 0, nil, err
	}
	var hAlive syscall.Handle
	if alive != nil {
		if hAlive, err = inheritable(alive); err != nil {
			readyW.Close()
			return 0, nil, err
		}
	}

	cmd := exec.Command(binary, successorArgs()...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d,%d,%d", envTakeover, hLn, hReady, hAlive))
	// Written to a file of its own rather than inherited or teed: see
	// successor_output.go. A window has no console for the successor to
	// print to, and its account of why it would not start is the whole of
	// what a failed handoff has to offer — but the writer has to be a
	// real file, or os/exec builds a pipe this process owns and the
	// child dies of SIGPIPE when this process goes.
	out := newSuccessorOutput()
	cmd.Stdout, cmd.Stderr = out.streams()
	childproc.Hide(cmd)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	handles := []syscall.Handle{hLn, hReady}
	if alive != nil {
		handles = append(handles, hAlive)
	}
	cmd.SysProcAttr.AdditionalInheritedHandles = handles
	if err := cmd.Start(); err != nil {
		readyW.Close()
		return 0, nil, fmt.Errorf("start the new localcode: %w", err)
	}
	// The parent's copy of the write end has to go, or the read never
	// sees EOF when the child dies before writing.
	readyW.Close()
	// The output file is closed when the process ends, in the same
	// goroutine that reports the end: a second reader of `done` would
	// take the value the caller is waiting for.
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		out.Close()
		done <- err
	}()

	if err := waitReady(readyR, cmd.Process.Pid, out, func() { _ = cmd.Process.Kill() }); err != nil {
		out.Close()
		return 0, nil, err
	}
	return cmd.Process.Pid, done, nil
}
