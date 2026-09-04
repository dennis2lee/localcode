package provider

import (
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
	return &c
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
