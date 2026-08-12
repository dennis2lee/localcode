package dictation

import (
	"context"
	"fmt"
	"net"
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

// A realistic PAC body: per-URL logic localcode cannot run, with the
// proxies named inside it as the extractable part.
func TestExtractPACProxies(t *testing.T) {
	pac := `function FindProxyForURL(url, host) {
	  if (shExpMatch(host, "*.internal.corp")) return "DIRECT";
	  if (isInNet(host, "10.0.0.0", "255.0.0.0")) return "PROXY proxy-a.corp:8080; PROXY proxy-b.corp:8080; DIRECT";
	  return "PROXY proxy-a.corp:8080";
	}`
	got := extractPACProxies(pac)
	want := []string{"proxy-a.corp:8080", "proxy-b.corp:8080"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("extractPACProxies = %v, want %v", got, want)
	}
}

// The situation the PAC path exists for, end to end: the registry has no
// fixed proxy, a middlebox kills every direct payload, the browser works
// because a PAC script sends it through a proxy — and the probe's job is
// to come back with that proxy's name.
func TestProbeFindsTheProxyNamedByThePAC(t *testing.T) {
	// A port where TCP connects and the first byte closes the connection:
	// what the middlebox looks like from this side.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				buf := make([]byte, 1)
				c.Read(buf)
				c.Close()
			}()
		}
	}()
	target := ln.Addr().String()

	// The proxy the PAC names, which can reach the "server".
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() || r.URL.Host != target {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"status":"running"}`)
	}))
	defer proxy.Close()

	pacServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `function FindProxyForURL(url, host) { return "PROXY %s; DIRECT"; }`,
			strings.TrimPrefix(proxy.URL, "http://"))
	}))
	defer pacServer.Close()

	old := autoConfigURL
	autoConfigURL = func() string { return pacServer.URL }
	defer func() { autoConfigURL = old }()

	res, err := Probe(context.Background(), Config{WhisperURL: "http://" + target})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	summary := res.Summary()
	if !strings.Contains(summary, strings.TrimPrefix(proxy.URL, "http://")) {
		t.Errorf("the summary does not name the working proxy:\n%s", summary)
	}
	if !strings.Contains(summary, "HTTPS_PROXY") {
		t.Errorf("the summary does not say how to use it:\n%s", summary)
	}
}

// WPAD is the proxy mechanism that leaves no URL anywhere localcode can
// read: "automatically detect settings", on by default, resolved through
// DNS. A machine can be proxying every browser request while the fixed
// proxy, the environment and AutoConfigURL are all empty — which is
// exactly the shape the reporting laptop turned out to have.
func TestProbeFindsTheProxyViaWPAD(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				buf := make([]byte, 1)
				c.Read(buf)
				c.Close()
			}()
		}
	}()
	target := ln.Addr().String()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() || r.URL.Host != target {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"status":"running"}`)
	}))
	defer proxy.Close()

	wpad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wpad.dat" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `function FindProxyForURL(url, host) { return "PROXY %s"; }`,
			strings.TrimPrefix(proxy.URL, "http://"))
	}))
	defer wpad.Close()

	oldPAC := autoConfigURL
	autoConfigURL = func() string { return "" } // registry is empty, as reported
	oldWPAD := wpadCandidates
	wpadCandidates = func() []string { return []string{wpad.URL + "/wpad.dat"} }
	defer func() { autoConfigURL = oldPAC; wpadCandidates = oldWPAD }()

	res, err := Probe(context.Background(), Config{WhisperURL: "http://" + target})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !strings.Contains(res.Summary(), strings.TrimPrefix(proxy.URL, "http://")) {
		t.Errorf("the summary does not name the WPAD-discovered proxy:\n%s", res.Summary())
	}
}

// A web server on the wpad name that answers with an error page is not a
// PAC, and treating it as one would send the probe chasing proxies out of
// an HTML 404.
func TestWPADIgnoresAnswersThatAreNotPACScripts(t *testing.T) {
	notPAC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body>404 Not Found</body></html>`)
	}))
	defer notPAC.Close()

	oldWPAD := wpadCandidates
	wpadCandidates = func() []string { return []string{notPAC.URL + "/wpad.dat"} }
	defer func() { wpadCandidates = oldWPAD }()

	if url, _ := fetchWPAD(context.Background()); url != "" {
		t.Errorf("an HTML error page was accepted as a PAC script: %s", url)
	}
}
