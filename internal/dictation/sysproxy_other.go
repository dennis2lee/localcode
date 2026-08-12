//go:build !windows

package dictation

import "net/url"

// systemProxyFor is Windows-only: macOS and Linux setups that need a
// proxy set the environment variables Go already honours, and reading
// scutil or GNOME settings would be new behaviour nobody has asked for.
func systemProxyFor(*url.URL) (*url.URL, error) { return nil, nil }

// systemProxyDescription reports no OS-level proxy on this platform.
func systemProxyDescription() string { return "" }

// systemAutoConfigURL: no PAC registry outside Windows.
func systemAutoConfigURL() string { return "" }
