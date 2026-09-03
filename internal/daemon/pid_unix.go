//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

// pidAlive reports whether a process exists. Signal 0 delivers nothing
// and fails only when there is nobody to deliver it to.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
