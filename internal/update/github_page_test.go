package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// pageFile is one row of a release's asset list.
type pageFile struct {
	name   string
	digest string // "" for a row that shows none
}

// expandedAssets is the asset list github.com serves at
// /releases/expanded_assets/<tag>, cut down to its shape: one row per
// file, the link first, the SHA-256 further along the row twice (as text
// and as the copy button's value), then the size and the date. The two
// source archives come last, as they do on every release.
func expandedAssets(repo, tag string, files ...pageFile) string {
	var b strings.Builder
	b.WriteString(`<div class="Box Box--condensed mt-3"><ul>`)
	for _, f := range files {
		fmt.Fprintf(&b, `<li class="Box-row d-flex flex-column flex-md-row"><div class="d-flex flex-justify-start col-12 col-lg-6">
  <a href="/%s/releases/download/%s/%s" rel="nofollow" data-turbo="false" class="wb-break-all"><span class="Truncate-text text-bold">%s</span></a></div>`,
			repo, tag, f.name, f.name)
		if f.digest != "" {
			fmt.Fprintf(&b, `
  <span class="Truncate text-mono text-small color-fg-muted"><span class="Truncate-text">%s</span></span>
  <clipboard-copy aria-label="Copy to clipboard digest for %s" value="%s"></clipboard-copy>`, f.digest, f.name, f.digest)
		}
		b.WriteString(`
  <span class="color-fg-muted text-right">12.7 MB</span><span><relative-time datetime="2026-08-15T00:00:00Z">Aug 15</relative-time></span></li>`)
	}
	fmt.Fprintf(&b, `<li class="Box-row"><a href="/%s/archive/refs/tags/%s.zip" rel="nofollow">Source code (zip)</a></li>`, repo, tag)
	fmt.Fprintf(&b, `<li class="Box-row"><a href="/%s/archive/refs/tags/%s.tar.gz" rel="nofollow">Source code (tar.gz)</a></li>`, repo, tag)
	b.WriteString(`</ul></div>`)
	return b.String()
}

