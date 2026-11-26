//go:build !windows

package sshterm

// startPseudoConsole has no pseudo console to start a shell under: a local
// terminal is Windows only. A test gives LocalShell.Start instead.
func startPseudoConsole(argv []string, dir string, cols, rows int) (LocalProcess, error) {
	return nil, ErrNoLocalShell
}
