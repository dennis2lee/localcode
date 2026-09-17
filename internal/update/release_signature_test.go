package update

import (
	"bytes"
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

// A browse page served where an installer should be: the file names are in
// it, but it is a page, not the file. This is what a Bitbucket Server
// browse URL answers with when the update_url should have named raw.
const browsePageBody = `<!DOCTYPE html><html><head><title>localcode-0.131.0-windows-amd64.msi</title></head><body>browse view</body></html>`

// pageServer answers every asset URL with an HTML page, the way a
// misconfigured update_url does.
func pageServer(t *testing.T, contentType string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		fmt.Fprint(w, browsePageBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// typedServer answers with body under contentType, whatever the bytes are.
func typedServer(t *testing.T, body []byte, contentType string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// goodBody returns bytes starting with the signature the name promises,
// followed by filler.
func goodBody(t *testing.T, name string) []byte {
	t.Helper()
	_, sig, ok := expectedSignature(name)
	if !ok {
		t.Fatalf("no signature for %q", name)
	}
	return append(append([]byte{}, sig...), bytes.Repeat([]byte{0x61}, 64)...)
}

// A page downloaded as an installer is refused before it is renamed into
// place, and the refusal says what arrived and why.
func TestDownloadRefusesAPageServedAsAnInstaller(t *testing.T) {
	srv := pageServer(t, "text/html; charset=utf-8")
	dir := t.TempDir()
	a := Asset{
		Name: "localcode-0.131.0-windows-amd64.msi",
		URL:  srv.URL + "/localcode-0.131.0-windows-amd64.msi",
		Size: int64(len(browsePageBody)),
	}
	_, err := Download(context.Background(), srv.Client(), a, dir)
	if err == nil {
		t.Fatal("an HTML page served as an .msi was accepted")
	}
	msg := err.Error()
	for _, want := range []string{
		a.Name,
		fmt.Sprint(len(browsePageBody)),
		"text/html",
		"<!DOCTYPE",
		"browse",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q: %q", want, msg)
		}
	}
	// Nothing was renamed into place and no temp file was left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("a refused download left %s behind", e.Name())
		if strings.HasSuffix(e.Name(), ".msi") {
			t.Errorf("a refused download was left under an installer name: %s", e.Name())
		}
	}
}

// The signature is what is true: correct bytes are accepted even when the
// server labels them text/plain, which ordinary file servers do.
func TestDownloadTrustsBytesNotContentType(t *testing.T) {
	for _, name := range []string{
		"localcode-1.2.3-windows-amd64.msi",
		"localcode-1.2.3-windows-amd64.zip",
		"localcode-1.2.3-darwin-universal.tar.gz",
		"localcode-1.2.3-linux-amd64.deb",
	} {
		t.Run(name, func(t *testing.T) {
			body := goodBody(t, name)
			srv := typedServer(t, body, "text/plain")
			a := Asset{Name: name, URL: srv.URL, Size: int64(len(body))}
			path, err := Download(context.Background(), srv.Client(), a, t.TempDir())
			if err != nil {
				t.Fatalf("correct %s bytes under text/plain were refused: %v", name, err)
			}
			if filepath.Base(path) != name {
				t.Errorf("written as %q, want %q", filepath.Base(path), name)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, body) {
				t.Errorf("contents changed in transit")
			}
		})
	}
}

// Each container served under another container's name is refused: the
// check is per format, not a single "is this binary" sniff.
func TestDownloadRefusesEachFormatWithTheWrongSignature(t *testing.T) {
	wrong := map[string][]byte{
		"localcode-1.2.3-windows-amd64.msi":       {'P', 'K', 0x03, 0x04, 0x61, 0x61},
		"localcode-1.2.3-windows-amd64.zip":       {0x1F, 0x8B, 0x61, 0x61},
		"localcode-1.2.3-darwin-universal.tar.gz": {'!', '<', 'a', 'r', 'c', 'h', '>', '\n'},
		"localcode-1.2.3-linux-amd64.deb":         {0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1},
	}
	for name, body := range wrong {
		t.Run(name, func(t *testing.T) {
			srv := typedServer(t, body, "application/octet-stream")
			a := Asset{Name: name, URL: srv.URL, Size: int64(len(body))}
			if _, err := Download(context.Background(), srv.Client(), a, t.TempDir()); err == nil {
				t.Errorf("%s bytes served as %s were accepted", "another container", name)
			}
		})
	}
}

// The name-to-signature table, asserted directly: four containers, each
// with fixed magic at byte zero, and a name that matches none of them.
func TestExpectedSignatureMapsNamesToLeadingBytes(t *testing.T) {
	for _, tt := range []struct {
		name  string
		label string
		sig   []byte
		ok    bool
	}{
		{"localcode-0.131.0-windows-amd64.msi", "MSI", []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, true},
		{"localcode-0.131.0-windows-amd64.zip", "zip", []byte{'P', 'K', 0x03, 0x04}, true},
		{"localcode-0.131.0-darwin-universal.tar.gz", "gzip", []byte{0x1F, 0x8B}, true},
		{"LocalCode-0.131.0-darwin-universal-app.tar.gz", "gzip", []byte{0x1F, 0x8B}, true},
		{"localcode-0.131.0-linux-amd64.deb", "ar", []byte{'!', '<', 'a', 'r', 'c', 'h', '>', '\n'}, true},
		{"localcode-0.131.0-linux-amd64.rpm", "", nil, false},
		{"localcode-0.131.0-darwin-universal.tar", "", nil, false},
		{"README", "", nil, false},
	} {
		label, sig, ok := expectedSignature(tt.name)
		if ok != tt.ok {
			t.Errorf("expectedSignature(%q) ok = %v, want %v", tt.name, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if label != tt.label || !bytes.Equal(sig, tt.sig) {
			t.Errorf("expectedSignature(%q) = (%q, %x), want (%q, %x)",
				tt.name, label, sig, tt.label, tt.sig)
		}
	}
}

// A name with no known container skips the signature check: there is
// nothing to check it against, and the download still succeeds.
func TestDownloadAcceptsANameWithNoKnownSignature(t *testing.T) {
	body := []byte(browsePageBody)
	srv := typedServer(t, body, "text/html; charset=utf-8")
	a := Asset{Name: "localcode-0.131.0-checksums.txt", URL: srv.URL, Size: int64(len(body))}
	if _, err := Download(context.Background(), srv.Client(), a, t.TempDir()); err != nil {
		t.Errorf("a name with no known container was refused on signature: %v", err)
	}
}

// Passing the signature check does not pass the checksum: a file with the
// right magic and the wrong SHA-256 is still refused by the checksum.
func TestDownloadWithPassingSignatureStillChecksSha256(t *testing.T) {
	body := goodBody(t, "localcode-1.2.3-windows-amd64.msi")
	srv := typedServer(t, body, "application/octet-stream")
	sum := sha256.Sum256([]byte("something else entirely"))
	a := Asset{
		Name:   "localcode-1.2.3-windows-amd64.msi",
		URL:    srv.URL,
		Size:   int64(len(body)),
		Digest: "sha256:" + hex.EncodeToString(sum[:]),
	}
	_, err := Download(context.Background(), srv.Client(), a, t.TempDir())
	if err == nil {
		t.Fatal("a file with valid magic and a wrong sha256 was accepted")
	}
	if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("the refusal is not the checksum: %v", err)
	}
}
