// Package sysproxy decides which proxy one of localcode's own requests
// goes through.
//
// Go reads HTTPS_PROXY, HTTP_PROXY and NO_PROXY, and nothing else. A
// browser on Windows reads Internet Options instead: a fixed proxy with
// its bypass list, or a proxy script that automatic detection finds or
// that is named by address. On a network that only lets proxied traffic
// out, that difference is a browser that opens GitHub and a localcode
// whose connection is reset before its first byte. This closes it for
// the requests that ask here.
//
// The environment comes first, because a variable is somebody saying
// what they want for this one process. Then the Windows settings, then a
// direct connection. Off Windows there is nothing past the environment to
// read, so the answer there is the one http.ProxyFromEnvironment gives,
// which is what every request had before this package existed.
package sysproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Choice is the proxy a request goes through, and where that came from.
type Choice struct {
	// Proxy is nil for a direct connection. It can carry a user name and
	// password, so it is never printed whole: see String.
	Proxy *url.URL
	// From names the setting the proxy came from, as a phrase a sentence
	// can end with: "HTTPS_PROXY", "the Windows proxy settings".
	From string
	// Note says why a request goes direct, when there is more to say than
	// "no proxy": a bypass list named the host, a script answered DIRECT,
	// a lookup did not finish in time.
	Note string
}

// String says how a request was sent, for an error message. The proxy is
// named by host and port only, never with the credentials its URL may
// carry, because this is text that gets pasted into bug reports.
func (c Choice) String() string {
	if c.Proxy == nil {
		if c.Note == "" {
			return "with no proxy"
		}
		return "with no proxy (" + c.Note + ")"
	}
	return fmt.Sprintf("through proxy %s (from %s)", c.Proxy.Host, c.From)
}

// For returns the proxy a request to u goes through. The error is the
// environment's, and only for the one case Go refuses: HTTP_PROXY in a
// process that looks like a CGI handler.
func For(ctx context.Context, u *url.URL) (Choice, error) {
	return std.choose(ctx, u)
}

// resolver is For with its inputs named, so a test can hand it an
// environment and a system lookup of its own. http.ProxyFromEnvironment
// reads the environment once per process, which no test can reset.
type resolver struct {
	getenv  func(string) string
	fromEnv func(*http.Request) (*url.URL, error)
	system  *cache
}

var std = resolver{
	getenv:  os.Getenv,
	fromEnv: http.ProxyFromEnvironment,
	system:  newCache(systemChoice),
}

func (r resolver) choose(ctx context.Context, u *url.URL) (Choice, error) {
	// Never proxied, whatever any setting says, which is also what Go
	// does with the environment: a proxy cannot reach this machine's
	// loopback, and a test server lives there.
	if isLoopback(u.Hostname()) {
		return Choice{Note: "loopback is never sent through a proxy"}, nil
	}
	// The environment decides whenever it names a proxy for this scheme,
	// and then entirely, NO_PROXY included, exactly as Go reads it. A
	// NO_PROXY with nothing beside it names exceptions to nothing, which
	// is also how Go reads it, so it does not stop the Windows settings.
	if name := envName(u.Scheme, r.getenv); name != "" {
		p, err := r.fromEnv(&http.Request{URL: u})
		if err != nil || p != nil {
			return Choice{Proxy: p, From: name}, err
		}
		return Choice{Note: name + " is set, and NO_PROXY leaves " + u.Hostname() + " out of it"}, nil
	}
	return r.system.get(ctx, u), nil
}

