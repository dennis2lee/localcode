package update

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A release archive carries only the console binary. Overwriting a
// desktop-window executable with it leaves the user running the console
// TUI instead of a window, so flavorMismatch refuses this replacement.
func TestFlavorMismatchRefusesConsolePayloadUnderWindowName(t *testing.T) {
	tests := []struct {
		exe          string
		entry        string
		wantMismatch bool
	}{
		// Console payload into desktop-window build target must be refused.
		{"localcode-gui.exe", "localcode.exe", true},
		{"localcode-gui", "localcode", true},
		{"LOCALCODE-GUI.EXE", "localcode.exe", true},
		{"LocalCode-GUI", "LOCALCODE", true},
		{`C:\Program Files\LocalCode\localcode-gui.exe`, "localcode.exe", true},
		{"/Applications/LocalCode.app/Contents/MacOS/localcode-gui", "localcode", true},

		// Same flavor or console over console must be permitted.
		{"localcode.exe", "localcode.exe", false},
		{"localcode", "localcode", false},
		{"localcode.exe", "localcode", false},
		{"localcode", "localcode.exe", false},
		{`C:\Users\alice\bin\localcode.exe`, "localcode.exe", false},
		{"/home/alice/.local/bin/localcode", "localcode", false},

		// Portable installs under custom names are permitted.
		{"custom-runner.exe", "localcode.exe", false},
		{"custom-runner", "localcode", false},
	}

	for _, tc := range tests {
		reason := flavorMismatch(tc.exe, tc.entry)
		if tc.wantMismatch {
			if reason == "" {
				t.Errorf("flavorMismatch(%q, %q) = %q, want non-empty reason", tc.exe, tc.entry, reason)
			}
			// The refusal is shown to a person, so it must name both sides.
			if !strings.Contains(reason, cleanBase(tc.exe)) || !strings.Contains(reason, cleanBase(tc.entry)) {
				t.Errorf("flavorMismatch(%q, %q) reason %q does not name both sides", tc.exe, tc.entry, reason)
			}
		} else {
			if reason != "" {
				t.Errorf("flavorMismatch(%q, %q) = %q, want empty string", tc.exe, tc.entry, reason)
			}
		}
	}
}

// selfInstall must refuse to unpack a console payload over a desktop-window
// executable and leave the existing file completely untouched.
func TestSelfInstallRefusesZipConsolePayloadOverWindowBinary(t *testing.T) {
	needsUnix(t)

	dir := t.TempDir()
	windowExe := filepath.Join(dir, "localcode-gui.exe")
	originalBytes := []byte("#!/bin/sh\necho 0.100.0\n")
	if err := os.WriteFile(windowExe, originalBytes, 0o755); err != nil {
		t.Fatal(err)
	}

	zipPayload := []byte("#!/bin/sh\necho 0.101.0\n")
	archive := zipWith(t, "localcode.exe", zipPayload)

	err := selfInstall(archive, windowExe)
	if err == nil {
		t.Fatal("selfInstall succeeded unexpectedly, want refusal for flavor mismatch")
	}
	if !errors.Is(err, errWrongFlavor) {
		t.Fatalf("selfInstall error = %v, want error matching errWrongFlavor", err)
	}

	afterBytes, err := os.ReadFile(windowExe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterBytes, originalBytes) {
		t.Errorf("target file was modified: got %q, want %q", afterBytes, originalBytes)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "localcode-gui.exe" {
			t.Errorf("refusal left temporary or extra file behind: %s", e.Name())
		}
	}
}

// When a desktop-window binary sits in a writable directory, handoff
// update must not overwrite it with the console build. It must stage
// the console binary beside it and keep the window file intact.
func TestApplyForHandoffStagesWhenTargetIsWindowBinaryEvenIfWritable(t *testing.T) {
	needsUnix(t)

	dir := t.TempDir()
	windowExe := filepath.Join(dir, "localcode-gui.exe")
	originalBytes := []byte("#!/bin/sh\necho 0.100.0\n")
	if err := os.WriteFile(windowExe, originalBytes, 0o755); err != nil {
		t.Fatal(err)
	}

	zipPayload := []byte("#!/bin/sh\necho 0.101.0\n")
	archive := zipWith(t, "localcode.exe", zipPayload)

	cacheDir := t.TempDir()
	target := func() (string, error) { return windowExe, nil }
	writable := func(string) bool { return true }
	cache := func() (string, error) { return cacheDir, nil }

	out, err := applyForHandoff(archive, "windows", target, writable, cache)
	if err != nil {
		t.Fatalf("applyForHandoff: %v", err)
	}

	afterBytes, err := os.ReadFile(windowExe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterBytes, originalBytes) {
		t.Errorf("window file was modified: got %q, want %q", afterBytes, originalBytes)
	}

	// True, even though the window file was not written over. Replaced is
	// what tells the caller a new binary is on disk and it may hand off to
	// Binary; internal/daemon/selfupdate.go turns a false into ErrNoUpdate
	// and starts the old version instead, so a staging outcome that says
	// false is an MSI install that silently stops updating itself.
	if !out.Replaced {
		t.Errorf("out.Replaced = false, so the startup handoff would refuse to run %q", out.Binary)
	}

	expectedStaged := filepath.Join(cacheDir, "localcode", "bin", "localcode.exe")
	if out.Binary != expectedStaged {
		t.Errorf("out.Binary = %q, want %q", out.Binary, expectedStaged)
	}
	if !strings.Contains(out.Detail, "staged at") {
		t.Errorf("out.Detail = %q, want staging description", out.Detail)
	}

	if filepath.Base(out.Binary) == "localcode-gui.exe" {
		t.Errorf("staged binary is named localcode-gui.exe, must be named after payload")
	}

	stagedBytes, err := os.ReadFile(out.Binary)
	if err != nil {
		t.Fatalf("staged binary not found on disk: %v", err)
	}
	if !bytes.Equal(stagedBytes, zipPayload) {
		t.Errorf("staged binary content = %q, want %q", stagedBytes, zipPayload)
	}
	if got := says(t, out.Binary); got != "0.101.0" {
		t.Errorf("staged binary execution output = %q, want 0.101.0", got)
	}

	stagedGuiPath := filepath.Join(filepath.Dir(out.Binary), "localcode-gui.exe")
	if _, err := os.Stat(stagedGuiPath); !os.IsNotExist(err) {
		t.Errorf("found %s, staged copy must not use window name", stagedGuiPath)
	}
}

