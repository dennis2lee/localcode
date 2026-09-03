package update

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// zipWith writes a zip holding one file at the root, the way the Windows
// release archive is built (`zip localcode.exe`, nothing else).
func zipWith(t *testing.T, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "localcode-9.9.9-windows-amd64.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The Windows archive is a zip, and a handoff installs from it rather
// than from the MSI, so the archive reader has to open one.
func TestOpenBinaryReadsTheWindowsZip(t *testing.T) {
	body := []byte("MZ this stands in for an exe")
	path := zipWith(t, "localcode.exe", body)

	r, done, err := openBinary(path)
	if err != nil {
		t.Fatalf("openBinary: %v", err)
	}
	defer done()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("read %q, want the archived binary", got)
	}
}

func TestOpenBinaryRefusesAZipWithoutTheBinary(t *testing.T) {
	path := zipWith(t, "README.txt", []byte("not it"))
	if _, _, err := openBinary(path); err == nil || !strings.Contains(err.Error(), "no localcode binary") {
		t.Errorf("a zip without localcode.exe was opened: %v", err)
	}
}

// A handoff needs a binary this user can run with no installer in the
// way. On Windows that is the zip; the MSI is the packaged install and
// stays the settings window's business.
func TestHandoffAssetIsTheZipOnWindows(t *testing.T) {
	rel := Release{Version: "9.9.9", Assets: []Asset{
		{Name: "localcode-9.9.9-windows-amd64.msi"},
		{Name: "localcode-9.9.9-windows-amd64.zip"},
		{Name: "localcode-9.9.9-windows-arm64.zip"},
		{Name: "localcode-9.9.9-darwin-universal.tar.gz"},
		{Name: "localcode-9.9.9-linux-amd64.tar.gz"},
	}}
	for goarch, want := range map[string]string{"amd64": "-windows-amd64.zip", "arm64": "-windows-arm64.zip"} {
		a, err := rel.HandoffAssetFor("windows", goarch, true)
		if err != nil {
			t.Fatalf("windows/%s: %v", goarch, err)
		}
		if !strings.HasSuffix(a.Name, want) {
			t.Errorf("windows/%s handoff asset = %s, want *%s", goarch, a.Name, want)
		}
	}
	// And the MSI is what the button still gets.
	a, err := rel.AssetFor("windows", "amd64", true)
	if err != nil || !strings.HasSuffix(a.Name, ".msi") {
		t.Errorf("the install button's asset = %v (%v), want the msi", a.Name, err)
	}
	// Elsewhere the two agree.
	for _, goos := range []string{"darwin", "linux"} {
		h, herr := rel.HandoffAssetFor(goos, "amd64", false)
		p, perr := rel.AssetFor(goos, "amd64", false)
		if herr != nil || perr != nil || h.Name != p.Name {
			t.Errorf("%s: handoff asset %v (%v) differs from %v (%v)", goos, h.Name, herr, p.Name, perr)
		}
	}
}

// On every platform but Windows a handoff install is the ordinary one,
// and it says which binary to start.
func TestApplyForHandoffNamesTheBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows path stages or renames; covered by the Windows CI run of the handoff test")
	}
	// A tarball of a fake binary, installed over a fake target that is
	// not this process, so rename does what it does anywhere.
	dir := t.TempDir()
	target := filepath.Join(dir, "localcode")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := tarball(t, dir, "localcode-9.9.9-darwin-universal.tar.gz", map[string]string{"localcode": "#!/bin/sh\necho 9.9.9\n"})

	out, err := apply(archive, func() (string, error) { return target, nil })
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !out.Replaced {
		t.Fatalf("not replaced: %+v", out)
	}
	if out.Binary != target {
		t.Errorf("Binary = %q, want the target %q", out.Binary, target)
	}
	got, _ := os.ReadFile(target)
	if !strings.Contains(string(got), "9.9.9") {
		t.Errorf("the target was not replaced: %q", got)
	}
}
