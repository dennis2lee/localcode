package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseJSON is the shape GitHub answers /releases/latest with, cut down
// to the fields this reads.
func releaseJSON(tag string, assets ...Asset) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"tag_name":%q,"html_url":"https://github.com/o/r/releases/tag/%s","body":"notes here","published_at":"2026-08-15T00:00:00Z","assets":[`, tag, tag)
	for i, a := range assets {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":%q,"browser_download_url":%q,"size":%d,"digest":%q}`, a.Name, a.URL, a.Size, a.Digest)
	}
	b.WriteString("]}")
	return b.String()
}

func TestLatestReadsTheReleaseAndDropsTheV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/releases/latest" {
			t.Errorf("asked for %s", r.URL.Path)
		}
		fmt.Fprint(w, releaseJSON("v0.46.0", Asset{Name: "localcode-0.46.0-windows-amd64.msi", URL: "https://example/msi", Size: 12, Digest: "sha256:aa"}))
	}))
	defer srv.Close()

	rel, err := Checker{Repo: "o/r", API: srv.URL}.Latest(context.Background())
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if rel.Version != "0.46.0" || rel.Tag != "v0.46.0" {
		t.Errorf("version %q tag %q", rel.Version, rel.Tag)
	}
	if len(rel.Assets) != 1 || rel.Assets[0].Size != 12 {
		t.Errorf("assets = %+v", rel.Assets)
	}
}

// Rate limiting is what an anonymous check runs into, and "403" on its own
// sends someone looking for a permissions problem they do not have.
func TestRateLimitingSaysWhatItIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := Checker{Repo: "o/r", API: srv.URL}.Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rate limiting") {
		t.Fatalf("error = %v, want it to name the rate limit", err)
	}
}

