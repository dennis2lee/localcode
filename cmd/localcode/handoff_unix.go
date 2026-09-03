//go:build !windows

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"

	"localcode/internal/childproc"
)

// Descriptors cross to the child at fixed numbers through ExtraFiles, so
// neither side has to parse anything. exec inherits nothing else by
// default in Go; ExtraFiles is the whole list.
const (
	fdListener = 3
	fdReady    = 4
	fdTUIAlive = 5
)

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

// spawnSuccessor starts binary with the listener and the pipes, and waits
// until it is serving.
func spawnSuccessor(binary string, ln net.Listener, alive *os.File) (pid int, exited <-chan error, err error) {
	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		return 0, nil, fmt.Errorf("the listener is a %T, which cannot be handed to another process", ln)
	}
	lnFile, err := tcp.File()
	if err != nil {
		return 0, nil, fmt.Errorf("get the listener's descriptor: %w", err)
	}
	defer lnFile.Close()

	readyR, readyW, err := os.Pipe()
	if err != nil {
		return 0, nil, err
	}
	defer readyR.Close()

	cmd := exec.Command(binary, successorArgs()...)
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
		return 0, nil, fmt.Errorf("start the new localcode: %w", err)
	}
	// The parent's copy of the write end has to go, or the read never
	// sees EOF when the child dies before writing.
	readyW.Close()
	// Reaped in the background: the child outlives this function, and a
	// zombie is what an unreaped child becomes.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if err := waitReady(readyR, cmd.Process.Pid, func() { _ = cmd.Process.Kill() }); err != nil {
		return 0, nil, err
	}
	return cmd.Process.Pid, done, nil
}
