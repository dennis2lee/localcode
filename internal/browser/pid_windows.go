//go:build windows

package browser

import "os"

// pidAlive reports whether a process exists. On Windows os.FindProcess
// opens the process, and fails for a pid nothing is running under.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
