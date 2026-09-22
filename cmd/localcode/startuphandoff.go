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

	"localcode/internal/config"
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
// are off, this caller execs instead, nothing newer exists, or the
// install failed. Every failure is printed and none stops startup.
//
// canExec is the caller's answer, not the platform's, and that
// distinction is the whole of a fix. It used to read selfRestartAvailable
// directly, which is true on macOS and Linux — so on those platforms the
// handoff never happened, and the desktop window, which is the one caller
// that cannot exec on any platform, was left with no startup update at
// all. autoUpdateAtStartup ends in exec, and replacing a process that is
// holding a native window is not the same operation as replacing a
// headless one. A window says false here and takes the successor path
// every platform's headless daemon already takes on Windows.
//
// The version the release is compared against is the newest this machine
// can already run. On a Program Files install that is the staged copy
// from the last update, not this binary — comparing against this binary
// alone would download the same release on every start.
// stagedHandoffBinary is the handoff decided before anything is built.
//
// StagedBinary is a newer localcode this machine already has, left by an
// update that could not replace the file it was running from. On Windows
// with the MSI that is every update: the installer puts localcode.exe in
// Program Files and puts Program Files on PATH, so the copy PATH finds
// cannot be written without elevation and the new version goes to the
// user's cache directory instead. Every start after that runs the old
// binary, which hands over to the staged one.
//
// The question this answers is what the old binary does first. It used to
// build the whole daemon: read the config, connect to every MCP server —
// spawning each one as a subprocess — load the skills and the custom
// commands, open the session store. All of it thrown away a moment later,
// and some of it wrong, because a version old enough to have been
// superseded is old enough to have bugs the successor does not: a report
// that arrived on v0.141.0 was a v0.133.0 launcher complaining about a
// skill file that v0.141.0 reads without a word, printed every start
// under a banner naming a version the person was not running.
//
// So the staged copy is looked for first, and found, nothing else is
// built. No network here either: whether something newer still exists is
// the successor's question, and it asks it on its own start.
//
// Ordered cheapest first. Most machines have no staged copy, and for them
// this is one stat.
func stagedHandoffBinary(running, configPath string, canExec bool) (string, bool) {
	// Where a process can exec it replaces itself through
	// autoUpdateAtStartup instead, and never reads a staged copy.
	if canExec {
		return "", false
	}
	// Before the stat and long before the exec: an unstamped build calls
	// itself "dev", which is not a version, and Newer refuses to call
	// anything newer than a thing that is not a version. So the answer is
	// already no, and finding out what the staged copy calls itself would
	// mean running it — a process, and up to VersionOf's thirty-second
	// timeout if that copy hangs — to learn something that cannot change
	// it.
	if !update.IsVersion(running) {
		return "", false
	}
	staged := update.StagedBinary()
	if staged == "" {
		return "", false
	}
	if !autoUpdateWanted(configPath) {
		return "", false
	}
	v, err := update.VersionOf(staged)
	if err != nil || !update.Newer(running, v) {
		return "", false
	}
	return staged, true
}

// autoUpdateWanted reads the one config flag this decision turns on,
// without building anything and without repeating the config's own
// notes — buildDaemon prints those, and a start that hands off never
// reaches it, so the successor is what says them.
//
// A config that cannot be read answers no. The reason is not that the
// flag defaults to off, it does not: it is that handing off is the
// unusual action, and taking it on the strength of a file this process
// could not read would be deciding from nothing. buildDaemon reads the
// same file a moment later and reports the real error.
func autoUpdateWanted(explicitPath string) bool {
	var cfg *config.Config
	var err error
	if explicitPath != "" {
		cfg, _, err = config.Load(explicitPath)
	} else {
		e, eerr := resolveEnv()
		if eerr != nil {
			return false
		}
		cfg, _, err = config.LoadMerged(e.cwd)
	}
	if err != nil {
		return false
	}
	return cfg.AutoUpdateEnabled()
}

func startupHandoffBinary(d *daemon.Daemon, out io.Writer, canExec bool) (string, bool) {
	if canExec || !d.Loop.Config.AutoUpdateEnabled() {
		return "", false
	}
	running, staged := d.Version, ""
	// The same refusal as stagedHandoffBinary's, for the same reason: a
	// build that is not a version cannot be superseded, so there is
	// nothing to learn by running the staged copy.
	if s := update.StagedBinary(); s != "" && update.IsVersion(running) {
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
	// The process's own pipe, not one made here: see processAlivePipe.
	alive, err := processAlivePipe()
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
	// The process's own pipe, not one made here: this function then runs a
	// TUI for hours without mentioning it again, which is exactly the
	// lifetime a local does not survive. See processAlivePipe.
	alive, err := processAlivePipe()
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
