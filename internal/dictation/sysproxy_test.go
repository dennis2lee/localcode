package dictation

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The two shapes the registry value comes in, per WinINET: one proxy for
// everything, or one per scheme.
func TestParseProxyServer(t *testing.T) {
	cases := []struct {
		server, scheme, want string
	}{
		{"proxy.corp:8080", "http", "proxy.corp:8080"},
		{"proxy.corp:8080", "https", "proxy.corp:8080"},
		{"http=p1:8080;https=p2:8443;ftp=p3:21", "https", "p2:8443"},
		{"http=p1:8080;https=p2:8443", "http", "p1:8080"},
		// An https request on a setup that only names an http proxy still
		// goes through it.
		{"http=p1:8080", "https", "p1:8080"},
		{"", "http", ""},
		{"socks=s1:1080", "http", ""},
	}
	for _, tc := range cases {
		if got := parseProxyServer(tc.server, tc.scheme); got != tc.want {
			t.Errorf("parseProxyServer(%q, %q) = %q, want %q", tc.server, tc.scheme, got, tc.want)
		}
	}
}

// ProxyOverride is why "the proxy works in the browser" is not the whole
// story: hosts on this list are reached directly even with a proxy set,
// and getting it backwards would proxy loopback traffic to the corporate
// gateway.
func TestProxyBypass(t *testing.T) {
	override := "<local>;*.internal.corp;10.1.2.3;example.com"
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},         // <local>: no dot
		{"box.internal.corp", true}, // wildcard domain
		{"deep.box.internal.corp", true},
		{"internal.corp", true},   // the domain itself
		{"10.1.2.3", true},        // literal
		{"example.com", true},     // bare domain pattern
		{"sub.example.com", true}, // and everything under it
		{"notexample.com", false}, // suffix must be on a dot boundary
		{"ted-cat-avery.ssi.samsung.com", false},
	}
	for _, tc := range cases {
		if got := proxyBypassed(tc.host, override); got != tc.want {
			t.Errorf("proxyBypassed(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// A transcription goes through the environment's proxy. This is the whole
// point of the change: the target address here is unroutable, so the only
// way the request can succeed is by being sent to the proxy instead —
// which is what the browser on the same machine has been doing all along.
func TestTranscriptionGoesThroughTheEnvProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A forward proxy receives the absolute URL in the request line.
		if !r.URL.IsAbs() || r.URL.Host != "unroutable.invalid:9" {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"error":"not proxied: %s"}`, r.URL)
			return
		}
		fmt.Fprint(w, `{"text":"via the proxy"}`)
	}))
	defer proxy.Close()

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	p := &whisperProcess{host: "unroutable.invalid:9", log: &syncBuffer{}}
	got, err := p.transcribeVia(context.Background(), whisperAPIs[0], testAudio, "en")
	if err != nil {
		t.Fatalf("transcribe through the proxy: %v", err)
	}
	if !strings.Contains(got, "via the proxy") {
		t.Errorf("text = %q", got)
	}
}

// The registry branch must never capture loopback: the local whisper
// engine lives at 127.0.0.1, and WinINET exempts loopback without it ever
// appearing in ProxyOverride. This is tested through dictationProxy
// directly because the registry itself cannot be simulated here.
func TestLoopbackIsNeverProxied(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1:8080/inference", "http://localhost:9999/x"} {
		req, _ := http.NewRequest(http.MethodPost, target, nil)
		u, err := dictationProxy(req)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		if u != nil {
			t.Errorf("%s would be proxied through %s", target, u)
		}
	}
}
