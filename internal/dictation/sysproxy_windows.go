package dictation

import (
	"net/url"

	"golang.org/x/sys/windows/registry"
)

// systemProxyFor reads the same per-user proxy setting every browser on
// the machine uses: HKCU\...\Internet Settings. Any failure — no key, no
// value, proxy disabled — means direct, which is exactly what Go did
// before this existed.
func systemProxyFor(target *url.URL) (*url.URL, error) {
	server, override, ok := readRegistryProxy()
	if !ok {
		return nil, nil
	}
	if proxyBypassed(target.Hostname(), override) {
		return nil, nil
	}
	scheme := target.Scheme
	if scheme == "" {
		scheme = "http"
	}
	return asProxyURL(parseProxyServer(server, scheme))
}

// systemProxyDescription is what the probe prints about the OS setting.
func systemProxyDescription() string {
	server, _, ok := readRegistryProxy()
	if !ok {
		return ""
	}
	return server
}

// systemAutoConfigURL reads the PAC script address, which is how most
// corporate Windows setups actually configure their proxy: ProxyEnable
// stays 0, and the browser runs this script to pick a proxy per URL.
func systemAutoConfigURL() string {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	u, _, err := k.GetStringValue("AutoConfigURL")
	if err != nil {
		return ""
	}
	return u
}

func readRegistryProxy() (server, override string, ok bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return "", "", false
	}
	defer k.Close()

	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return "", "", false
	}
	server, _, err = k.GetStringValue("ProxyServer")
	if err != nil || server == "" {
		return "", "", false
	}
	override, _, _ = k.GetStringValue("ProxyOverride")
	return server, override, true
}
