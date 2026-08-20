package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scripts/install.sh is the install for a machine where nobody has root.
// It is shell, so nothing else in this repo would notice it breaking:
// these tests run the real script against a fake GitHub and a fake
// release, and check what lands in the fake home directory.

// fakeRelease serves a releases API and the tarball it advertises, and
// returns the server. digest is what the API claims the tarball hashes to,
// which the caller can make wrong on purpose.
func fakeRelease(t *testing.T, version string, body []byte, digest string) *httptest.Server {
	t.Helper()

	asset := "localcode-" + version + "-linux-amd64.tar.gz"
	if runtime.GOOS == "darwin" {
		asset = "localcode-" + version + "-darwin-universal.tar.gz"
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	type jsonAsset struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
		Size   int    `json:"size"`
		URL    string `json:"browser_download_url"`
	}
	// Decoys either side of the real one: the script has to pick by name,
	// not by position, and must not walk off with the .deb.
	assets := []jsonAsset{
		{Name: "localcode-" + version + "-linux-amd64.deb", Digest: "sha256:" + strings.Repeat("0", 64), URL: srv.URL + "/dl/deb"},
		{Name: asset, Digest: "sha256:" + digest, Size: len(body), URL: srv.URL + "/dl/" + asset},
		{Name: "localcode-" + version + "-windows-amd64.msi", Digest: "sha256:" + strings.Repeat("1", 64), URL: srv.URL + "/dl/msi"},
	}
	rel, err := json.Marshal(map[string]any{
		"tag_name": "v" + version,
		"html_url": srv.URL + "/releases/v" + version,
		"assets":   assets,
	})
	if err != nil {
		t.Fatal(err)
	}

	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(rel)
	})
	mux.HandleFunc("/repos/o/r/releases/tags/v"+version, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(rel)
	})
	mux.HandleFunc("/dl/"+asset, func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	mux.HandleFunc("/dl/deb", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("a debian package")) })
	mux.HandleFunc("/dl/msi", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("an installer")) })
	return srv
}

// runInstall runs the script with a home directory of its own and returns
// its combined output.
func runInstall(t *testing.T, home string, srv *httptest.Server, args ...string) (string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"LOCALCODE_API="+srv.URL,
		"LOCALCODE_REPO=o/r",
		"LOCALCODE_DIR="+filepath.Join(home, ".local", "bin"),
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func installArchive(t *testing.T, version string) ([]byte, string) {
	t.Helper()
	dir := t.TempDir()
	path := tarball(t, dir, "release.tar.gz", map[string]string{
		"localcode": "#!/bin/sh\necho " + version + "\n",
	})
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:])
}

func TestInstallScriptPutsLocalcodeUnderTheUsersOwnHome(t *testing.T) {
	needsUnix(t)
	home := t.TempDir()
	body, digest := installArchive(t, "0.49.0")
	srv := fakeRelease(t, "0.49.0", body, digest)

	out, err := runInstall(t, home, srv)
	if err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	exe := filepath.Join(home, ".local", "bin", "localcode")
	if got := says(t, exe); got != "0.49.0" {
		t.Errorf("installed localcode says %q", got)
	}
	info, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the installed binary is not executable: %v", info.Mode())
	}
	// Nothing was written outside the home directory it was given, which
	// is the entire promise of this install.
	if !strings.Contains(out, exe) {
		t.Errorf("the output does not say where it installed: %q", out)
	}
	if strings.Contains(out, "sudo") {
		t.Errorf("a root-free install asked for root: %q", out)
	}
}

// The release API publishes a SHA-256 per asset. A download that does not
// match it is not something to chmod +x and put on PATH.
func TestInstallScriptRefusesATamperedDownload(t *testing.T) {
	needsUnix(t)
	home := t.TempDir()
	body, _ := installArchive(t, "0.49.0")
	srv := fakeRelease(t, "0.49.0", body, strings.Repeat("a", 64))

	out, err := runInstall(t, home, srv)
	if err == nil {
		t.Fatalf("a download that failed its checksum was installed:\n%s", out)
	}
	if !strings.Contains(out, "checksum mismatch") {
		t.Errorf("the failure does not say what was wrong: %q", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "localcode")); !os.IsNotExist(err) {
		t.Error("a localcode was installed anyway")
	}
}

func TestInstallScriptUninstallsWhatItInstalled(t *testing.T) {
	needsUnix(t)
	home := t.TempDir()
	body, digest := installArchive(t, "0.49.0")
	srv := fakeRelease(t, "0.49.0", body, digest)

	if out, err := runInstall(t, home, srv); err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	out, err := runInstall(t, home, srv, "--uninstall")
	if err != nil {
		t.Fatalf("install.sh --uninstall: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "localcode")); !os.IsNotExist(err) {
		t.Error("the binary is still there after --uninstall")
	}
	// Config and sessions are not this script's to delete, and it says so.
	if !strings.Contains(out, ".localcode") {
		t.Errorf("the output does not say the config was left alone: %q", out)
	}
}

// Installing over a copy that is already there is the upgrade path, and it
// has to work while the old one is running.
func TestInstallScriptReplacesAnEarlierInstall(t *testing.T) {
	needsUnix(t)
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	installed(t, bin, "0.48.0")

	body, digest := installArchive(t, "0.49.0")
	srv := fakeRelease(t, "0.49.0", body, digest)
	if out, err := runInstall(t, home, srv, "--version", "0.49.0"); err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	if got := says(t, filepath.Join(bin, "localcode")); got != "0.49.0" {
		t.Errorf("the earlier install was not replaced: %q", got)
	}
	entries, err := os.ReadDir(bin)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the install left something else in %s: %v", bin, entries)
	}
}
