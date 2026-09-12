// Package egress bounds where localcode itself connects.
//
// Permission rules answer "which tool, on which path". They say nothing
// about where a connection goes, and a model with a shell or an MCP
// server can reach anything the machine can. This is the half of that
// which localcode is actually able to enforce: its own outbound
// connections — the model providers, remote MCP servers, the update
// check — every one of which goes through http.DefaultTransport or a
// clone of it.
//
// What it does not cover is stated plainly rather than left to be
// discovered, because a partial control described as a complete one is
// worse than none. A shell command is a separate process with its own
// sockets: `curl`, a package manager, a test suite that calls an API.
// Nothing inside this program can stop those, and refusing to *run*
// commands whose names look networked would be theatre — a script, a
// different binary name, or a here-document defeats it in seconds. What
// bounds those is the permission prompt on bash, and below that the
// operating system: a firewall, a network namespace, a proxy the child
// inherits.
package egress

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
)

// Policy is where this process may connect.
type Policy struct {
	// Allow is the destinations permitted. A bare name matches that host
	// exactly; a leading "*." matches any sub-domain of what follows, and
	// the bare domain too, so "*.example.com" covers both api.example.com
	// and example.com.
	Allow []string
	// Enforced turns the policy on. Off — the default, and every
	// configuration written before this existed — nothing is checked and
	// the transport is left exactly as it was.
	Enforced bool
}

// Permits reports whether host may be reached.
//
// Loopback is always permitted, and that is not a hole. A local model
// server is the case this project exists around, the daemon talks to
// itself, and a policy that broke both would be one nobody could turn
// on. Nothing leaves the machine through it.
func (p Policy) Permits(host string) bool {
	if !p.Enforced {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if isLoopback(host) {
		return true
	}
	for _, rule := range p.Allow {
		if matches(strings.ToLower(strings.TrimSpace(rule)), host) {
			return true
		}
	}
	return false
}

func matches(rule, host string) bool {
	switch {
	case rule == "":
		return false
	case rule == host:
		return true
	case strings.HasPrefix(rule, "*."):
		base := rule[2:]
		return host == base || strings.HasSuffix(host, "."+base)
	}
	return false
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// BlockedError is a connection this policy refused.
//
// Its own type so a caller can tell it from a network failure: "no route
// to host" and "this build is not allowed to reach that host" are
// different problems with different fixes, and a policy that looked like
// an outage would be debugged for hours.
type BlockedError struct{ Host string }

func (e *BlockedError) Error() string {
	return fmt.Sprintf("localcode is not allowed to connect to %s: it is not in the egress allow list "+
		"(network.egress in config.json). Loopback is always allowed.", e.Host)
}

var installOnce sync.Once

// Install applies p to this process's outbound HTTP.
//
// It replaces http.DefaultTransport's dialer rather than handing every
// caller a client of its own, because that is the one place all of them
// meet: the provider clients use http.DefaultClient, the update check
// builds a bare http.Client, and the MCP transport clones
// http.DefaultTransport. A policy applied at any one of those would be a
// policy with two ways around it.
//
// Once per process. Calling it again is a no-op, so a config reload
// cannot leave the transport wrapped twice.
func Install(p Policy) {
	if !p.Enforced {
		return
	}
	installOnce.Do(func() {
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return
		}
		http.DefaultTransport = Wrap(base, p)
	})
}

// Wrap is Install's one step, on a transport of the caller's choosing.
//
// Separate so it can be tested: Install is once per process by design,
// and a second test in the same binary would otherwise silently get the
// first one's policy.
func Wrap(base *http.Transport, p Policy) *http.Transport {
	t := base.Clone()
	dial := t.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		if !p.Permits(host) {
			// Before the lookup, not after: a refusal that first resolved
			// the name has already told a DNS server where this machine
			// was about to go.
			return nil, &BlockedError{Host: host}
		}
		return dial(ctx, network, addr)
	}
	return t
}
