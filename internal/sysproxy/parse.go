package sysproxy

import (
	"net"
	"net/url"
	"strings"
	"unicode"
)

// Reading the strings Windows writes proxies in.
//
// Untagged on purpose: only the calls that fetch these strings are
// Windows-only, and the reading is where the mistakes would be, so it is
// tested on every platform the gate runs on.

// pickProxy reads a Windows proxy string and returns the proxy it names
// for a request of the given scheme, or nil for a direct connection.
//
// Windows writes one answer in three shapes. Internet Options keeps
// "host:port" for every protocol, or "http=host:port;https=host:port"
// per protocol. A proxy script answers in the browser convention,
// "PROXY host:port; DIRECT". And WinHTTP passes a script's answer on as a
// list of "host:port" separated by semicolons or spaces. All three are
// read here.
//
// The first entry that applies is the one used. Go's transport takes one
// proxy, not a list to fail over along, and the first is the one the
// script or the person preferred.
func pickProxy(list, scheme string) *url.URL {
	for _, part := range strings.Split(list, ";") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		if kind := strings.ToUpper(fields[0]); isKeyword(kind) {
			if kind == "DIRECT" {
				return nil
			}
			if len(fields) < 2 {
				continue
			}
			if u := keywordProxy(kind, fields[1]); u != nil {
				return u
			}
			continue
		}
		// No keyword: each field is an address of its own, which is how
		// WinHTTP separates a list with spaces.
		for _, f := range fields {
			if u := plainProxy(f, scheme); u != nil {
				return u
			}
		}
	}
	return nil
}

func isKeyword(s string) bool {
	switch s {
	case "DIRECT", "PROXY", "HTTP", "HTTPS", "SOCKS", "SOCKS4", "SOCKS5":
		return true
	}
	return false
}

// keywordProxy reads one entry of a proxy script's answer.
func keywordProxy(kind, addr string) *url.URL {
	switch kind {
	case "PROXY", "HTTP":
		return proxyURL("http", addr)
	case "HTTPS":
		return proxyURL("https", addr)
	case "SOCKS5":
		return proxyURL("socks5", addr)
	}
	// SOCKS and SOCKS4 are SOCKS version 4, which Go's transport does not
	// speak. Skipped, so a later entry can still apply.
	return nil
}

// plainProxy reads one entry of Internet Options' own form: an address
// for every protocol, or "scheme=address" for one.
func plainProxy(entry, scheme string) *url.URL {
	if proto, addr, ok := strings.Cut(entry, "="); ok {
		// "socks=" is SOCKS version 4 here too, and an "ftp=" entry is
		// for nothing localcode asks for.
		if !strings.EqualFold(proto, scheme) {
			return nil
		}
		return proxyURL("http", addr)
	}
	return proxyURL("http", entry)
}

// proxyURL turns an address into a proxy URL, with def as the scheme
// when the address names none. Anything that does not come out as a
// scheme Go can use and a host is not a proxy.
func proxyURL(def, addr string) *url.URL {
	if addr == "" {
		return nil
	}
	if !strings.Contains(addr, "://") {
		addr = def + "://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil
	}
	if u.Path != "" && u.Path != "/" {
		return nil
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Path, u.RawQuery, u.Fragment = "", "", ""
	return u
}

// bypassed reports whether a request to u skips the proxy, by a Windows
// bypass list.
//
// The list is what Internet Options' exceptions box writes: entries
// separated by semicolons (commas and spaces are accepted too, since
// people type them), "*" as a wildcard anywhere in an entry, and
// "<local>" for a name with no dot in it, which is what "Bypass proxy
// server for local addresses" adds. An entry may name a scheme
// ("https://*.example.com") or a port ("*.example.com:8443"), and then
// only matches requests with that scheme or port. A CIDR range is
// accepted as well, which Windows ignores and people write anyway.
func bypassed(u *url.URL, list string) bool {
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	entries := strings.FieldsFunc(list, func(r rune) bool {
		return r == ';' || r == ',' || unicode.IsSpace(r)
	})
	for _, e := range entries {
		e = strings.ToLower(e)
		if e == "<local>" {
			if !strings.Contains(host, ".") && !strings.Contains(host, ":") {
				return true
			}
			continue
		}
		if scheme, rest, ok := strings.Cut(e, "://"); ok {
			if scheme != u.Scheme {
				continue
			}
			e = rest
		}
		if _, cidr, err := net.ParseCIDR(e); err == nil {
			if ip := net.ParseIP(host); ip != nil && cidr.Contains(ip) {
				return true
			}
			continue
		}
		if h, p, err := net.SplitHostPort(e); err == nil && h != "" {
			// An entry with a port matches only that port.
			if p != "*" && p != port {
				continue
			}
			e = h
		}
		// ".example.com" is how some tools write "every name under it".
		if strings.HasPrefix(e, ".") {
			e = "*" + e
		}
		if wildcard(e, host) {
			return true
		}
	}
	return false
}

// wildcard matches s against pattern, where "*" stands for any run of
// characters, dots included, and nothing else is special.
func wildcard(pattern, s string) bool {
	// The classic two-pointer match: remember the last star and how much
	// of s it has swallowed, and widen it whenever the rest fails.
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case p < len(pattern) && pattern[p] == s[i]:
			p++
			i++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
