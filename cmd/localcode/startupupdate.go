package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"localcode/internal/daemon"
)

// startupCheckTimeout bounds the outbound request that asks what the
// newest release is.
//
// Short, and short for a reason nothing else here has: this runs before
// anything is served, so every second of it is a second localcode is not
// there yet. A machine with no route to GitHub must cost startup a
// noticeable pause at most, never a hang, so the deadline is the thing
// that makes an unreachable network indistinguishable from an up-to-date
// one.
const startupCheckTimeout = 4 * time.Second

// autoUpdateAtStartup installs a newer release before localcode begins,
// and replaces this process with it.
//
// Startup is the one moment worth doing this at, and the reason is what
// an update costs rather than what it gains. Replacing the binary means
// exec, and exec means every turn in flight is gone and every background
// task with it. At startup there are none: nothing is being said to a
// model, no shell command is running, and the sessions this is about to
// interrupt have not been opened yet. So the update that would be rude at
// any other time is free here, which is why this is the only place
// localcode replaces itself without being asked.
//
// What it deliberately does not do is fail. Every outcome that is not "a
// newer release is now installed" ends with localcode starting normally
// on the build it already had: no release, no network, no asset for this
// platform, a refused write, a download that did not match its checksum.
// An update is a convenience and startup is not; a program that would not
// start because it could not check for a new version would be a worse
// program than one that never checked.
func autoUpdateAtStartup(d *daemon.Daemon, out io.Writer) {
	if !d.Loop.Config.AutoUpdateEnabled() {
		return
	}
	if !selfRestartAvailable {
		// Windows. Applying an update there runs msiexec, which asks for
		// elevation, and nothing can put a console program back in the
		// terminal it was started from afterwards. An update that cannot
		// finish is not one to start unasked.
		return
	}
	// Deliberately not gated on AllowUpdateInstall, which is a different
	// question with a similar shape. That flag asks whether a *browser
	// somewhere else* may replace the program on this machine, and its
	// answer is no for a daemon bound to a public address. Nobody remote
	// is asking here: this is the machine deciding about itself, at the
	// moment it starts, and the switch for it is auto_update.

	ctx, cancel := context.WithTimeout(context.Background(), startupCheckTimeout)
	defer cancel()

	report, err := d.InstallAtStartup(ctx)
	if err != nil {
		if errors.Is(err, daemon.ErrNoUpdate) {
			return
		}
		// Named, not swallowed. Somebody who turned this on is owed the
		// reason it did nothing, and the one that matters — an expired
		// token, a proxy, a full disk — is invisible otherwise.
		fmt.Fprintf(out, "update check: %v\n", err)
		return
	}
	fmt.Fprintln(out, report)

	// The binary on disk is the new one and this process is still the old
	// one. exec rather than a message telling somebody to start localcode
	// again: the whole of what the person asked for is to be running the
	// new version, and stopping one step short of that is how an update
	// that worked reads as one that did nothing.
	//
	// A failure here is still not fatal. The old image is intact and
	// running; it carries on as itself, having said what happened.
	if err := execSelf(); err != nil {
		fmt.Fprintf(out, "installed, but could not restart into it: %v\nStart localcode again to run %s.\n",
			err, d.Version)
	}
}
