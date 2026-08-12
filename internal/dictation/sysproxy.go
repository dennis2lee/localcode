package dictation

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

// This file is why the browser can reach the speech server and localcode
// cannot.
//
// On a corporate Windows laptop, web traffic goes through a proxy nobody
// remembers configuring — it came from IT, it lives in the Windows
// registry, and every browser and WinHTTP application uses it without
// being asked. Go reads none of that: it honours the HTTP_PROXY family of
// environment variables and nothing else. So the analysis that said "the
// server answers GET / with JSON" (made through tooling that uses the
// system proxy) and the probe that said "every byte sent gets the
// connection reset" (made by localcode, connecting directly into a
// middlebox) were both telling the truth about two different network
// paths.
//
// dictationProxy closes the gap for dictation traffic: the environment
// wins when set, because that is the Go convention and an explicit
// instruction; otherwise the OS's own proxy setting is used, which is
// what the person's other software is already doing.

// dictationProxy is the Proxy function on every dictation transport.
func dictationProxy(req *http.Request) (*url.URL, error) {
	if envProxyConfigured() {
		// Read fresh, not via http.ProxyFromEnvironment: that caches the
		// environment on its first use anywhere in the process, so a
		// variable exported after startup — which is exactly how someone
		// follows the probe's advice — would be silently ignored.
		return envProxyFor(req.URL)
	}
	// Loopback is never proxied, whatever the registry says. The local
	// whisper engine lives at 127.0.0.1, WinINET exempts loopback without
	// writing it into ProxyOverride, and routing the local engine's
	// traffic to a corporate gateway would break local dictation on
	// every proxied laptop at once. (The environment branch above gets
	// this from net/http itself, which never proxies localhost.)
	host := strings.ToLower(req.URL.Hostname())
	if host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" {
		return nil, nil
	}
	return systemProxyFor(req.URL)
}

// envProxyFor resolves the conventional variables for one request:
// HTTPS_PROXY for https targets, HTTP_PROXY for http, NO_PROXY to exempt.
// Loopback is never proxied, matching net/http's own behaviour.
func envProxyFor(target *url.URL) (*url.URL, error) {
	host := strings.ToLower(target.Hostname())
	if host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" {
		return nil, nil
	}
	if proxyBypassed(host, firstEnv("NO_PROXY", "no_proxy")) {
		return nil, nil
	}
	var raw string
	if target.Scheme == "https" {
		raw = firstEnv("HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy")
	} else {
		raw = firstEnv("HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy")
	}
	if raw == "" {
		return nil, nil
	}
	return asProxyURL(raw)
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// envProxyConfigured reports whether the Go-conventional variables are
// set at all. Any of them counts: someone who set HTTP_PROXY but not
// HTTPS_PROXY meant something by it, and second-guessing an explicit
// environment with the registry would make the explicit one impossible
// to use as an override.
func envProxyConfigured() bool {
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}

// parseProxyServer extracts the proxy for scheme from the registry's
// ProxyServer value, which comes in two shapes: "host:port" for all
// traffic, or "http=host:port;https=host:port;ftp=..." per scheme.
func parseProxyServer(server, scheme string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	if !strings.Contains(server, "=") {
		return server
	}
	var httpEntry string
	for _, part := range strings.Split(server, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case scheme:
			return strings.TrimSpace(v)
		case "http":
			httpEntry = strings.TrimSpace(v)
		}
	}
	// An https request on a setup that only names an http proxy still
	// goes through it; that is how WinINET treats the single-proxy case.
	return httpEntry
}

// proxyBypassed reports whether host is on the registry's ProxyOverride
// list — the "don't proxy these" patterns, separated by semicolons.
// "<local>" means hosts with no dot in them. Patterns may carry a
// leading "*." or a bare domain; both mean the domain and everything
// under it.
func proxyBypassed(host, override string) bool {
	host = strings.ToLower(host)
	for _, pattern := range strings.Split(override, ";") {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if pattern == "<local>" {
			if !strings.Contains(host, ".") {
				return true
			}
			continue
		}
		pattern = strings.TrimPrefix(pattern, "*.")
		pattern = strings.TrimPrefix(pattern, "*")
		pattern = strings.TrimPrefix(pattern, ".")
		if host == pattern || strings.HasSuffix(host, "."+pattern) {
			return true
		}
	}
	return false
}

// asProxyURL turns a registry-style "host:port" into a URL the transport
// accepts. A scheme is rare in the registry but tolerated.
func asProxyURL(hostport string) (*url.URL, error) {
	if hostport == "" {
		return nil, nil
	}
	if !strings.Contains(hostport, "://") {
		hostport = "http://" + hostport
	}
	return url.Parse(hostport)
}