func TestNewerComparesVersionsNumerically(t *testing.T) {
	for _, tt := range []struct {
		current, latest string
		want            bool
	}{
		{"0.45.2", "0.45.3", true},
		{"0.45.2", "v0.46.0", true},
		{"0.9.0", "0.10.0", true}, // not a string comparison
		{"0.45.2", "0.45.2", false},
		{"0.46.0", "0.45.2", false},
		{"1.0.0", "0.99.99", false},
		// A build from a working tree is not a version, and every release
		// is not "newer" than it.
		{"dev", "0.46.0", false},
		{"0.45.2", "", false},
		{"0.45.2", "nightly", false},
	} {
		if got := Newer(tt.current, tt.latest); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestAssetForPicksWhatThisPlatformCanInstall(t *testing.T) {
	rel := Release{
		Tag:     "v0.46.0",
		PageURL: "https://example/rel",
		Assets: []Asset{
			{Name: "LocalCode-0.46.0-darwin-universal-app.tar.gz"},
			{Name: "localcode-0.46.0-darwin-universal.tar.gz"},
			{Name: "localcode-0.46.0-windows-amd64.msi"},
			{Name: "localcode-0.46.0-windows-amd64.zip"},
			{Name: "localcode-0.46.0-windows-arm64.zip"},
			{Name: "localcode-0.46.0-linux-amd64.deb"},
			{Name: "localcode-0.46.0-linux-amd64.tar.gz"},
			{Name: "localcode-0.46.0-linux-arm64.deb"},
			{Name: "localcode-0.46.0-linux-arm64.tar.gz"},
		},
	}
	for _, tt := range []struct {
		goos, goarch string
		packaged     bool
		want         string
	}{
		// The installer, not the zip: it upgrades in place rather than
		// leaving two copies on the machine.
		{"windows", "amd64", false, "localcode-0.46.0-windows-amd64.msi"},
		{"windows", "arm64", false, "localcode-0.46.0-windows-arm64.zip"},
		// The desktop build lives in LocalCode.app and the command line one
		// does not; installing the wrong one leaves two localcodes and no
		// way to tell which is running.
		{"darwin", "arm64", true, "LocalCode-0.46.0-darwin-universal-app.tar.gz"},
		{"darwin", "amd64", false, "localcode-0.46.0-darwin-universal.tar.gz"},
		// Linux: the .deb only for a copy dpkg installed. A tarball
		// unpacked into ~/bin has to stay a tarball, or the update leaves
		// a second localcode that shadows the first depending on PATH.
		{"linux", "amd64", true, "localcode-0.46.0-linux-amd64.deb"},
		{"linux", "amd64", false, "localcode-0.46.0-linux-amd64.tar.gz"},
		{"linux", "arm64", true, "localcode-0.46.0-linux-arm64.deb"},
		{"linux", "arm64", false, "localcode-0.46.0-linux-arm64.tar.gz"},
	} {
		got, err := rel.AssetFor(tt.goos, tt.goarch, tt.packaged)
		if err != nil {
			t.Errorf("%s/%s: %v", tt.goos, tt.goarch, err)
			continue
		}
		if got.Name != tt.want {
			t.Errorf("%s/%s packaged=%v picked %q, want %q", tt.goos, tt.goarch, tt.packaged, got.Name, tt.want)
		}
	}

	if _, err := rel.AssetFor("freebsd", "amd64", false); err == nil {
		t.Error("a platform with no asset was offered one anyway")
	} else if !strings.Contains(err.Error(), rel.PageURL) {
		t.Errorf("the error does not say where to get it: %v", err)
	}
}

func assetServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadVerifiesWhatArrived(t *testing.T) {
	// An MSI body starts with the OLE compound document magic.
	body := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, []byte("this is an installer, honestly")...)
	sum := sha256.Sum256(body)
	srv := assetServer(t, body)

	dir := t.TempDir()
	a := Asset{
		Name:   "localcode-0.46.0-windows-amd64.msi",
		URL:    srv.URL,
		Size:   int64(len(body)),
		Digest: "sha256:" + hex.EncodeToString(sum[:]),
	}
	path, err := Download(context.Background(), srv.Client(), a, dir)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	// Under its real name, because an installer is chosen by its extension.
	if filepath.Base(path) != a.Name {
		t.Errorf("written as %q, want %q", filepath.Base(path), a.Name)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("contents = %q", got)
	}
}

// A connection dropped at 90% is the likely failure, and it produces an
// installer that opens, fails halfway, and leaves a broken install.
func TestDownloadRefusesAFileThatDoesNotMatch(t *testing.T) {
	// MSI magic up front, so the refusal below is the checksum and the
	// size, not the signature.
	body := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, []byte("truncated")...)
	srv := assetServer(t, body)
	dir := t.TempDir()

	wrongSum := Asset{Name: "x.msi", URL: srv.URL, Size: int64(len(body)), Digest: "sha256:" + strings.Repeat("00", 32)}
	if _, err := Download(context.Background(), srv.Client(), wrongSum, dir); err == nil {
		t.Error("a file whose checksum was wrong was accepted")
	}
	wrongSize := Asset{Name: "x.msi", URL: srv.URL, Size: 9999}
	if _, err := Download(context.Background(), srv.Client(), wrongSize, dir); err == nil {
		t.Error("a truncated file was accepted")
	}
	// And nothing is left behind under a name anything would run.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".msi") {
			t.Errorf("a rejected download was left at %s", e.Name())
		}
	}
}

// countingBody is a response body of left zero bytes that counts how many
// were read from it, which is how much of it a download took in.
type countingBody struct{ left, read int64 }

func (b *countingBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	clear(p)
	b.left -= int64(len(p))
	b.read += int64(len(p))
	return len(p), nil
}

func (b *countingBody) Close() error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// bodyClient answers every request with body, and no network between.
func bodyClient(body io.ReadCloser) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{}, Body: body, Request: r}, nil
	})}
}

