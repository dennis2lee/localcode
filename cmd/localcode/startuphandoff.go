package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/daemon"
	"localcode/internal/update"
)

// Updating at startup where exec is not available.
//
// autoUpdateAtStartup installs a newer release and execs into it, and on
// Windows both halves of that were closed: applying the MSI meant a UAC
// prompt, and a console program cannot be put back in its terminal. The
// handoff answers both. The install is ApplyForHandoff — the zip, a
// rename or a staged copy, no elevation — and instead of exec the new
// binary is started beside this process on the listener this process
// already holds, before anything is served. This process then does what
// a "/update" handoff leaves it doing: it runs the TUI, or holds the
// console, or fronts the window, against a daemon that is the new
// version. Arrived at from the start rather than mid-session.
//
// What stays old is this process's own code — the TUI, or the window
// shell. On a portable install the file under its name is the new one,
// so the next start runs it directly. Under Program Files the file is
// never replaced by this path; the staged copy is the daemon every start,
// and the MSI, from the settings window, is what brings the installed
// copy up to date.

// startupHandoffBinary reports the binary a startup handoff should
// start, or false when there is nothing to hand off to: startup updates
// are off, this platform execs instead, nothing newer exists, or the
// install failed. Every failure is printed and none stops startup.
//
// The version the release is compared against is the newest this machine
// can already run. On a Program Files install that is the staged copy
// from the last update, not this binary — comparing against this binary
// alone would download the same release on every start.
func startupHandoffBinary(d *daemon.Daemon, out io.Writer) (string, bool) {
	if selfRestartAvailable || !d.Loop.Config.AutoUpdateEnabled() {
		return "", false
	}
	running, staged := d.Version, ""
	if s := update.StagedBinary(); s != "" {
		if v, err := update.VersionOf(s); err == nil && update.Newer(running, v) {
			running, staged = v, s
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), startupCheckTimeout)
	defer cancel()
	res, report, err := d.InstallAtStartup(ctx, running, true)
	switch {
	case err == nil:
		fmt.Fprintln(out, report)
		return res.Binary, true
	case errors.Is(err, daemon.ErrNoUpdate):
		// Nothing newer than what this machine has. If what it has is a
		// staged copy newer than this binary, that copy is still the one
		// to run.
		if staged != "" {
			fmt.Fprintf(out, "Running localcode %s from %s.\n", running, staged)
			return staged, true
		}
		return "", false
	default:
		fmt.Fprintf(out, "update check: %v\n", err)
		if staged != "" {
			fmt.Fprintf(out, "Running localcode %s from %s.\n", running, staged)
			return staged, true
		}
		return "", false
	}
}

// superviseSuccessor is a headless daemon's startup handoff: start the
// new version on this listener and stay only to hold the console. This
// process exits when the successor does; the successor exits when this
// process does, through the pipe it watches, so Ctrl+C here ends both.
func superviseSuccessor(binary string, ln net.Listener) error {
	alive, err := newTUIAlivePipe()
	if err != nil {
		return err
	}
	pid, exited, err := spawnSuccessor(binary, ln, alive.r)
	if err != nil {
		return fmt.Errorf("start the new localcode: %w", err)
	}
	ln.Close()
	fmt.Fprintf(os.Stderr, "localcode is running as pid %d; this process holds the console for it\n", pid)
	return <-exited
}

// runTUIBehindSuccessor is the terminal's startup handoff: the new
// version takes the listener, and this process runs the TUI against it.
// On exit it stops the daemon it started, the way a TUI left behind by a
// "/update" handoff does.
func runTUIBehindSuccessor(binary string, ln net.Listener, listen, agentName string) error {
	alive, err := newTUIAlivePipe()
	if err != nil {
		return err
	}
	if _, _, err := spawnSuccessor(binary, ln, alive.r); err != nil {
		return fmt.Errorf("start the new localcode: %w", err)
	}
	ln.Close()

	var prog atomic.Pointer[tea.Program]
	err = runTUIClient("http://"+listen, agentName, &prog)
	if serr := stopDaemonAt(listen); serr != nil {
		fmt.Fprintf(os.Stderr, "the daemon at %s was not stopped: %v\n", listen, serr)
	}
	return err
}
