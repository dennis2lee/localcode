package update

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// http to a private host is accepted, http to the public internet is
// refused. The resolver is a stub, so this table runs with no network:
// the names that resolve are answered below, and anything else does not
// exist.
func stubLookupIP(host string) ([]net.IP, error) {
	switch host {
	case "mirror.corp.example.com":
		return []net.IP{net.ParseIP("10.0.0.5")}, nil
	case "v6mirror.example.com":
		return []net.IP{net.ParseIP("fd00::5")}, nil
	case "downloads.example.com":
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	case "split.example.com":
		return []net.IP{net.ParseIP("10.0.0.5"), net.ParseIP("93.184.216.34")}, nil
	default:
		return nil, fmt.Errorf("lookup %s: no such host", host)
	}
}

func TestHTTPIsAcceptedOnlyOffThePublicInternet(t *testing.T) {
	accept := []string{
		// https is untouched by all of this.
		"https://example.com/dl/",
		"https://10.0.0.5/dl/",
		// Loopback, in its spellings.
		"http://localhost/dl/",
		"http://LOCALHOST/dl/",
		"http://localhost.:8080/dl/",
		"http://127.0.0.1/dl/",
		"http://127.0.0.99:8080/dl/",
		"http://[::1]/dl/",
		// The private ranges, v4 and v6.
		"http://10.1.2.3/dl/",
		"http://172.16.0.1/dl/",
		"http://172.31.255.254/dl/",
		"http://192.168.1.1/dl/",
		"http://[fd00::5]/dl/",
		"http://[fc00::1]/dl/",
		// Carrier-grade NAT, which an internal network may well use.
		"http://100.64.0.1/dl/",
		"http://100.127.255.254/dl/",
		// Link-local, v4 and v6.
		"http://169.254.10.20/dl/",
		"http://[fe80::1]/dl/",
		// Names that cannot be public.
		"http://mirror/dl/",
		"http://buildserver:8080/dl/",
		"http://print.local/dl/",
		"http://nas.internal/dl/",
		"http://wiki.intranet/dl/",
		"http://router.home.arpa/dl/",
		"http://home.arpa/dl/",
		"http://notinternal/dl/",
		// A public-looking name that resolves privately.
		"http://mirror.corp.example.com/dl/",
		"http://v6mirror.example.com/dl/",
		// Surrounding whitespace is the config typed by hand.
		"  http://mirror/dl/  ",
	}
	for _, raw := range accept {
		t.Run("accept "+raw, func(t *testing.T) {
			if _, err := checkedURLWithLookup(raw, stubLookupIP); err != nil {
				t.Errorf("checkedURLWithLookup(%q) refused: %v", raw, err)
			}
		})
	}

	refuse := []struct{ url, want string }{
		// A public literal, v4 and v6.
		{"http://93.184.216.34/dl/", "public internet"},
		{"http://[2606:2800:220:1:248:1893:25c8:1946]/dl/", "public internet"},
		// The addresses just outside each private range stay refused.
		{"http://11.0.0.1/dl/", "public internet"},
		{"http://172.15.255.255/dl/", "public internet"},
		{"http://172.32.0.1/dl/", "public internet"},
		{"http://192.167.1.1/dl/", "public internet"},
		{"http://100.128.0.1/dl/", "public internet"},
		{"http://169.253.1.1/dl/", "public internet"},
		// A suffix that merely ends like an internal one is not one.
		{"http://notinternal.example.com/dl/", "could not be resolved"},
		// A public name resolving publicly.
		{"http://downloads.example.com/dl/", "public internet"},
		// A name mixing private and public answers. One public address
		// means DNS splits the name across networks, and accepting would
		// let the public answer serve the installer half the time.
		{"http://split.example.com/dl/", "public internet"},
		// A name that will not resolve.
		{"http://no-such-host.invalid/dl/", "could not be resolved"},
		// A scheme that is neither http nor https.
		{"ftp://mirror/dl/", "must be https"},
		{"file:///dl/", "names no host"},
		// No host at all.
		{"http:///dl/", "names no host"},
		{"   ", "names no host"},
	}
	for _, tt := range refuse {
		t.Run("refuse "+tt.url, func(t *testing.T) {
			_, err := checkedURLWithLookup(tt.url, stubLookupIP)
			if err == nil {
				t.Fatalf("checkedURLWithLookup(%q) accepted; it should refuse", tt.url)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// The three refusals have to read differently: a public host, a name that
// will not resolve, and a scheme that is neither are three different
// problems, and "must be https" for all three would send somebody fixing
// DNS to check their scheme instead.
func TestHTTPRefusalsSayWhichProblemTheyAre(t *testing.T) {
	errs := map[string]error{}
	for name, raw := range map[string]string{
		"public http":       "http://93.184.216.34/dl/",
		"unresolvable host": "http://no-such-host.invalid/dl/",
		"other scheme":      "ftp://mirror/dl/",
	} {
		u, err := checkedURLWithLookup(raw, stubLookupIP)
		if err == nil {
			t.Fatalf("checkedURLWithLookup(%q) accepted into %v", raw, u)
		}
		errs[name] = err
	}
	markers := map[string]string{
		"public http":       "public internet",
		"unresolvable host": "could not be resolved",
		"other scheme":      "must be https",
	}
	for name, marker := range markers {
		if !strings.Contains(errs[name].Error(), marker) {
			t.Errorf("%s refusal = %q, want it to say %q", name, errs[name], marker)
		}
		for other, otherMarker := range markers {
			if other != name && strings.Contains(errs[name].Error(), otherMarker) {
				t.Errorf("%s refusal = %q, which also says %q (the %s problem)", name, errs[name], otherMarker, other)
			}
		}
	}
}

// https never reaches the resolver: no lookup, no new message, nothing.
// The lookup below fails the test if it is called at all.
func TestHTTPSNeverTouchesTheResolver(t *testing.T) {
	lookup := func(host string) ([]net.IP, error) {
		t.Errorf("resolver asked about %q for an https URL", host)
		return nil, fmt.Errorf("must not be called")
	}
	for _, raw := range []string{"https://example.com/dl/", "https://10.0.0.5/dl/"} {
		u, err := checkedURLWithLookup(raw, lookup)
		if err != nil {
			t.Errorf("checkedURLWithLookup(%q): %v", raw, err)
			continue
		}
		if u.String() != raw {
			t.Errorf("https URL came back changed: %q, want %q", u, raw)
		}
	}
}

// The user-observable end of the rule: a plain http mirror on loopback
// serves a release through the same entry point config.json uses.
func TestLatestFromURLAcceptsLoopbackHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "localcode-9.9.9-linux-amd64.tar.gz\n")
	}))
	defer srv.Close()

	rel, err := Checker{}.LatestFromURL(context.Background(), srv.URL+"/dl/")
	if err != nil {
		t.Fatalf("LatestFromURL over loopback http: %v", err)
	}
	if rel.Version != "9.9.9" {
		t.Errorf("version = %q, want %q", rel.Version, "9.9.9")
	}
}