func sha256Of(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// resetServer accepts each connection and closes it before answering,
// which is what the reported network did to api.github.com.
func resetServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeGitHub is github.com for one release: /releases/latest redirects to
// the tag, the asset list links the files, and each file downloads.
// hits counts every request, which is how a test tells that the fallback
// was not tried.
type fakeGitHub struct {
	srv   *httptest.Server
	hits  atomic.Int32
	files map[string][]byte
}

func newFakeGitHub(t *testing.T, repo, tag string, rows []pageFile, files map[string][]byte) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{files: files}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.hits.Add(1)
		switch {
		case r.URL.Path == "/"+repo+"/releases/latest":
			http.Redirect(w, r, "/"+repo+"/releases/tag/"+tag, http.StatusFound)
		case r.URL.Path == "/"+repo+"/releases/tag/"+tag:
			t.Error("the redirect to the release page was followed; only its Location is needed")
			fmt.Fprint(w, "<html>the release page</html>")
		case r.URL.Path == "/"+repo+"/releases/expanded_assets/"+tag:
			fmt.Fprint(w, expandedAssets(repo, tag, rows...))
		case strings.HasPrefix(r.URL.Path, "/"+repo+"/releases/download/"+tag+"/"):
			body, ok := g.files[strings.TrimPrefix(r.URL.Path, "/"+repo+"/releases/download/"+tag+"/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// The reported network: api.github.com is reset and github.com is not.
// The release comes from github.com's pages, with the SHA-256 the page
// prints for each file, and the download is checked against it exactly
// as it would be against the API's.
func TestAnUnreachableAPIFallsBackToGitHubsPages(t *testing.T) {
	msi := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, []byte("an installer")...)
	zip := append([]byte{'P', 'K', 0x03, 0x04}, []byte("an archive")...)
	g := newFakeGitHub(t, "o/r", "v0.46.0", []pageFile{
		{name: "localcode-0.46.0-windows-amd64.msi", digest: sha256Of(msi)},
		{name: "localcode-0.46.0-windows-amd64.zip", digest: sha256Of(zip)},
	}, map[string][]byte{
		"localcode-0.46.0-windows-amd64.msi": msi,
		"localcode-0.46.0-windows-amd64.zip": zip,
	})

	rel, err := Checker{Repo: "o/r", API: resetServer(t).URL, Web: g.srv.URL}.Latest(context.Background())
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if rel.Version != "0.46.0" || rel.Tag != "v0.46.0" || !rel.FromPage {
		t.Errorf("release = %+v, want v0.46.0 read from the pages", rel)
	}
	if want := g.srv.URL + "/o/r/releases/tag/v0.46.0"; rel.PageURL != want {
		t.Errorf("page = %q, want %q", rel.PageURL, want)
	}
	if len(rel.Assets) != 2 {
		t.Fatalf("assets = %+v, want the two files and not the source archives", rel.Assets)
	}

	a, err := rel.AssetFor("windows", "amd64", false)
	if err != nil {
		t.Fatalf("asset: %v", err)
	}
	if a.Digest != sha256Of(msi) {
		t.Errorf("digest = %q, want the one the page printed", a.Digest)
	}
	path, err := Download(context.Background(), nil, a, t.TempDir())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(msi) {
		t.Errorf("downloaded %q, %v", got, err)
	}

	// The handoff's zip is held to the same check.
	if z, err := rel.HandoffAssetFor("windows", "amd64", false); err != nil || z.Digest != sha256Of(zip) {
		t.Errorf("handoff asset = %+v, %v", z, err)
	}
}

// The fallback does not weaken the check: a file that does not match the
// SHA-256 the page printed is refused, as it would be from the API.
func TestAFileFromThePagesIsStillCheckedAgainstItsSHA256(t *testing.T) {
	msi := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, []byte("an installer")...)
	other := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, []byte("something else")...)
	g := newFakeGitHub(t, "o/r", "v0.46.0", []pageFile{
		{name: "localcode-0.46.0-windows-amd64.msi", digest: sha256Of(msi)},
	}, map[string][]byte{
		"localcode-0.46.0-windows-amd64.msi": other,
	})
	rel, err := Checker{Repo: "o/r", API: resetServer(t).URL, Web: g.srv.URL}.Latest(context.Background())
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	a, err := rel.AssetFor("windows", "amd64", false)
	if err != nil {
		t.Fatalf("asset: %v", err)
	}
	if _, err := Download(context.Background(), nil, a, t.TempDir()); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("a file that does not match the page's SHA-256 was not refused by it: %v", err)
	}
}

// A file the page shows no SHA-256 for is reported and not installed. A
// GitHub release always has one, so its absence is a page that was not
// read correctly, and the page itself is where to get the file.
func TestAFileThePagesGiveNoChecksumForIsNotInstalled(t *testing.T) {
	g := newFakeGitHub(t, "o/r", "v0.46.0", []pageFile{
		{name: "localcode-0.46.0-windows-amd64.msi"},
		{name: "localcode-0.46.0-windows-amd64.zip"},
		{name: "localcode-0.46.0-linux-amd64.tar.gz", digest: sha256Of([]byte("x"))},
	}, nil)
	rel, err := Checker{Repo: "o/r", API: resetServer(t).URL, Web: g.srv.URL}.Latest(context.Background())
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	// The release is still reported: knowing a newer version exists is
	// worth saying even when it cannot be installed from here.
	if rel.Version != "0.46.0" {
		t.Errorf("version = %q", rel.Version)
	}
	for name, pick := range map[string]func() (Asset, error){
		"install": func() (Asset, error) { return rel.AssetFor("windows", "amd64", false) },
		"handoff": func() (Asset, error) { return rel.HandoffAssetFor("windows", "amd64", false) },
	} {
		_, err := pick()
		if !errors.Is(err, ErrUnchecked) {
			t.Fatalf("%s: err = %v, want ErrUnchecked", name, err)
		}
		for _, want := range []string{"0.46.0", "SHA-256", "api.github.com", rel.PageURL} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not say %q: %v", name, want, err)
			}
		}
	}
	// One file without a checksum does not stop the others.
	if _, err := rel.AssetFor("linux", "amd64", false); err != nil {
		t.Errorf("a file with a checksum was refused: %v", err)
	}
}