// A staged binary is named after the payload it contains. Any copy
// staged under the old window name localcode-gui.exe must be ignored.
func TestStagedBinaryNotNamedWindowBinary(t *testing.T) {
	if got := stagedName("windows"); got != "localcode.exe" {
		t.Errorf("stagedName(windows) = %q, want localcode.exe", got)
	}
	if got := stagedName("linux"); got != "localcode" {
		t.Errorf("stagedName(linux) = %q, want localcode", got)
	}
	if got := stagedName("darwin"); got != "localcode" {
		t.Errorf("stagedName(darwin) = %q, want localcode", got)
	}

	cacheDir := t.TempDir()
	binDir := filepath.Join(cacheDir, "localcode", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Legacy staged copy with window name must not be picked up.
	legacyStaged := filepath.Join(binDir, "localcode-gui.exe")
	if err := os.WriteFile(legacyStaged, []byte("legacy"), 0o755); err != nil {
		t.Fatal(err)
	}

	target := func() (string, error) { return `C:\Program Files\LocalCode\localcode-gui.exe`, nil }
	cache := func() (string, error) { return cacheDir, nil }

	if got := stagedBinary("windows", target, cache); got != "" {
		t.Errorf("stagedBinary returned legacy window binary %q, want empty string", got)
	}

	// When staged console binary exists, it is reported.
	validStaged := filepath.Join(binDir, "localcode.exe")
	if err := os.WriteFile(validStaged, []byte("valid"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := stagedBinary("windows", target, cache); got != validStaged {
		t.Errorf("stagedBinary = %q, want %q", got, validStaged)
	}

	// If the current process is the staged binary, it is not reported as staged.
	targetSame := func() (string, error) { return validStaged, nil }
	if got := stagedBinary("windows", targetSame, cache); got != "" {
		t.Errorf("stagedBinary returned current running binary %q, want empty string", got)
	}
}

// Regression guard: an ordinary portable install where console replaces
// console must continue to replace the executable in place.
func TestApplyForHandoffOrdinaryPortableInstallReplacesBinary(t *testing.T) {
	needsUnix(t)

	// Windows portable install: localcode.exe over localcode.exe.
	dir := t.TempDir()
	targetExe := filepath.Join(dir, "localcode.exe")
	if err := os.WriteFile(targetExe, []byte("#!/bin/sh\necho 0.100.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	zipPayload := []byte("#!/bin/sh\necho 0.101.0\n")
	zipArchive := zipWith(t, "localcode.exe", zipPayload)

	cacheDir := t.TempDir()
	targetW := func() (string, error) { return targetExe, nil }
	writable := func(string) bool { return true }
	cache := func() (string, error) { return cacheDir, nil }

	outW, err := applyForHandoff(zipArchive, "windows", targetW, writable, cache)
	if err != nil {
		t.Fatalf("applyForHandoff windows portable: %v", err)
	}
	if !outW.Replaced {
		t.Errorf("outW.Replaced = false, want true for portable install")
	}
	if outW.Binary != targetExe {
		t.Errorf("outW.Binary = %q, want %q", outW.Binary, targetExe)
	}
	if got := says(t, targetExe); got != "0.101.0" {
		t.Errorf("targetExe version = %q, want 0.101.0", got)
	}

	// Linux portable install: localcode over localcode.
	targetLinux := filepath.Join(dir, "localcode")
	if err := os.WriteFile(targetLinux, []byte("#!/bin/sh\necho 0.100.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	tarArchive := tarball(t, t.TempDir(), "localcode-0.101.0-linux-amd64.tar.gz", map[string]string{
		"localcode": "#!/bin/sh\necho 0.101.0\n",
	})
	targetL := func() (string, error) { return targetLinux, nil }

	outL, err := applyForHandoff(tarArchive, "linux", targetL, writable, cache)
	if err != nil {
		t.Fatalf("applyForHandoff linux portable: %v", err)
	}
	if !outL.Replaced {
		t.Errorf("outL.Replaced = false, want true for portable install")
	}
	if outL.Binary != targetLinux {
		t.Errorf("outL.Binary = %q, want %q", outL.Binary, targetLinux)
	}
	if got := says(t, targetLinux); got != "0.101.0" {
		t.Errorf("targetLinux version = %q, want 0.101.0", got)
	}
}
