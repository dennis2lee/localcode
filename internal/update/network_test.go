package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"localcode/internal/egress"
	"localcode/internal/sysproxy"
)

// withProxy makes every request in this package go through the proxy c
// names, as though a machine's settings had named it.
func withProxy(t *testing.T, c sysproxy.Choice) {
	t.Helper()
	old := proxyFor
	proxyFor = func(context.Context, *url.URL) (sysproxy.Choice, error) { return c, nil }
	t.Cleanup(func() { proxyFor = old })
}

// The reported failure, said so somebody can act on it: what happened,
// that no proxy was used, and the two ways around it.
func TestAResetCheckSaysItWentDirectAndWhatToDo(t *testing.T) {
	api := resetServer(t)
	_, err := Checker{Repo: "o/r", API: api.URL}.Latest(context.Background())
	if err == nil {
		t.Fatal("a reset connection was reported as an answer")
	}
	host := strings.TrimPrefix(api.URL, "http://")
	msg := err.Error()
	for _, want := range []string{
		"ask GitHub for the latest release",
		"Tried with no proxy",
		"did not let a direct connection to " + host + " through",
		sysproxy.Sources,
		"update_url",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not say %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "..") {
		t.Errorf("the message doubles a full stop: %s", msg)
	}
}

// Through a proxy, the message names it, by host and port and never with
// its credentials, so "no proxy was found" and "the proxy refused it"
// read differently.
func TestACheckThroughAProxyNamesTheProxy(t *testing.T) {
	proxy := resetServer(t)
	p, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.User = url.UserPassword("alice", "s3cret")
	withProxy(t, sysproxy.Choice{Proxy: p, From: "the Windows proxy settings"})

	_, err = Checker{Repo: "o/r", API: "http://api.example.com"}.Latest(context.Background())
	if err == nil {
		t.Fatal("a proxy that closed the connection was reported as an answer")
	}
	msg := err.Error()
	for _, want := range []string{
		"through proxy " + p.Host + " (from the Windows proxy settings)",
		"The proxy did not carry the connection to api.example.com",
		"HTTPS_PROXY",
		"update_url",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not say %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "alice") || strings.Contains(msg, "s3cret") {
		t.Errorf("the message carries the proxy's credentials: %s", msg)
	}
}

// A proxy that wants a sign-in is told apart from one that refused: the
// fix is credentials in HTTPS_PROXY, not a different proxy.
func TestAProxyThatWantsASignInSaysSo(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Proxy-Authenticate", `Negotiate`)
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	t.Cleanup(proxy.Close)
	p, _ := url.Parse(proxy.URL)
	withProxy(t, sysproxy.Choice{Proxy: p, From: "the Windows proxy script"})

	// https, so the proxy is asked to CONNECT, which is where a sign-in
	// is refused. The name is never resolved: the proxy is dialled.
	_, err := Checker{Repo: "o/r", API: "https://api.example.com"}.Latest(context.Background())
	if err == nil {
		t.Fatal("a 407 from the proxy was reported as an answer")
	}
	msg := err.Error()
	for _, want := range []string{"asked for a sign-in", "http://user:password@" + p.Host, "update_url"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not say %q: %s", want, msg)
		}
	}
}

// A certificate this machine does not trust is what a proxy that
// inspects TLS presents when its root is not installed, and the message
// says that rather than leaving "unknown authority" to be looked up.
func TestAnUntrustedCertificateSaysWhatUsuallyCausesIt(t *testing.T) {
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a server this machine does not trust was answered")
	}))
	t.Cleanup(api.Close)
	_, err := Checker{Repo: "o/r", API: api.URL}.Latest(context.Background())
	if err == nil {
		t.Fatal("an untrusted certificate was accepted")
	}
	if !strings.Contains(err.Error(), "inspects TLS") || !strings.Contains(err.Error(), "system certificate store") {
		t.Errorf("the message does not explain the certificate: %v", err)
	}
}

// A check its caller gave up on was not refused by anything: no advice,
// and no second request to github.com.
func TestACancelledCheckIsNotAdvisedOnOrRetried(t *testing.T) {
	g := newFakeGitHub(t, "o/r", "v0.46.0", nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Checker{Repo: "o/r", API: resetServer(t).URL, Web: g.srv.URL}.Latest(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if strings.Contains(err.Error(), "Tried") {
		t.Errorf("a cancelled check was advised on: %v", err)
	}
	if n := g.hits.Load(); n != 0 {
		t.Errorf("github.com was asked %d times after the check was cancelled", n)
	}
}

// The transport is rebuilt from http.DefaultTransport when that has been
// replaced, so the egress policy egress.Install puts there applies to
// every update request. An egress refusal is this machine's own policy:
// it is reported in its own words and not routed around.
func TestUpdateRequestsFollowTheEgressPolicy(t *testing.T) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("the default transport is not an *http.Transport here")
	}
	http.DefaultTransport = egress.Wrap(base, egress.Policy{Enforced: true})
	t.Cleanup(func() { http.DefaultTransport = base })

	g := newFakeGitHub(t, "o/r", "v0.46.0", nil, nil)
	_, err := Checker{Repo: "o/r", API: "http://api.example.com", Web: g.srv.URL}.Latest(context.Background())
	var blocked *egress.BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("err = %v, want the egress refusal", err)
	}
	if strings.Contains(err.Error(), "Tried") {
		t.Errorf("an egress refusal was advised on as a network failure: %v", err)
	}
	if n := g.hits.Load(); n != 0 {
		t.Errorf("github.com was asked %d times after egress refused the API", n)
	}
}
