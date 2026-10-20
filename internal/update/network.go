package update

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"localcode/internal/egress"
	"localcode/internal/sysproxy"
)

// How this package reaches the network, and what it says when it cannot.
//
// Every request here, to GitHub, to github.com and to an update_url, goes
// through one transport whose proxy is chosen by sysproxy: the
// environment first, then on Windows the proxy in Internet Options, which
// a browser follows and Go does not. A machine whose browser opens GitHub
// through a proxy is the one case this exists for: before it, the check
// went direct, was reset by the network, and said only that.

// proxyFor chooses the proxy for one request. A variable so a test can
// name a proxy without a Windows machine or an environment to read it
// from, which http.ProxyFromEnvironment reads once per process.
var proxyFor = sysproxy.For

// shared is the transport, and the http.DefaultTransport it was cloned
// from. It is rebuilt when the default has been replaced, which is what
// egress.Install does: the egress check is a dialer on the default
// transport, and a clone taken before it would be a way around it.
var shared struct {
	mu   sync.Mutex
	base http.RoundTripper
	t    *http.Transport
}

func transport() http.RoundTripper {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.t != nil && shared.base == http.DefaultTransport {
		return shared.t
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	t := base.Clone()
	t.Proxy = func(req *http.Request) (*url.URL, error) {
		c, err := proxyFor(req.Context(), req.URL)
		if r, ok := req.Context().Value(routeKey{}).(*route); ok {
			r.set(c, err)
		}
		return c.Proxy, err
	}
	shared.base, shared.t = http.DefaultTransport, t
	return t
}

// route is how the transport sent a request, kept for the error that
// says so. Asking proxyFor again afterwards is not the same thing: a
// lookup that had not answered when the request went direct may have
// answered since, and the error would name a proxy that was never used.
//
// A redirect is another request with the same context, so what is kept
// is the last one sent, which is the one that failed. A client of the
// caller's own never fills it in, and its request reads as direct.
type route struct {
	mu     sync.Mutex
	choice sysproxy.Choice
	err    error
}

type routeKey struct{}

// withRoute returns ctx carrying a route, which the transport fills in
// for each request made with it.
func withRoute(ctx context.Context) (context.Context, *route) {
	r := &route{}
	return context.WithValue(ctx, routeKey{}, r), r
}

func (r *route) set(c sysproxy.Choice, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.choice, r.err = c, err
}

func (r *route) get() (sysproxy.Choice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.choice, r.err
}

// noAnswer reports whether err, from a client's Do, is a request that got
// no HTTP answer at all: refused, reset, timed out, a name that did not
// resolve, a TLS handshake that failed, a proxy that would not carry it.
// Those are the failures a different address or a proxy can fix, and the
// only ones the github.com fallback is tried for. An HTTP status is an
// answer, and is never one of these.
//
// Two are left out. A request whose caller's context ended, cancelled
// or past its deadline, was not refused by anything: the startup check's
// four seconds running out says nothing about the network. A client's own
// Timeout is not that, since the context is still live, and no answer in
// that time is a network that dropped the request. And an egress refusal
// is this machine's own policy, which says what it is in its own words
// and is not for a fallback to route around.
func noAnswer(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var blocked *egress.BlockedError
	return !errors.As(err, &blocked)
}

// unreachableError is a request to GitHub that got no answer, said with
// how it was tried and what usually fixes it.
type unreachableError struct {
	// host is the address that did not answer: api.github.com. scheme is
	// the request's, which decides the variable a proxy is set in.
	host, scheme string
	// via is how the transport sent the request. See route.
	via sysproxy.Choice
	err error
	// fallback is how reading the release from github.com failed, when
	// that was tried too.
	fallback error
}

// Unwrap is both failures, so errors.As finds an egress refusal of the
// fallback as well as anything in the first.
func (e *unreachableError) Unwrap() []error {
	if e.fallback == nil {
		return []error{e.err}
	}
	return []error{e.err, e.fallback}
}

func (e *unreachableError) Error() string {
	var b strings.Builder
	b.WriteString(sentence(e.err))
	fmt.Fprintf(&b, " Tried %s.", e.via)
	if e.fallback != nil {
		fmt.Fprintf(&b, " Reading the release from github.com instead failed too: %s", sentence(e.fallback))
	}
	b.WriteString(" ")
	b.WriteString(unreachableHint(e.host, e.scheme, e.via, e.err))
	return b.String()
}

// sentence is an error's text ending in exactly one full stop. Windows
// ends its socket errors with one and Go's own errors do not.
func sentence(err error) string {
	return strings.TrimSuffix(err.Error(), ".") + "."
}

// updateURLHint is the way around every one of these, said last in each.
const updateURLHint = "update_url in config.json points the check at another place where the installers are published."

// unreachableHint says what a request that got no answer usually means,
// and what to do about it. One case each for the three things somebody
// can act on differently: a certificate this machine does not trust, a
// proxy that wants a sign-in, and a connection refused with or without a
// proxy, which is told apart because "no proxy was found" and "the proxy
// refused it" have different fixes.
func unreachableHint(host, scheme string, via sysproxy.Choice, err error) string {
	var unknown x509.UnknownAuthorityError
	switch {
	case errors.As(err, &unknown):
		// Go verifies with the operating system's own store on Windows
		// and macOS, so a root certificate installed there for the
		// browser is trusted here too. Getting this means it is not.
		return fmt.Sprintf("The certificate presented for %s is not one this machine trusts, which is what a proxy that inspects TLS presents when its root certificate is not in the system certificate store; %s", host, updateURLHint)
	case via.Proxy != nil && strings.Contains(err.Error(), "Proxy Authentication Required"):
		return fmt.Sprintf("The proxy asked for a sign-in, and localcode can only answer with a user name and password written into HTTPS_PROXY, as in http://user:password@%s, and not with the account this machine is signed in to; %s", via.Proxy.Host, updateURLHint)
	case via.Proxy != nil:
		return fmt.Sprintf("The proxy did not carry the connection to %s. If a browser here uses another proxy, set HTTPS_PROXY to that one; %s", host, updateURLHint)
	}
	return fmt.Sprintf("The network did not let a direct connection to %s through. If a browser here reaches GitHub through a proxy, set it in %s, which localcode follows; %s", host, sysproxy.Sources(scheme), updateURLHint)
}
