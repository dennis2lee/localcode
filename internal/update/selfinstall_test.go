package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// tarball writes a gzipped tar containing the given name/contents pairs
// and returns its path. Everything is mode 0755, since a release tarball
// carries an executable.
func tarball(t *testing.T, dir, name string, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for path, body := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name: path, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// installed puts a stand-in for an installed localcode at dir/localcode: a
// script that prints the version it is pretending to be, so a test can see
// which of the two binaries survived.
func installed(t *testing.T, dir, version string) string {
	t.Helper()
	path := filepath.Join(dir, "localcode")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho "+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func says(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command(path, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("running %s: %v: %s", path, err, out)
	}
	return strings.TrimSpace(string(out))
}

func needsUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the shell stand-ins here are not executables on Windows, which installs from the MSI anyway")
	}
}

// The point of the whole thing: localcode unpacked into a directory this
// user owns updates itself, with no root and no package manager.
func TestApplyReplacesAnInstallThisUserOwns(t *testing.T) {
	needsUnix(t)
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := installed(t, bin, "0.48.0")
	archive := tarball(t, t.TempDir(), "localcode-0.49.0-linux-amd64.tar.gz", map[string]string{
		"localcode": "#!/bin/sh\necho 0.49.0\n",
	})

	out, err := apply(archive, func() (string, error) { return exe, nil })
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := says(t, exe); got != "0.49.0" {
		t.Errorf("the installed localcode is still %q", got)
	}
	if !strings.Contains(out.Detail, "installed over "+exe) {
		t.Errorf("the answer does not say it installed: %q", out.Detail)
	}
	// Reported as a fact rather than as a sentence about restarting: what
	// to do about the running copy is the caller's to decide, and the two
	// callers decide differently — the daemon restarts itself where it
	// can, and says so where it cannot.
	if !out.Replaced {
		t.Error("replacing the installed binary was not reported as such, so nothing downstream can restart")
	}
	if out.Started {
		t.Error("an installer was reported as running when nothing was started")
	}
}

// Renaming into place rather than writing through the old file is what
// lets this happen while localcode is running: the process keeps the file
// it started from.
func TestReplacingDoesNotDisturbTheRunningCopy(t *testing.T) {
	needsUnix(t)
	dir := t.TempDir()
	exe := installed(t, dir, "0.48.0")
	before, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()

	archive := tarball(t, t.TempDir(), "localcode-0.49.0-linux-amd64.tar.gz", map[string]string{
		"localcode": "#!/bin/sh\necho 0.49.0\n",
	})
	if err := selfInstall(archive, exe); err != nil {
		t.Fatalf("selfInstall: %v", err)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(before); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "0.48.0") {
		t.Errorf("the open file changed under the running process: %q", buf.String())
	}
}

// An install only root can write is the .deb's job, and the answer says so
// instead of failing silently or asking for a password.
func TestApplyLeavesAnInstallItCannotWriteAlone(t *testing.T) {
	needsUnix(t)
	if os.Geteuid() == 0 {
		t.Skip("running as root, where no directory is unwritable")
	}
	dir := t.TempDir()
	exe := installed(t, dir, "0.48.0")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	archive := tarball(t, t.TempDir(), "localcode-0.49.0-linux-amd64.tar.gz", map[string]string{
		"localcode": "#!/bin/sh\necho 0.49.0\n",
	})
	out, err := apply(archive, func() (string, error) { return exe, nil })
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := says(t, exe); got != "0.48.0" {
		t.Errorf("an unwritable install was replaced anyway: %q", got)
	}
	if !strings.Contains(out.Detail, "not writable") {
		t.Errorf("the answer does not say why it did not install: %q", out.Detail)
	}
	if !strings.Contains(out.Detail, archive) {
		t.Errorf("the answer does not say where the download is: %q", out.Detail)
	}
}

