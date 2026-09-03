//go:build windows

package main

import (
	"fmt"
	"net"
	"net/http"
	"os"

	"localcode/internal/daemon"
)

// No handoff on Windows, for the same reason there is no exec: a socket
// cannot be inherited by another process through Go's os/exec, and the
// Windows update is an MSI that msiexec applies after localcode exits.
// The stubs keep the one code path in modes.go; each says so if reached.

type inherited struct {
	ln    net.Listener
	ready *os.File
	alive *os.File
}

func takingOver() (inherited, bool) { return inherited{}, false }

func (in inherited) announceReady() {}

type tuiAlivePipe struct{ r, w *os.File }

func newTUIAlivePipe() (*tuiAlivePipe, error) { return &tuiAlivePipe{}, nil }

func handoffTo(d *daemon.Daemon, srv *http.Server, ln net.Listener, alive *os.File, cleanup func(), version string) error {
	return fmt.Errorf("localcode cannot hand itself over on Windows")
}

func stopDaemonAt(addr string) error { return nil }