// The API path is unchanged: a release GitHub recorded no checksums for
// is still offered, checked by size, as before. Only a release read from
// the pages, which give no size, needs the checksum.
func TestTheAPIPathStillOffersAFileWithNoDigest(t *testing.T) {
	rel := Release{Version: "0.46.0", Tag: "v0.46.0", Assets: []Asset{{Name: "localcode-0.46.0-windows-amd64.msi", Size: 10}}}
	if _, err := rel.AssetFor("windows", "amd64", false); err != nil {
		t.Errorf("an API release without digests was refused: %v", err)
	}
}

// An HTTP status is GitHub answering, a rate limit included, and never a
// reason to ask the pages instead.
func TestAnAPIThatAnswersIsNeverFallenBackFrom(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			t.Cleanup(api.Close)
			g := newFakeGitHub(t, "o/r", "v0.46.0", nil, nil)
			if _, err := (Checker{Repo: "o/r", API: api.URL, Web: g.srv.URL}).Latest(context.Background()); err == nil {
				t.Fatal("expected the status to be reported")
			}
			if n := g.hits.Load(); n != 0 {
				t.Errorf("github.com was asked %d times after the API answered %d", n, status)
			}
		})
	}
}

// An API pointed somewhere else is not GitHub, and without Web there is
// no github.com to fall back to: a test pointed at a dead API must never
// reach the real one.
func TestAnOverriddenAPIHasNoFallbackUnlessOneIsNamed(t *testing.T) {
	if _, ok := (Checker{API: "http://127.0.0.1:1"}).web(); ok {
		t.Error("an overridden API falls back to github.com")
	}
	if web, ok := (Checker{}).web(); !ok || web != "https://github.com" {
		t.Errorf("web() = %q, %v; want github.com when nothing is overridden", web, ok)
	}
	if web, ok := (Checker{API: "http://127.0.0.1:1", Web: "http://127.0.0.1:2/"}).web(); !ok || web != "http://127.0.0.1:2" {
		t.Errorf("web() = %q, %v; want the named address", web, ok)
	}

	_, err := Checker{Repo: "o/r", API: resetServer(t).URL}.Latest(context.Background())
	if err == nil {
		t.Fatal("a reset API was reported as answering")
	}
	if strings.Contains(err.Error(), "github.com instead") {
		t.Errorf("the check fell back without a Web address: %v", err)
	}
}

// When github.com cannot be read either, both failures are said, and the
// advice still follows.
func TestAFailedFallbackSaysBothFailures(t *testing.T) {
	_, err := Checker{Repo: "o/r", API: resetServer(t).URL, Web: resetServer(t).URL}.Latest(context.Background())
	if err == nil {
		t.Fatal("expected a failure")
	}
	msg := err.Error()
	for _, want := range []string{"ask GitHub for the latest release", "github.com instead failed too", "ask github.com for the latest release", "update_url"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not say %q: %s", want, msg)
		}
	}
}

// github.com answering something other than a redirect to a tag is a
// failure that says what it got, not a release with an empty version.
func TestThePagesMustRedirectToATag(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"a page instead of a redirect": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>releases</html>") },
		"a redirect elsewhere":         func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/login", http.StatusFound) },
		"not found":                    http.NotFound,
	} {
		t.Run(name, func(t *testing.T) {
			web := httptest.NewServer(handler)
			t.Cleanup(web.Close)
			_, err := Checker{Repo: "o/r", API: resetServer(t).URL, Web: web.URL}.Latest(context.Background())
			if err == nil {
				t.Fatal("expected a failure")
			}
		})
	}
}

