package dictation

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdata/model-fixture.tar.bz2 has the same shape as the real 400MB
// archive — one top-level directory named after the model, holding both
// int8 and float32 weights, tokens.txt, and a test_wavs subdirectory —
// with the weights replaced by a line of text each. The real archive was
// installed once by hand to confirm this shape is accurate; a test that
// downloaded it every run would not be a test anybody would keep.
const fixture = "testdata/model-fixture.tar.bz2"

func serveFixture(t *testing.T) (url, sum string) {
	t.Helper()
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	h := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, hex.EncodeToString(h[:])
}

func TestInstallUnpacksAModelReadyToUse(t *testing.T) {
	url, sum := serveFixture(t)
	parent := t.TempDir()

	var lastDone, lastTotal int64
	dir, err := install(context.Background(), url, sum, parent, func(done, total int64) {
		lastDone, lastTotal = done, total
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	// The point of the whole thing: what lands on disk is something the
	// recognizer can open, not just some unpacked files.
	if _, err := resolveModel(dir); err != nil {
		t.Fatalf("the installed directory is not a usable model: %v", err)
	}
	if got := Installed(parent); got != dir {
		t.Errorf("Installed(parent) = %q, want %q", got, dir)
	}
	if lastDone == 0 || lastDone != lastTotal {
		t.Errorf("progress ended at %d/%d, want a complete transfer", lastDone, lastTotal)
	}

	// The float32 weights are dropped and the int8 ones kept: on the real
	// archive that is the difference between 133MB and 400MB on disk.
	for _, gone := range []string{"encoder-epoch-99-avg-1.onnx", "decoder-epoch-99-avg-1.onnx", "joiner-epoch-99-avg-1.onnx"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s should have been dropped", gone)
		}
	}
	for _, kept := range []string{"encoder-epoch-99-avg-1.int8.onnx", "tokens.txt"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s should have been kept: %v", kept, err)
		}
	}

	// Nothing is left behind. The staging directory and the downloaded
	// archive both live in parent, and on the real thing they are 400MB.
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ModelName {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("parent holds %v, want only %s", names, ModelName)
	}
}

// The Windows installer runs this on every upgrade. Re-downloading 400MB
// each time would be its own bug, so a second call has to be free.
func TestInstallIsANoOpWhenTheModelIsAlreadyThere(t *testing.T) {
	url, sum := serveFixture(t)
	parent := t.TempDir()

	if _, err := install(context.Background(), url, sum, parent, nil); err != nil {
		t.Fatalf("first install: %v", err)
	}

	var served bool
	watched := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
	}))
	defer watched.Close()

	if _, err := install(context.Background(), watched.URL, sum, parent, nil); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if served {
		t.Error("the second install downloaded again instead of noticing the model was there")
	}
	_ = url
}

// A mismatch means the bytes are not the ones that were reviewed. The
// Windows installer unpacks this elevated, so the archive must not be
// touched at all in that case.
func TestInstallRefusesAnArchiveWithTheWrongChecksum(t *testing.T) {
	url, _ := serveFixture(t)
	parent := t.TempDir()

	_, err := install(context.Background(), url, strings.Repeat("0", 64), parent, nil)
	if err == nil {
		t.Fatal("expected a checksum failure")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("error does not mention the checksum: %v", err)
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Errorf("a rejected download left %d entries behind", len(entries))
	}
}

func TestInstallReportsAServerThatSaysNo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := install(context.Background(), srv.URL, "", t.TempDir(), nil); err == nil {
		t.Fatal("expected an error for a 404")
	}
}

// A tar entry names its own path, and nothing in the format stops that
// path pointing outside the directory being extracted into. The Windows
// installer runs this extraction as SYSTEM.
func TestArchiveEntriesCannotEscapeTheDestination(t *testing.T) {
	dest := t.TempDir()
	for _, name := range []string{
		"../escaped.txt",
		"a/../../escaped.txt",
		"/etc/passwd",
		"..",
	} {
		if _, err := safeJoin(dest, name); err == nil {
			t.Errorf("safeJoin allowed %q out of the destination", name)
		}
	}
	for _, name := range []string{
		"model/tokens.txt",
		"model/test_wavs/0.wav",
		"./model/tokens.txt",
	} {
		got, err := safeJoin(dest, name)
		if err != nil {
			t.Errorf("safeJoin rejected the ordinary entry %q: %v", name, err)
			continue
		}
		if !strings.HasPrefix(got, dest) {
			t.Errorf("safeJoin(%q) = %q, outside %q", name, got, dest)
		}
	}
}

// Belt and braces on the above: prove the extractor itself refuses,
// not just the helper it happens to call today.
func TestExtractRefusesATraversingArchive(t *testing.T) {
	// Written uncompressed and read back through the same path a real
	// archive takes, minus bzip2 — the stdlib can read bzip2 but not
	// write it, and the traversal check is above the compression layer.
	dest := t.TempDir()
	raw := filepath.Join(t.TempDir(), "evil.tar")
	f, err := os.Create(raw)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	body := []byte("owned\n")
	if err := tw.WriteHeader(&tar.Header{Name: "../escaped.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()
	f.Close()

	in, err := os.Open(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err := extractTar(tar.NewReader(in), dest); err == nil {
		t.Fatal("extract accepted an entry pointing outside the destination")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "escaped.txt")); err == nil {
		t.Fatal("the traversing entry was written")
	}
}
