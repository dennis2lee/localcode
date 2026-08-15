//go:build !windows

package update

import "fmt"

// startInstaller is never reached off Windows: Apply only calls it for an
// MSI, and there are none anywhere else. It exists so that the decision
// about what can be installed lives in one place — Apply — rather than
// being spread across build tags.
func startInstaller(path string) error {
	return fmt.Errorf("localcode cannot run an installer on this platform; the download is at %s", path)
}
