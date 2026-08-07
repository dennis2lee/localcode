package gui

import (
	"os"
	"path/filepath"
	"testing"
)

// logo.svg is a copy of the installer's icon, because go:embed cannot
// reach outside its own package directory and the splash screen has to
// carry the artwork inside the binary — there is no file to read at
// startup, and fetching one would defeat the point of showing something
// immediately.
//
// A copy that can drift is a copy that will: the two would diverge the
// first time the icon is redrawn, and the only symptom would be a splash
// screen showing the old logo, which nobody would think to check. So the
// build fails instead.
//
// This test has no build tag on purpose. The embed only compiles into
// the desktop build, but the drift is just as real in every other build,
// and a guard that only runs on one platform is half a guard.
func TestLogoMatchesTheInstallerIcon(t *testing.T) {
	const source = "../../build/icon/icon.svg"

	want, err := os.ReadFile(filepath.FromSlash(source))
	if err != nil {
		t.Fatalf("read %s: %v", source, err)
	}
	got, err := os.ReadFile("logo.svg")
	if err != nil {
		t.Fatalf("read logo.svg: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("internal/gui/logo.svg has drifted from %s.\n"+
			"They are the same artwork and must stay identical:\n"+
			"\tcp %s internal/gui/logo.svg", source, source)
	}
}