// envName returns the variable that names a proxy for scheme, in the
// order Go looks: upper case first.
func envName(scheme string, getenv func(string) string) string {
	var names []string
	switch scheme {
	case "https":
		names = []string{"HTTPS_PROXY", "https_proxy"}
	case "http":
		names = []string{"HTTP_PROXY", "http_proxy"}
	}
	for _, n := range names {
		if getenv(n) != "" {
			return n
		}
	}
	return ""
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// settings is what Internet Options say about proxies, in the shape
// WinHttpGetIEProxyConfigForCurrentUser reports them. Empty off Windows.
type settings struct {
	// AutoDetect is "Automatically detect settings": WPAD, a script found
	// through DHCP or DNS.
	AutoDetect bool
	// ScriptURL is "Use automatic configuration script".
	ScriptURL string
	// Proxy is "Use a proxy server": "host:port" for every protocol, or
	// "http=host:port;https=host:port" per protocol.
	Proxy string
	// Bypass is the exceptions to Proxy, ";"-separated, with "<local>"
	// for "Bypass proxy server for local addresses".
	Bypass string
}

// autoAnswer is what a proxy script decided for one URL.
type autoAnswer struct {
	Direct bool
	Proxy  string
	Bypass string
}

// systemChoice reads this platform's proxy settings and decides for u.
func systemChoice(u *url.URL) Choice {
	s, err := readSettings()
	if err != nil {
		return Choice{Note: fmt.Sprintf("none set in HTTPS_PROXY, and the system proxy settings could not be read: %v", err)}
	}
	return decide(u, s, autoProxy)
}

// decide turns the Windows settings into a Choice for u, in the order
// Internet Explorer set and every browser since has kept: a script
// first, found by detection or named by address, and the fixed proxy
// only when no script answered. auto runs the script, and is called only
// when the settings ask for one.
func decide(u *url.URL, s settings, auto func(target string, s settings) (autoAnswer, error)) Choice {
	host := u.Hostname()
	var scriptFailed string
	if s.AutoDetect || s.ScriptURL != "" {
		// The scheme and host and nothing else. A script decides by host
		// in practice, the answer is cached by host, and the path and
		// query of a request are nobody's business but its own.
		a, err := auto(u.Scheme+"://"+u.Host+"/", s)
		switch {
		case err != nil:
			scriptFailed = err.Error()
		case a.Direct:
			return Choice{Note: "the Windows proxy script answers DIRECT for " + host}
		case bypassed(u, a.Bypass):
			return Choice{Note: host + " is in the bypass list the Windows proxy script returned"}
		default:
			if p := pickProxy(a.Proxy, u.Scheme); p != nil {
				return Choice{Proxy: p, From: "the Windows proxy script"}
			}
			return Choice{Note: fmt.Sprintf("the Windows proxy script answered %q, which names no HTTP proxy for %s", a.Proxy, u.Scheme)}
		}
	}
	if s.Proxy != "" {
		if bypassed(u, s.Bypass) {
			return Choice{Note: host + " is in the Windows proxy bypass list"}
		}
		if p := pickProxy(s.Proxy, u.Scheme); p != nil {
			return Choice{Proxy: p, From: "the Windows proxy settings"}
		}
	}
	if scriptFailed != "" {
		return Choice{Note: scriptFailed + ", and no proxy server is set in " + Sources}
	}
	return Choice{Note: "none set in " + Sources}
}

// lookupWait is how long a request waits for the system's answer before
// it goes direct. Automatic detection asks DHCP and DNS for a script and
// can take seconds where there is none; a check that waited on that
// without a bound would be a button that hangs.
const lookupWait = 5 * time.Second

// lookupTTL is how long an answer is reused. Long enough that a check and
// the download it leads to ask once; short enough that a laptop carried
// from home to an office network is not kept on the wrong answer for
// long.
const lookupTTL = 5 * time.Minute

// cache keeps one system lookup per scheme and host, and lets at most one
// run at a time for each.
//
// A lookup that outlives lookupWait keeps running and its answer is kept:
// the request that gave up on it went direct, and the next one gets the
// answer without waiting again.
type cache struct {
	lookup    func(*url.URL) Choice
	wait, ttl time.Duration
	now       func() time.Time

	mu      sync.Mutex
	entries map[string]*pending
}

type pending struct {
	done chan struct{}
	// Both written before done is closed and read only after.
	choice Choice
	at     time.Time
}

func (p *pending) finished() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func newCache(lookup func(*url.URL) Choice) *cache {
	return &cache{lookup: lookup, wait: lookupWait, ttl: lookupTTL, now: time.Now}
}

func (c *cache) get(ctx context.Context, u *url.URL) Choice {
	key := u.Scheme + "://" + u.Host
	c.mu.Lock()
	p := c.entries[key]
	if p != nil && p.finished() && c.now().Sub(p.at) > c.ttl {
		p = nil
	}
	if p == nil {
		p = &pending{done: make(chan struct{})}
		if c.entries == nil {
			c.entries = map[string]*pending{}
		}
		c.entries[key] = p
		target := &url.URL{Scheme: u.Scheme, Host: u.Host}
		go func() {
			p.choice = c.lookup(target)
			p.at = c.now()
			close(p.done)
		}()
	}
	c.mu.Unlock()

	// An answer already there wins over a context that has already
	// ended: select picks at random among ready cases.
	if p.finished() {
		return p.choice
	}
	timer := time.NewTimer(c.wait)
	defer timer.Stop()
	select {
	case <-p.done:
		return p.choice
	case <-timer.C:
		return Choice{Note: fmt.Sprintf("the system proxy lookup did not answer within %s", c.wait)}
	case <-ctx.Done():
		return Choice{Note: "the request ended before the system proxy lookup answered"}
	}
}
