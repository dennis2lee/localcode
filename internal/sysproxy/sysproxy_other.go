//go:build !windows

package sysproxy

import "errors"

// Sources names where a proxy can be set on this platform, for a message
// that tells somebody where to set one.
const Sources = "HTTPS_PROXY"

// readSettings has nothing to read off Windows. macOS keeps its proxy
// settings behind a framework only CGo reaches, which the default build
// does not use, and Linux has no one place every program reads. The
// environment is the whole answer here, as it was before this package.
func readSettings() (settings, error) {
	return settings{}, nil
}

// autoProxy is never asked for off Windows: readSettings never reports a
// script to run.
func autoProxy(string, settings) (autoAnswer, error) {
	return autoAnswer{}, errors.New("proxy scripts are only read on Windows")
}
