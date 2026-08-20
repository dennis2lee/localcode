package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	body := []byte("this is an installer, honestly")
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
	body := []byte("truncated")
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

// A checksum in an algorithm localcode does not know is not the same as no
// checksum: the file is about to be run.
func TestDownloadRefusesAChecksumItCannotCheck(t *testing.T) {
	body := []byte("hello")
	srv := assetServer(t, body)
	a := Asset{Name: "x.msi", URL: srv.URL, Size: int64(len(body)), Digest: "sha512:beef"}
	if _, err := Download(context.Background(), srv.Client(), a, t.TempDir()); err == nil {
		t.Error("an unverifiable download was accepted")
	}
}

// Everything that is not a Windows installer is a file and an instruction.
// Unpacking an archive over a running install is how an update leaves half
// of two versions on a machine.
func TestApplyOnlyRunsAnInstallerItHasOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "localcode-0.46.0-darwin-universal.tar.gz")
	if err := os.WriteFile(path, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Apply(path)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Started {
		t.Error("an archive was treated as something that installs itself")
	}
	if !strings.Contains(out.Detail, path) {
		t.Errorf("the answer does not say where the file is: %q", out.Detail)
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
