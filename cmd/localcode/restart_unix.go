//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// selfRestartAvailable says localcode can come back as the new version
// under its own power, which is what makes updating at startup worth
// offering here and not on Windows.
const selfRestartAvailable = true

// execSelf replaces this process with the binary at its own path.
//
// exec rather than spawn-and-exit, and that is the whole reason this is
// worth doing at all: the new program inherits the process id, the
// terminal, the standard streams and the working directory, so a TUI
// running in a terminal comes back in that same terminal instead of
// leaving a shell prompt behind a window that closed. It also disposes of
// the old image completely — there is no moment where two localcodes are
// both listening on the same port, because the listening socket is closed
// by the exec itself.
//
// os.Args, not a reconstruction: whatever the user typed is what should
// come back, --agent, --listen, --config and all.
//
// It returns only on failure. A successful exec never comes back.
func execSelf() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find this program to restart it: %w", err)
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		return fmt.Errorf("restart %s: %w", exe, err)
	}
	return nil
}