// The latest tag is read only from a redirect to this repository's own
// release on the host that was asked. One anywhere else names a release
// that is not this program's, and is reported without any user name or
// password its address carries.
func TestTheLatestTagMustBeThisRepositorysOwn(t *testing.T) {
	var location atomic.Value
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", location.Load().(string))
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(web.Close)
	host := strings.TrimPrefix(web.URL, "http://")

	for _, tt := range []struct {
		location string
		want     string // "" means refused
	}{
		{"/o/r/releases/tag/v1.2.3", "v1.2.3"},
		{"/O/R/releases/tag/v1.2.3", "v1.2.3"},
		{web.URL + "/o/r/releases/tag/v1.2.3", "v1.2.3"},
		{"https://example.com/o/r/releases/tag/v9.9.9", ""},
		{"//example.com/o/r/releases/tag/v9.9.9", ""},
		{"https://" + host + "/o/r/releases/tag/v9.9.9", ""},
		{"/other/repo/releases/tag/v9.9.9", ""},
		{"/o/r/other/releases/tag/v9.9.9", ""},
		{"/x/o/r/releases/tag/v9.9.9", ""},
		{"/o/r/releases/tag/", ""},
		{"/o/r/releases/tag/v9.9.9/assets", ""},
		{"http://alice:s3cret@example.com/o/r/releases/tag/v9.9.9", ""},
	} {
		location.Store(tt.location)
		tag, err := Checker{Repo: "o/r"}.latestTag(context.Background(), web.URL)
		switch {
		case tt.want != "" && (err != nil || tag != tt.want):
			t.Errorf("%s: tag = %q, %v; want %s", tt.location, tag, err, tt.want)
		case tt.want == "" && err == nil:
			t.Errorf("%s: tag = %q, want the redirect refused", tt.location, tag)
		case tt.want == "" && (strings.Contains(err.Error(), "alice") || strings.Contains(err.Error(), "s3cret")):
			t.Errorf("%s: the refusal carries the credentials: %v", tt.location, err)
		}
	}
}

