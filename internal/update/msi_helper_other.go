//go:build !windows

package update

import "fmt"

// RunMSIHelper never runs off Windows: the helper is a staged copy that
// only a Windows install stages. It exists so main can name it without a
// build tag around the call.
func RunMSIHelper(pendingPath string) error {
	return fmt.Errorf("the install helper runs on Windows only")
}

// SweepMSIHelper is a no-op off Windows: there is no helper copy.
func SweepMSIHelper() {}
