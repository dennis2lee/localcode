//go:build windows

package daemon

import "os"

// pidAlive reports whether a process exists. On Windows os.FindProcess is
// not the formality it is elsewhere: it opens the process, and fails for
// a pid nothing is running under, which is exactly the question.
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