// A .app is signed as a whole directory. Swapping the binary inside one
// leaves a bundle whose resources belong to the previous version.
func TestApplyWillNotReachIntoAnAppBundle(t *testing.T) {
	needsUnix(t)
	dir := t.TempDir()
	inner := filepath.Join(dir, "LocalCode.app", "Contents", "MacOS")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := installed(t, inner, "0.48.0")
	archive := tarball(t, t.TempDir(), "localcode-0.49.0-darwin-universal.tar.gz", map[string]string{
		"localcode": "#!/bin/sh\necho 0.49.0\n",
	})

	out, err := apply(archive, func() (string, error) { return exe, nil })
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := says(t, exe); got != "0.48.0" {
		t.Errorf("the binary inside the bundle was replaced: %q", got)
	}
	if !strings.Contains(out.Detail, "bundle") {
		t.Errorf("the answer does not explain the bundle: %q", out.Detail)
	}
}

// The archive for the .app has no bare binary in it, so it cannot be
// mistaken for one even if it is downloaded by something else.
func TestTheAppBundleArchiveIsNotMistakenForABinary(t *testing.T) {
	dir := t.TempDir()
	archive := tarball(t, dir, "localcode-0.49.0-darwin-universal-app.tar.gz", map[string]string{
		"LocalCode.app/Contents/MacOS/localcode": "binary",
	})
	if _, _, err := openBinary(archive); err == nil {
		t.Error("a .app archive was accepted as a bare binary")
	}
}

// The version check is what stops a download that unpacks perfectly and
// then does not execute — the wrong architecture, say — from taking the
// place of a localcode that works.
func TestAnUnrunnableDownloadDoesNotTakeTheInstallsPlace(t *testing.T) {
	needsUnix(t)
	dir := t.TempDir()
	exe := installed(t, dir, "0.48.0")
	archive := tarball(t, t.TempDir(), "localcode-0.49.0-linux-amd64.tar.gz", map[string]string{
		"localcode": "\x7fELF not really\n",
	})

	out, err := apply(archive, func() (string, error) { return exe, nil })
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := says(t, exe); got != "0.48.0" {
		t.Errorf("a binary that does not run replaced the one that did: %q", got)
	}
	if !strings.Contains(out.Detail, "does not run here") {
		t.Errorf("the answer does not say the download would not run: %q", out.Detail)
	}
	// Nothing left behind in the install directory either.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "localcode" {
			t.Errorf("a failed install left %s behind", e.Name())
		}
	}
}

// A file that is not a release archive is not installed, and the answer
// says what was wrong with it rather than leaving the person to guess why
// their localcode is still the old one.
func TestAFileThatIsNotAnArchiveIsNotInstalled(t *testing.T) {
	needsUnix(t)
	dir := t.TempDir()
	exe := installed(t, dir, "0.48.0")
	archive := filepath.Join(t.TempDir(), "localcode-0.49.0-linux-amd64.tar.gz")
	if err := os.WriteFile(archive, []byte("not an archive"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := apply(archive, func() (string, error) { return exe, nil })
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := says(t, exe); got != "0.48.0" {
		t.Errorf("the install was replaced from a file that is not an archive: %q", got)
	}
	if !strings.Contains(out.Detail, "not a gzip archive") {
		t.Errorf("the answer does not say what was wrong with the file: %q", out.Detail)
	}
}

// A second update in one run has to find a name for the binary it moves
// aside.
//
// Windows permits renaming a running image and forbids deleting one. The
// first update renamed this process's own image to <exe>.old, and this
// process is still alive holding it — so the Remove before the second
// rename fails silently and the rename onto it fails with access denied.
// Every second update inside one session answered "could not move the
// running binary aside".
func TestMovingAsideTwiceFindsAFreeName(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "localcode.exe")

	for i := 0; i < 3; i++ {
		if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := moveAside(exe); err != nil {
			t.Fatalf("move %d: %v", i+1, err)
		}
		if _, err := os.Stat(exe); !os.IsNotExist(err) {
			t.Fatalf("move %d left the binary in place", i+1)
		}
	}
}

// And the copies go once nothing holds them.
func TestTheAsideCopiesAreSweptUp(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("moveAside and SweepAside are the Windows answer to a running image")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "localcode.exe")
	for _, name := range []string{exe + ".old", exe + ".old.1", exe + ".old.2"} {
		if err := os.WriteFile(name, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	SweepAside(exe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the sweep left %d file(s) behind", len(entries))
	}
}
