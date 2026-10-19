package provider

import (
	"errors"
	"fmt"
	"net/http"

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
	c.CheckRedirect = refuseDowngrade
	return &c
}

// refuseDowngrade is the redirect policy: Go's own, ten redirects at most,
// and no redirect from https to plain http. Go forwards the Authorization
// header to a redirect on the same host whatever its scheme, so an https
// endpoint that redirected to http sent the API key or the JWT across the
// network unencrypted.
func refuseDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("refused a redirect from https to %s: the request carries a credential, and plain http would send it unencrypted", req.URL.Redacted())
	}
	return nil
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
