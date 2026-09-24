//go:build windows

package update

import (
	"os"
	"path/filepath"
)

// startInstaller hands the MSI to the install helper and returns without
// waiting. See msi_helper_windows.go for the helper: why it exists, what
// it waits for, and what it runs. window is the daemon's DesktopWindow,
// the same value the reply's detail is built from, so the two cannot
// disagree.
func startInstaller(path string, window bool) error {
	dir, err := msiInstallDir()
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
	return stageMSIInstaller(dir, exe, path, version, exe, os.Args[1:], window)
}

// msiInstallDir is where the helper copy and its files are staged. A
// variable so a test can point it at a directory it controls rather than
// the user's cache.
var msiInstallDir = msiUpdatesDir
