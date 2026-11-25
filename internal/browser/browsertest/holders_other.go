//go:build !windows

package browsertest

// holders names the processes holding one of paths open. Only Windows
// refuses to remove a file for being open, and only there is there a
// holder to name.
func holders(paths []string) []string { return nil }
