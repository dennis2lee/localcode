package update

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// update_url takes http anywhere, and the address has nothing to do with it.
//
// It was https-only once, and then https plus http to an address that could
// not be on the public internet. That second rule was wrong in the way
// inferred rules usually are: a closed network does not have to use the
// private ranges, and the deployment this was written for serves its mirror
// on publicly-allocated space, which no address check can tell from the
// internet. The rule refused the exact case it existed to allow.
//
// So the rows below are every shape of address the old rule cared about, and
// every one of them is accepted. Written out rather than reduced to "http is
// fine", because the point is that the distinction is gone.
func TestHTTPIsAcceptedWhateverTheAddress(t *testing.T) {
	for _, raw := range []string{
		"http://localhost:7990/dl/",
		"http://127.0.0.1/dl/",
		"http://[::1]/dl/",
		"http://10.0.0.5/dl/",
		"http://192.168.1.10/dl/",
		"http://172.16.0.1/dl/",
		"http://100.64.0.1/dl/",
		"http://169.254.1.1/dl/",
		"http://mirror/dl/",
		"http://mirror.internal/dl/",
		// Publicly-allocated space, used inside a closed network. The
		// reported case: a Bitbucket mirror on 105.128.44.10:7990.
		"http://105.128.44.10:7990/projects/TCAT/repos/ted-mirror/browse/LocalCode",
		"http://93.184.216.34/dl/",
		"http://downloads.example.com/dl/",
		"https://example.com/dl/",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := checkedURL(raw); err != nil {
				t.Errorf("checkedURL(%q) refused it: %v", raw, err)
			}
		})
	}
}

// What is still refused is what cannot be fetched at all, and each says
// which of the two it is rather than one message for both.
func TestAnUnusableUpdateURLSaysWhichProblemItIs(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"a scheme nothing can fetch", "ftp://mirror.internal/dl/", "must be http or https"},
		{"no host at all", "http:///dl/", "names no host"},
		{"not a URL", "http://%zz/dl/", "is not a URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := checkedURL(tc.raw)
			if err == nil {
				t.Fatalf("checkedURL(%q) accepted it", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("checkedURL(%q) said %q, which does not say %q", tc.raw, err, tc.want)
			}
		})
	}
}

// No name is resolved to decide any of this. A resolver that fails the test
// when called pins that: the decision is the scheme and nothing else, so a
// mirror on a network with no DNS for its own name still works.
func TestNothingIsResolvedToDecideTheScheme(t *testing.T) {
	called := false
	lookup := func(string) ([]net.IP, error) {
		called = true
		return nil, fmt.Errorf("the resolver was consulted")
	}
	for _, raw := range []string{"http://downloads.example.com/dl/", "https://example.com/dl/"} {
		if _, err := checkedURLWithLookup(raw, lookup); err != nil {
			t.Errorf("checkedURLWithLookup(%q) refused it: %v", raw, err)
		}
	}
	if called {
		t.Error("a name was resolved to decide whether the URL is usable; the scheme is the whole decision")
	}
}

// End to end over plain http: a listing is read and the release comes back,
// with no address rule in the way.
func TestLatestFromURLReadsAnHTTPMirror(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="localcode-0.129.0-windows-amd64.msi">msi</a>
<a href="localcode-0.129.0-linux-amd64.tar.gz">tgz</a>`)
	}))
	defer srv.Close()

	rel, err := Checker{}.LatestFromURL(t.Context(), srv.URL+"/")
	if err != nil {
		t.Fatalf("LatestFromURL over http: %v", err)
	}
	if rel.Version != "0.129.0" {
		t.Errorf("version = %q, want 0.129.0", rel.Version)
	}
	if len(rel.Assets) != 2 {
		t.Errorf("assets = %d, want the two published", len(rel.Assets))
	}
}

// A Bitbucket Server raw directory, which is what a mirror on an internal
// Bitbucket actually looks like.
//
// The listing is git's own tree output — mode, type, object id, then the
// name — with no links at all, and the directory carries the ref it is
// being read at. Both halves matter: the names have to be found in text
// that was never meant as a page, and the "at" has to reach each file, or
// the download asks for a path on whatever the default branch happens to
// be.
func TestABitbucketRawDirectoryIsReadAndItsRefIsKept(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.String())
		fmt.Fprint(w, "100644 blob 83dbdeaeb2deada3fcc18e5119ada299756e5e0f\tlocalcode-0.129.0-linux-amd64.tar.gz\n"+
			"100644 blob 4c887c00c5f7a5e654add8bb8f5e2ecf7445e1a7\tlocalcode-0.129.0-windows-amd64.msi\n")
	}))
	defer srv.Close()

	rel, err := Checker{}.LatestFromURL(t.Context(), srv.URL+"/projects/TCAT/repos/ted-mirror/raw/LocalCode/?at=refs/heads/master")
	if err != nil {
		t.Fatalf("LatestFromURL against a raw directory: %v", err)
	}
	if rel.Version != "0.129.0" {
		t.Fatalf("version = %q, want 0.129.0", rel.Version)
	}
	if len(rel.Assets) != 2 {
		t.Fatalf("assets = %d, want 2: the tree listing is not a page and its names still have to be found", len(rel.Assets))
	}
	for _, a := range rel.Assets {
		u, err := url.Parse(a.URL)
		if err != nil {
			t.Fatalf("asset %s has an unparseable URL %q: %v", a.Name, a.URL, err)
		}
		if !strings.HasSuffix(u.Path, "/raw/LocalCode/"+a.Name) {
			t.Errorf("asset %s downloads from %q, which is not the file beside the listing", a.Name, u.Path)
		}
		if got := u.Query().Get("at"); got != "refs/heads/master" {
			t.Errorf("asset %s downloads at %q, want refs/heads/master: the listing's ref did not reach the file", a.Name, got)
		}
	}
}

// And a listing with no query of its own is untouched, which is every
// plain directory index.
func TestAPlainDirectoryGainsNoQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="localcode-0.129.0-windows-amd64.msi">msi</a>`)
	}))
	defer srv.Close()

	rel, err := Checker{}.LatestFromURL(t.Context(), srv.URL+"/dl/")
	if err != nil {
		t.Fatalf("LatestFromURL: %v", err)
	}
	if len(rel.Assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(rel.Assets))
	}
	if strings.Contains(rel.Assets[0].URL, "?") {
		t.Errorf("asset URL %q gained a query the listing never had", rel.Assets[0].URL)
	}
}