// A server that sends more than the file can be is refused once it has
// sent one byte too many, and not after the rest has reached the disk:
// past the size the release gives, and past the ceiling for a file whose
// size it does not give.
func TestDownloadStopsReadingPastTheFilesSize(t *testing.T) {
	for _, tt := range []struct {
		name    string
		size    int64
		ceiling int64
		want    string
	}{
		{"a known size", 16, maxDownload, "more than the 16 bytes the release says"},
		{"an unknown size", 0, 1 << 10, "more than 1024 bytes"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := &countingBody{left: 8 << 20}
			dir := t.TempDir()
			a := Asset{Name: "localcode-0.46.0-windows-amd64.msi", URL: "https://github.com/o/r/releases/download/v0.46.0/localcode-0.46.0-windows-amd64.msi", Size: tt.size}
			_, err := download(context.Background(), bodyClient(body), a, dir, tt.ceiling)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to say %q", err, tt.want)
			}
			limit := tt.ceiling
			if tt.size > 0 {
				limit = tt.size
			}
			if body.read > limit+1 {
				t.Errorf("read %d bytes of a file that can be at most %d", body.read, limit)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("a refused download left %d files behind", len(entries))
			}
		})
	}
}

// A checksum in an algorithm localcode does not know is not the same as no
// checksum: the file is about to be run.
func TestDownloadRefusesAChecksumItCannotCheck(t *testing.T) {
	// MSI magic up front, so the refusal below is the unknown algorithm,
	// not the signature.
	body := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, []byte("hello")...)
	srv := assetServer(t, body)
	a := Asset{Name: "x.msi", URL: srv.URL, Size: int64(len(body)), Digest: "sha512:beef"}
	if _, err := Download(context.Background(), srv.Client(), a, t.TempDir()); err == nil {
		t.Error("an unverifiable download was accepted")
	}
}

// An archive that is not one, handed to the platform with no installer:
// nothing is run, nothing is replaced, and the answer says where the file
// is. The archive that *is* one, over an install this user owns, is
// selfinstall_test.go's subject.
func TestApplyOnlyRunsAnInstallerItHasOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "localcode-0.46.0-darwin-universal.tar.gz")
	if err := os.WriteFile(path, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "localcode")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := apply(path, func() (string, error) { return exe, nil }, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Started {
		t.Error("an archive was treated as something that installs itself")
	}
	if !strings.Contains(out.Detail, path) {
		t.Errorf("the answer does not say where the file is: %q", out.Detail)
	}
	if body, err := os.ReadFile(exe); err != nil || string(body) != "old" {
		t.Errorf("the install was touched: %q %v", body, err)
	}
}

func TestApplyReportsAMissingDownload(t *testing.T) {
	if _, err := Apply(filepath.Join(t.TempDir(), "nothing-here.msi")); err == nil {
		t.Error("installing a file that is not there succeeded")
	}
}

// A .deb is not run either, and for a different reason from the archive:
// installing it needs root. localcode does not ask for root and does not
// drive a package manager on someone's behalf, so the answer is the file
// and the one command that installs it.
func TestApplyHandsBackTheCommandForADebianPackage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "localcode-0.46.0-linux-amd64.deb")
	if err := os.WriteFile(path, []byte("package"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Apply(path)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Started {
		t.Error("localcode tried to install a Debian package itself")
	}
	if !strings.Contains(out.Detail, "apt install "+path) {
		t.Errorf("the answer does not give the install command: %q", out.Detail)
	}
}

// A build with no version stamped into it must never look older than a
// release.
//
// This became load-bearing when updating moved to startup. "go build -o
// localcode ./cmd/localcode" leaves version at its default "dev", and if
// that compared as older than the newest tag, the first run of every
// development build would download a release and exec into it — replacing
// the binary somebody had just compiled, with no way to tell that is what
// happened. The same goes for any string that is not a version: the safe
// reading of "I do not know what I am" is "not older than anything".
func TestAnUnversionedBuildIsNeverOlderThanARelease(t *testing.T) {
	// Strings that are not versions at all. "0.0.0-dirty" is deliberately
	// not in this list: it parses, 0.0.0 really is older than 9.9.9, and
	// asserting otherwise would be asking the comparison to be wrong.
	for _, current := range []string{"dev", "", "unknown", "devel", "not a version"} {
		if Newer(current, "9.9.9") {
			t.Errorf("Newer(%q, \"9.9.9\") = true; a build that cannot say what version it is would replace itself on startup", current)
		}
	}
	// And the ordinary case still works, or the test above passes for the
	// wrong reason.
	if !Newer("0.84.1", "0.85.0") {
		t.Error("Newer(\"0.84.1\", \"0.85.0\") = false; the comparison itself is broken")
	}
}
