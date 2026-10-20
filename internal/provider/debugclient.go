package provider

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"

	"localcode/internal/debuglog"
)

// debugClient is the HTTP client every provider that speaks HTTP is built
// with: the default one, wrapped so that a request whose context carries
// a debug sink is written to it.
//
// Installed always rather than swapped in when "/debug-log" is on. The
// transport does nothing at all without a sink on the context, so the
// cost is one type assertion per request, and the alternative — building
// providers differently depending on a switch that moves at runtime —
// would mean a turn admitted before the switch moved talking through a
// client that cannot log, which is the class of bug the Smart Agent pin
// exists to prevent.
func debugClient() *http.Client {
	c := *http.DefaultClient
	c.Transport = debuglog.Transport{Base: http.DefaultTransport}
	c.CheckRedirect = checkRedirect
	return &c
}

// checkRedirect is the redirect policy every HTTP provider is built with:
// refuseDowngrade, and the headers a provider's config adds withheld from
// any host but the one they were configured for.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if err := refuseDowngrade(req, via); err != nil {
		return err
	}
	withholdOffHost(req, via)
	return nil
}

// clientOr is c, or for a provider built without one, the client a
// constructor would have given it. Not http.DefaultClient: that one has
// neither the debug log nor the redirect policy, so a configured header
// would follow a redirect to another host.
func clientOr(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return debugClient()
}

// refuseDowngrade is the half of the redirect policy that refuses: Go's
// own, ten redirects at most, and no redirect from https to plain http.
// Go forwards the Authorization header to a redirect on the same host
// whatever its scheme, so an https endpoint that redirected to http sent
// the API key or the JWT across the network unencrypted.
func refuseDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("refused a redirect from https to %s: the request carries a credential, and plain http would send it unencrypted", req.URL.Redacted())
	}
	return nil
}

// withholdOffHost takes the headers a provider's config added off a
// redirect that has left the host they were sent to.
//
// Go does that for Authorization and cookies and for nothing else: every
// other header of the first request is copied onto the next one, wherever
// it goes. So a gateway's second credential, which the key's own header
// would not have carried there, went to whatever host a 307 named.
//
// By host and port, which is stricter than Go's rule for Authorization (a
// subdomain keeps it), with the port a URL leaves out read as its
// scheme's: example.com and example.com:443 are one place over https, and
// http://example.com:80 and https://example.com are two. And once the
// chain has left the host the headers stay off, even on a hop that comes
// back, as Go's own stripping does: the host in between chose where the
// request went next.
func withholdOffHost(req *http.Request, via []*http.Request) {
	names := debuglog.SecretHeaders(req.Context())
	if len(names) == 0 {
		return
	}
	home := hostPort(via[0].URL)
	left := hostPort(req.URL) != home
	for _, r := range via[1:] {
		if hostPort(r.URL) != home {
			left = true
		}
	}
	if !left {
		return
	}
	for _, name := range names {
		req.Header.Del(name)
	}
}

// hostPort is u's host, lower-cased, and its port, with the scheme's
// default when the URL names none.
func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// loggingAWSClient wraps the HTTP client the AWS config resolved so a
// debug log sees the request and the response, without replacing it.
//
// Replacing it is what v0.98.0 did and it broke config loading outright
// for anyone with a custom CA bundle: the loader asserts the concrete
// awshttp.BuildableClient to add roots to, and a plain *http.Client
// fails that assertion. Wrapping keeps whatever the loader built.
func loggingAWSClient(inner aws.HTTPClient) aws.HTTPClient {
	if inner == nil {
		return debugClient()
	}
	return awsLogging{inner: inner}
}

type awsLogging struct{ inner aws.HTTPClient }

func (c awsLogging) Do(req *http.Request) (*http.Response, error) {
	return debuglog.Transport{Base: doerTransport{c.inner}}.RoundTrip(req)
}

// doerTransport presents an aws.HTTPClient as the RoundTripper the debug
// transport wraps. The two interfaces have the same shape and different
// names, which is the whole of the adapter.
type doerTransport struct{ c aws.HTTPClient }

func (d doerTransport) RoundTrip(req *http.Request) (*http.Response, error) { return d.c.Do(req) }
