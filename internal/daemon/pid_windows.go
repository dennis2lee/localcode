//go:build windows

package daemon

// pidAlive is optimistic here. Windows has no handoff — see
// selfRestartAvailable in cmd/localcode — so a manifest is never
// written on this platform, and the question is only asked of one that
// was copied in from somewhere else. Answering "alive" keeps such a file
// from being deleted by a process that cannot check.
func pidAlive(pid int) bool { return pid > 0 }