// The asset list read row by row: each file gets the checksum on its own
// row, and nothing the page links that is not one of this release's
// files becomes an asset.
func TestAssetsFromPageReadsEachRowsChecksum(t *testing.T) {
	a := "sha256:" + strings.Repeat("a1", 32)
	b := "sha256:" + strings.Repeat("b2", 32)
	body := expandedAssets("o/r", "v1.2.3",
		pageFile{name: "localcode-1.2.3-windows-amd64.msi", digest: a},
		pageFile{name: "localcode-1.2.3-linux-amd64.deb"},
		pageFile{name: "localcode-1.2.3-darwin-universal.tar.gz", digest: b},
	) +
		// Links that are not this release's files.
		`<a href="/o/r/releases/download/v1.2.2/localcode-1.2.2-windows-amd64.msi">older</a>` +
		`<a href="https://elsewhere.example.com/o/r/releases/download/v1.2.3/localcode-1.2.3-windows-arm64.zip">elsewhere</a>` +
		`<a href="/other/repo/releases/download/v1.2.3/localcode-1.2.3-linux-arm64.deb">another repo</a>`
	base, _ := url.Parse("https://github.com/o/r/releases/expanded_assets/v1.2.3")
	got := assetsFromPage(base, "o/r", "v1.2.3", body)

	want := []Asset{
		{Name: "localcode-1.2.3-windows-amd64.msi", URL: "https://github.com/o/r/releases/download/v1.2.3/localcode-1.2.3-windows-amd64.msi", Digest: a},
		{Name: "localcode-1.2.3-linux-amd64.deb", URL: "https://github.com/o/r/releases/download/v1.2.3/localcode-1.2.3-linux-amd64.deb"},
		{Name: "localcode-1.2.3-darwin-universal.tar.gz", URL: "https://github.com/o/r/releases/download/v1.2.3/localcode-1.2.3-darwin-universal.tar.gz", Digest: b},
	}
	if len(got) != len(want) {
		t.Fatalf("assets = %+v, want %d", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("asset %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A row with two different checksums in it gives none: which one is the
// file's is a guess, and a guess is not a check.
func TestARowWithTwoChecksumsGivesNone(t *testing.T) {
	a := "sha256:" + strings.Repeat("a1", 32)
	b := "sha256:" + strings.Repeat("b2", 32)
	body := `<a href="/o/r/releases/download/v1.2.3/localcode-1.2.3-windows-amd64.msi">x</a> ` + a + " " + b
	base, _ := url.Parse("https://github.com/o/r/releases/expanded_assets/v1.2.3")
	got := assetsFromPage(base, "o/r", "v1.2.3", body)
	if len(got) != 1 || got[0].Digest != "" {
		t.Errorf("assets = %+v, want one with no digest", got)
	}

	// And a file linked twice keeps a checksum only while the rows agree.
	twice := `<a href="/o/r/releases/download/v1.2.3/f.msi">x</a> ` + a +
		`<a href="/o/r/releases/download/v1.2.3/f.msi">x</a> ` + a
	if got := assetsFromPage(base, "o/r", "v1.2.3", twice); len(got) != 1 || got[0].Digest != a {
		t.Errorf("assets = %+v, want one with the agreed digest", got)
	}
	disagree := `<a href="/o/r/releases/download/v1.2.3/f.msi">x</a> ` + a +
		`<a href="/o/r/releases/download/v1.2.3/f.msi">x</a> ` + b +
		`<a href="/o/r/releases/download/v1.2.3/f.msi">x</a> ` + a
	if got := assetsFromPage(base, "o/r", "v1.2.3", disagree); len(got) != 1 || got[0].Digest != "" {
		t.Errorf("assets = %+v, want one with no digest once two rows disagree", got)
	}
}

// A row ends at the next link of any kind, as on github.com, where each
// file's SHA-256 follows its link and the source archives come last with
// none. A checksum past the next link, the asset pattern's or not, is
// never this file's.
func TestARowEndsAtTheNextLink(t *testing.T) {
	a := "sha256:" + strings.Repeat("a1", 32)
	b := "sha256:" + strings.Repeat("b2", 32)
	base, _ := url.Parse("https://github.com/o/r/releases/expanded_assets/v1.2.3")

	// A checksum after the source archives' links is not the last file's.
	trailing := expandedAssets("o/r", "v1.2.3", pageFile{name: "localcode-1.2.3-windows-amd64.msi", digest: a}) + " " + b
	if got := assetsFromPage(base, "o/r", "v1.2.3", trailing); len(got) != 1 || got[0].Digest != a {
		t.Errorf("assets = %+v, want the one file with its own digest", got)
	}

	// A link the asset pattern does not read still ends the row before it.
	unread := `<a href="/o/r/releases/download/v1.2.3/first.msi">first.msi</a>` +
		`<a href='/o/r/releases/download/v1.2.3/second.msi'>second.msi</a> ` + b
	if got := assetsFromPage(base, "o/r", "v1.2.3", unread); len(got) != 1 || got[0].Digest != "" {
		t.Errorf("assets = %+v, want first.msi with no digest", got)
	}
}

// A file linked over plain http from an https page is not one of the
// page's files: the download would leave the TLS the page came over.
func TestAssetsFromPageKeepsThePagesScheme(t *testing.T) {
	a := "sha256:" + strings.Repeat("a1", 32)
	base, _ := url.Parse("https://github.com/o/r/releases/expanded_assets/v1.2.3")
	body := `<a href="http://github.com/o/r/releases/download/v1.2.3/localcode-1.2.3-windows-amd64.msi">x</a> ` + a
	if got := assetsFromPage(base, "o/r", "v1.2.3", body); len(got) != 0 {
		t.Errorf("assets = %+v, want none from a plain http link", got)
	}
}
