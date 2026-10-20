package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
		// The test's API is http, and an http request reads HTTP_PROXY.
		sysproxy.Sources("http"),
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

// The message names the route the request took, not the one a lookup
// gives afterwards. A request that went direct because the lookup was
// slow is said to have gone direct, even when the lookup has answered
// with a proxy by the time the error is written.
func TestTheMessageNamesTheRouteTheRequestTook(t *testing.T) {
	late := sysproxy.Choice{Note: "the system proxy lookup did not answer within 5s"}
	answered := sysproxy.Choice{Proxy: &url.URL{Scheme: "http", Host: "proxy.example.com:8080"}, From: "the Windows proxy script"}
	var calls atomic.Int32
	old := proxyFor
	proxyFor = func(context.Context, *url.URL) (sysproxy.Choice, error) {
		if calls.Add(1) == 1 {
			return late, nil
		}
		return answered, nil
	}
	t.Cleanup(func() { proxyFor = old })

	_, err := Checker{Repo: "o/r", API: resetServer(t).URL}.Latest(context.Background())
	if err == nil {
		t.Fatal("a reset connection was reported as an answer")
	}
	if !strings.Contains(err.Error(), "Tried "+late.String()+".") {
		t.Errorf("the message does not name the route the request took, %s: %v", late, err)
	}
	if strings.Contains(err.Error(), "proxy.example.com") {
		t.Errorf("the message names a proxy the request never went through: %v", err)
	}
}

// A proxy setting that was refused is reported as it is: the request was
// never sent, so there is nothing to advise on, and github.com would be
// refused the same way.
func TestARefusedProxySettingIsNotAdvisedOnOrRetried(t *testing.T) {
	old := proxyFor
	proxyFor = func(context.Context, *url.URL) (sysproxy.Choice, error) {
		return sysproxy.Choice{From: "HTTPS_PROXY"}, errors.New("HTTPS_PROXY is not a proxy address localcode can read")
	}
	t.Cleanup(func() { proxyFor = old })

	g := newFakeGitHub(t, "o/r", "v0.46.0", nil, nil)
	_, err := Checker{Repo: "o/r", API: "http://api.example.com", Web: g.srv.URL}.Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTPS_PROXY is not a proxy address") {
		t.Fatalf("err = %v, want the refused setting", err)
	}
	if strings.Contains(err.Error(), "Tried") {
		t.Errorf("a request that was never sent was advised on: %v", err)
	}
	if n := g.hits.Load(); n != 0 {
		t.Errorf("github.com was asked %d times after the proxy setting was refused", n)
	}
}

// hangingServer takes each request and never answers it, which is what a
// network that drops packets looks like from here.
func hangingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A check that ran out of its caller's time, as the startup check can, was
// not refused by the network: it says so and nothing else, and github.com
// is not asked.
func TestACheckPastItsDeadlineIsNotAdvisedOnOrRetried(t *testing.T) {
	g := newFakeGitHub(t, "o/r", "v0.46.0", nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := Checker{Repo: "o/r", API: hangingServer(t).URL, Web: g.srv.URL}.Latest(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if strings.Contains(err.Error(), "Tried") {
		t.Errorf("a check past its deadline was advised on: %v", err)
	}
	if n := g.hits.Load(); n != 0 {
		t.Errorf("github.com was asked %d times after the check ran out of time", n)
	}
}

// The client's own timeout is not the caller's deadline: the context is
// still live, no answer in that time is a network that dropped the
// request, and github.com is asked instead.
func TestAClientTimeoutIsANetworkThatDidNotAnswer(t *testing.T) {
	msi := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, []byte("an installer")...)
	g := newFakeGitHub(t, "o/r", "v0.46.0", []pageFile{
		{name: "localcode-0.46.0-windows-amd64.msi", digest: sha256Of(msi)},
	}, nil)
	// The same timeout covers the fallback's own requests, so it is long
	// enough for those to finish on a loaded machine.
	client := &http.Client{Timeout: 2 * time.Second, Transport: transport()}
	rel, err := Checker{Repo: "o/r", API: hangingServer(t).URL, Web: g.srv.URL, Client: client}.Latest(context.Background())
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if !rel.FromPage {
		t.Errorf("release = %+v, want it read from the pages", rel)
	}
}

// An egress refusal of the github.com fallback is still an egress refusal
// to anybody who asks the error: the policy's words are not lost behind
// the first failure.
func TestAnEgressRefusalOfTheFallbackCanBeFound(t *testing.T) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("the default transport is not an *http.Transport here")
	}
	// Loopback is always allowed, so the API is reached and reset, and the
	// fallback's host is the one refused.
	http.DefaultTransport = egress.Wrap(base, egress.Policy{Enforced: true})
	t.Cleanup(func() { http.DefaultTransport = base })
	withProxy(t, sysproxy.Choice{})

	_, err := Checker{Repo: "o/r", API: resetServer(t).URL, Web: "http://web.example.com"}.Latest(context.Background())
	var blocked *egress.BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("err = %v, want the egress refusal of the fallback", err)
	}
	if blocked.Host != "web.example.com" {
		t.Errorf("refused host = %q, want web.example.com", blocked.Host)
	}
	if !strings.Contains(err.Error(), "github.com instead failed too") {
		t.Errorf("the message does not say the fallback failed: %v", err)
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

// After a redirect, the request that failed is the last hop, and the
// message names that hop's host beside that hop's route, not the first
// request's host with the last one's route.
func TestAFailedRedirectNamesTheHopThatFailed(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/moved", http.StatusFound)
	}))
	t.Cleanup(proxy.Close)
	pu, _ := url.Parse(proxy.URL)
	old := proxyFor
	proxyFor = func(_ context.Context, u *url.URL) (sysproxy.Choice, error) {
		if u.Hostname() == "api.example.com" {
			return sysproxy.Choice{Proxy: pu, From: "HTTPS_PROXY"}, nil
		}
		return sysproxy.Choice{Note: "the second hop goes direct"}, nil
	}
	t.Cleanup(func() { proxyFor = old })

	_, err := Checker{Repo: "o/r", API: "http://api.example.com"}.Latest(context.Background())
	if err == nil {
		t.Fatal("the check succeeded against a redirect to a closed port")
	}
	if msg := err.Error(); strings.Contains(msg, "connection to api.example.com") || !strings.Contains(msg, "127.0.0.1:1") {
		t.Errorf("the message does not name the hop that failed: %v", err)
	}
}

// egress checks a proxied request's destination as well as its proxy: a
// loopback proxy, which the policy always allows, is not a way past it.
func TestEgressSeesTheDestinationBehindAProxy(t *testing.T) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("the default transport is not an *http.Transport here")
	}
	http.DefaultTransport = egress.Wrap(base, egress.Policy{Enforced: true, Allow: []string{"models.example.com"}})
	t.Cleanup(func() { http.DefaultTransport = base })
	var asked atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9","html_url":"x","assets":[]}`))
	}))
	t.Cleanup(proxy.Close)
	pu, _ := url.Parse(proxy.URL)
	withProxy(t, sysproxy.Choice{Proxy: pu, From: "the Windows proxy settings"})

	_, err := Checker{Repo: "o/r", API: "http://api.blocked.example.com"}.Latest(context.Background())
	var blocked *egress.BlockedError
	if !errors.As(err, &blocked) || blocked.Host != "api.blocked.example.com" {
		t.Errorf("a destination outside the allow list went through the proxy: %v", err)
	}
	if asked.Load() != 0 {
		t.Error("the proxy was asked for it")
	}
}
