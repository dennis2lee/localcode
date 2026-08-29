package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Updating from a plain address instead of GitHub.
//
// The difference the tests are really about: GitHub answers a question
// about releases, and a file share is a directory. The version has to
// come out of the filenames, because that is the only place it is
// written down.

// The three shapes an internal host actually serves, all read the same
// way. That is the point of scanning the body rather than parsing a
// format: nobody gets to say in advance which one it is.
func TestLatestFromURLReadsTheVersionOutOfTheFilenames(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		wantURL    string
	}{
		{
			name: "an ordinary directory index",
			body: `<html><body><h1>Index of /localcode</h1>
				<a href="localcode-0.65.0-darwin-universal.tar.gz">localcode-0.65.0-darwin-universal.tar.gz</a>
				<a href="localcode-0.65.0-windows-amd64.msi">localcode-0.65.0-windows-amd64.msi</a>
				</body></html>`,
			wantURL: "/dl/localcode-0.65.0-darwin-universal.tar.gz",
		},
		{
			name: "a listing whose links are absolute",
			body: `{"values":[
				{"name":"localcode-0.65.0-darwin-universal.tar.gz","links":{"self":{"href":"https://example.test/other/localcode-0.65.0-darwin-universal.tar.gz"}}},
				{"name":"localcode-0.65.0-windows-amd64.msi","links":{"self":{"href":"https://example.test/other/localcode-0.65.0-windows-amd64.msi"}}}]}`,
			wantURL: "https://example.test/other/localcode-0.65.0-darwin-universal.tar.gz",
		},
		{
			name:    "bare names, one per line",
			body:    "localcode-0.65.0-darwin-universal.tar.gz\nlocalcode-0.65.0-linux-amd64.deb\n",
			wantURL: "/dl/localcode-0.65.0-darwin-universal.tar.gz",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := Checker{Client: srv.Client()}

			rel, err := c.LatestFromURL(context.Background(), srv.URL+"/dl/")
			if err != nil {
				t.Fatalf("LatestFromURL: %v", err)
			}
			if rel.Version != "0.65.0" {
				t.Errorf("version = %q, want %q", rel.Version, "0.65.0")
			}
			asset, err := rel.AssetFor("darwin", "arm64", false)
			if err != nil {
				t.Fatalf("AssetFor: %v", err)
			}
			want := tt.wantURL
			if strings.HasPrefix(want, "/") {
				want = srv.URL + want
			}
			if asset.URL != want {
				t.Errorf("asset URL = %q, want %q", asset.URL, want)
			}
		})
	}
}

// update_url may point straight at one file, since the address is part of
// what gets read.
func TestUpdateURLMayNameTheFileItself(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("binary-ish"))
	}))
	defer srv.Close()
	c := Checker{Client: srv.Client()}

	rel, err := c.LatestFromURL(context.Background(), srv.URL+"/localcode-0.66.0-linux-amd64.tar.gz")
	if err != nil {
		t.Fatalf("LatestFromURL: %v", err)
	}
	if rel.Version != "0.66.0" {
		t.Errorf("version = %q, want %q", rel.Version, "0.66.0")
	}
}

// "There is only ever the latest file there" is a promise about how
// somebody keeps a directory, not something to depend on. A leftover from
// last month must not make localcode offer a downgrade.
func TestTheHighestVersionWinsWhenSeveralAreThere(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`localcode-0.9.0-linux-amd64.tar.gz
			localcode-0.10.0-linux-amd64.tar.gz
			localcode-0.10.0-windows-amd64.msi
			localcode-0.2.0-linux-amd64.tar.gz`))
	}))
	defer srv.Close()

	rel, err := Checker{Client: srv.Client()}.LatestFromURL(context.Background(), srv.URL+"/dl/")
	if err != nil {
		t.Fatalf("LatestFromURL: %v", err)
	}
	// 0.10.0 beats 0.9.0, which a string comparison gets backwards.
	if rel.Version != "0.10.0" {
		t.Errorf("version = %q, want %q", rel.Version, "0.10.0")
	}
	// And only that version's files are offered, so AssetFor cannot pick
	// an installer from a different release than the one being reported.
	for _, a := range rel.Assets {
		if !strings.Contains(a.Name, "0.10.0") {
			t.Errorf("release 0.10.0 carries %q, which is from another version", a.Name)
		}
	}
}

// A URL typed into config.json by hand is the common failure, so each way
// of getting it wrong has to say which way it was wrong.
func TestABadUpdateURLSaysWhatIsWrongWithIt(t *testing.T) {
	reachable := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("<html><body>nothing to see</body></html>"))
	}))
	defer reachable.Close()
	c := Checker{Client: reachable.Client()}

	for _, tt := range []struct{ name, url, want string }{
		{"not a url at all", "://nope", "not a URL"},
		{"no host", "https:///dl/", "names no host"},
		{"plain http", "http://example.test/dl/", "must be https"},
		{"answers an error", reachable.URL + "/missing", "404"},
		{"nothing that looks like an installer", reachable.URL + "/dl/", "looks like a localcode installer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.LatestFromURL(context.Background(), tt.url)
			if err == nil {
				t.Fatalf("LatestFromURL(%q) succeeded; it should refuse", tt.url)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

// http is refused rather than allowed with a warning. What this URL names
// is a file about to be run as an installer, and there is usually no
// checksum beside it, so the connection is the only thing that says the
// file came from the host somebody meant.
func TestPlainHTTPIsRefusedAndSaysWhy(t *testing.T) {
	_, err := Checker{}.LatestFromURL(context.Background(), "http://internal.example/dl/")
	if err == nil {
		t.Fatal("plain http was accepted")
	}
	if !strings.Contains(err.Error(), "installer") {
		t.Errorf("the refusal does not say what is at stake: %v", err)
	}
}

// A checksum beside the file is used when somebody published one, and its
// absence is reported rather than hidden.
func TestDigestForReadsASiblingChecksum(t *testing.T) {
	const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/good.tar.gz.sha256"):
			// The shape sha256sum writes: digest, then the filename.
			w.Write([]byte(sum + "  good.tar.gz\n"))
		case strings.HasSuffix(r.URL.Path, "/junk.tar.gz.sha256"):
			w.Write([]byte("not a checksum\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := Checker{Client: srv.Client()}

	if got := c.DigestFor(context.Background(), srv.URL+"/good.tar.gz"); got != "sha256:"+sum {
		t.Errorf("DigestFor = %q, want the published checksum", got)
	}
	if got := c.DigestFor(context.Background(), srv.URL+"/none.tar.gz"); got != "" {
		t.Errorf("DigestFor with no sidecar = %q, want empty so the caller can say it is unverified", got)
	}
	if got := c.DigestFor(context.Background(), srv.URL+"/junk.tar.gz"); got != "" {
		t.Errorf("DigestFor on a file that is not a checksum = %q, want empty rather than a bad digest", got)
	}
}

// Latest dispatches on the setting, so the check button, the install
// button and anything added later all follow it without each having to
// remember to.
func TestLatestFollowsTheConfiguredURL(t *testing.T) {
	mirror := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("localcode-9.9.9-linux-amd64.tar.gz"))
	}))
	defer mirror.Close()
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("GitHub was asked even though update_url is set")
		w.Write([]byte(`{"tag_name":"v0.0.1"}`))
	}))
	defer github.Close()

	rel, err := Checker{API: github.URL, URL: mirror.URL + "/dl/", Client: mirror.Client()}.
		Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Version != "9.9.9" {
		t.Errorf("version = %q, want the one from update_url", rel.Version)
	}
}
