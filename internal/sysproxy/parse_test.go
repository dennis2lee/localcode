package sysproxy

import (
	"net/url"
	"testing"
)

// Every shape Windows writes a proxy in, read for an https request and an
// http one. The three sources write three shapes: Internet Options'
// "host:port" and "http=...;https=...", a proxy script's "PROXY host:port;
// DIRECT", and WinHTTP's own list, separated by semicolons or spaces.
func TestPickProxyReadsEveryShapeWindowsWrites(t *testing.T) {
	for _, tt := range []struct {
		list   string
		scheme string
		want   string // "" means direct
	}{
		// Internet Options, one proxy for every protocol.
		{"proxy.example.com:8080", "https", "http://proxy.example.com:8080"},
		{"proxy.example.com:8080", "http", "http://proxy.example.com:8080"},
		{"proxy.example.com", "https", "http://proxy.example.com"},
		{"http://proxy.example.com:8080", "https", "http://proxy.example.com:8080"},
		{"http://proxy.example.com:8080/", "https", "http://proxy.example.com:8080"},
		{"  proxy.example.com:8080  ", "https", "http://proxy.example.com:8080"},
		// Internet Options, per protocol. The https entry is still a
		// proxy spoken to over HTTP, with CONNECT.
		{"http=web.example.com:80;https=secure.example.com:8443", "https", "http://secure.example.com:8443"},
		{"http=web.example.com:80;https=secure.example.com:8443", "http", "http://web.example.com:80"},
		{"HTTPS=secure.example.com:8443", "https", "http://secure.example.com:8443"},
		{"http=web.example.com:80", "https", ""},
		{"ftp=ftp.example.com:21;https=secure.example.com:8443", "https", "http://secure.example.com:8443"},
		// SOCKS there is version 4, which Go does not speak.
		{"socks=socks.example.com:1080", "https", ""},
		{"socks=socks.example.com:1080;https=secure.example.com:8443", "https", "http://secure.example.com:8443"},
		// A proxy script's answer.
		{"PROXY proxy.example.com:8080; DIRECT", "https", "http://proxy.example.com:8080"},
		{"PROXY proxy.example.com:8080", "https", "http://proxy.example.com:8080"},
		{"proxy proxy.example.com:8080", "https", "http://proxy.example.com:8080"},
		{"PROXY a.example.com:8080; PROXY b.example.com:8080", "https", "http://a.example.com:8080"},
		{"DIRECT", "https", ""},
		{"DIRECT; PROXY proxy.example.com:8080", "https", ""},
		{"HTTPS secure.example.com:443", "https", "https://secure.example.com:443"},
		{"HTTP proxy.example.com:3128", "https", "http://proxy.example.com:3128"},
		{"SOCKS5 socks.example.com:1080", "https", "socks5://socks.example.com:1080"},
		{"SOCKS socks.example.com:1080; PROXY proxy.example.com:8080", "https", "http://proxy.example.com:8080"},
		{"SOCKS4 socks.example.com:1080", "https", ""},
		{"PROXY", "https", ""},
		// WinHTTP's list.
		{"a.example.com:8080;b.example.com:8080", "https", "http://a.example.com:8080"},
		{"a.example.com:8080 b.example.com:8080", "https", "http://a.example.com:8080"},
		// Nothing, or nothing that is a proxy.
		{"", "https", ""},
		{";;", "https", ""},
		{"ftp://proxy.example.com:21", "https", ""},
		{"http://proxy.example.com:8080/path", "https", ""},
		{"http://", "https", ""},
	} {
		got := pickProxy(tt.list, tt.scheme)
		switch {
		case tt.want == "" && got != nil:
			t.Errorf("pickProxy(%q, %s) = %s, want a direct connection", tt.list, tt.scheme, got)
		case tt.want != "" && got == nil:
			t.Errorf("pickProxy(%q, %s) = direct, want %s", tt.list, tt.scheme, tt.want)
		case tt.want != "" && got.String() != tt.want:
			t.Errorf("pickProxy(%q, %s) = %s, want %s", tt.list, tt.scheme, got, tt.want)
		}
	}
}

// The exceptions box in Internet Options, read the way Windows reads it:
// "*" anywhere, "<local>" for a name with no dot, an optional scheme or
// port, and the separators people actually type.
func TestBypassedReadsTheWindowsExceptionList(t *testing.T) {
	for _, tt := range []struct {
		target string
		list   string
		want   bool
	}{
		{"https://intranet/", "<local>", true},
		{"https://intranet.example.com/", "<local>", false},
		{"https://10.0.0.5/", "<local>", false},
		{"https://api.github.com/", "<local>", false},
		{"https://api.github.com/", "", false},
		{"https://build.example.com/", "*.example.com", true},
		{"https://example.com/", "*.example.com", false},
		{"https://example.com/", "example.com", true},
		{"https://build.example.com/", "example.com", false},
		{"https://build.example.com/", ".example.com", true},
		{"https://Build.Example.COM/", "*.EXAMPLE.com", true},
		{"https://10.0.0.5/", "10.*", true},
		{"https://10.0.0.5/", "192.168.*.*", false},
		{"https://192.168.1.20/", "192.168.*.*", true},
		{"https://10.0.0.5/", "10.0.0.0/8", true},
		{"https://11.0.0.5/", "10.0.0.0/8", false},
		{"https://mirror.example.com/", "*mirror*", true},
		{"https://api.github.com/", "*", true},
		// Several entries, separated the ways people separate them.
		{"https://build.example.com/", "<local>;*.example.com", true},
		{"https://build.example.com/", "<local>; *.example.com", true},
		{"https://build.example.com/", "<local>,*.example.com", true},
		{"https://api.github.com/", "<local>;*.example.com;10.*", false},
		// A scheme limits an entry to requests with that scheme.
		{"https://build.example.com/", "https://*.example.com", true},
		{"http://build.example.com/", "https://*.example.com", false},
		// A port limits an entry to that port, with the scheme's default
		// standing in for a URL that names none.
		{"https://build.example.com:8443/", "*.example.com:8443", true},
		{"https://build.example.com/", "*.example.com:8443", false},
		{"https://build.example.com/", "*.example.com:443", true},
		{"https://build.example.com:8443/", "*.example.com:*", true},
	} {
		u, err := url.Parse(tt.target)
		if err != nil {
			t.Fatal(err)
		}
		if got := bypassed(u, tt.list); got != tt.want {
			t.Errorf("bypassed(%s, %q) = %v, want %v", tt.target, tt.list, got, tt.want)
		}
	}
}

func TestWildcardMatchesAnyRunOfCharacters(t *testing.T) {
	for _, tt := range []struct {
		pattern, s string
		want       bool
	}{
		{"", "", true},
		{"*", "", true},
		{"*", "anything.at.all", true},
		{"a*c", "abc", true},
		{"a*c", "ac", true},
		{"a*c", "abcd", false},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", false},
		{"a**b", "ab", true},
		{"*a*b*", "xxaxxbxx", true},
		{"*a*b", "xxbxxa", false},
		{"exact", "exact", true},
		{"exact", "exactly", false},
	} {
		if got := wildcard(tt.pattern, tt.s); got != tt.want {
			t.Errorf("wildcard(%q, %q) = %v, want %v", tt.pattern, tt.s, got, tt.want)
		}
	}
}
