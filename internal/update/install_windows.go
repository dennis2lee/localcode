//go:build windows

package update

import (
	"os"
	"path/filepath"
)

// startInstaller hands the MSI to the install helper and returns without
// waiting. See msi_helper_windows.go for the helper: why it exists, what
// it waits for, and what it runs.
func startInstaller(path string) error {
	dir, err := msiUpdatesDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	version := MSIVersionFromName(path)
	if version == "" {
		version = filepath.Base(path)
	}
	return stageMSIInstaller(dir, exe, path, version, exe, os.Args[1:], IsGUIExecutable(exe))
}
