package dialog

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubReveal replaces the platform half for the duration of a test and
// returns a pointer to whatever path it was handed.
func stubReveal(t *testing.T) *string {
	t.Helper()
	var got string
	prev := openInFileManager
	openInFileManager = func(ctx context.Context, dir string) error {
		got = dir
		return nil
	}
	t.Cleanup(func() { openInFileManager = prev })
	return &got
}

// A workspace written with forward slashes is a perfectly ordinary thing —
// config.json is JSON, and every Go file API on Windows accepts it — but
// explorer.exe does not: handed C:/Users/me/proj it opens the default
// Documents window instead, which looks exactly like the button working and
// going to the wrong place.
func TestRevealNormalizesThePathForTheOS(t *testing.T) {
	got := stubReveal(t)
	dir := t.TempDir()

	if err := RevealDirectory(context.Background(), filepath.ToSlash(dir)); err != nil {
		t.Fatalf("RevealDirectory: %v", err)
	}
	if *got != filepath.Clean(dir) {
		t.Errorf("handed %q to the file manager, want %q", *got, filepath.Clean(dir))
	}
	if runtime.GOOS == "windows" && strings.Contains(*got, "/") {
		t.Errorf("path %q still has forward slashes; explorer.exe ignores those", *got)
	}
}

// A relative workspace is resolved here rather than left for the file
// manager to interpret against its own idea of a current directory.
func TestRevealMakesThePathAbsolute(t *testing.T) {
	got := stubReveal(t)
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	defer func() { _ = os.Chdir(wd) }()

	if err := os.Mkdir("sub", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RevealDirectory(context.Background(), "sub"); err != nil {
		t.Fatalf("RevealDirectory: %v", err)
	}
	if !filepath.IsAbs(*got) {
		t.Errorf("handed a relative path %q to the file manager", *got)
	}
	if filepath.Base(*got) != "sub" {
		t.Errorf("handed %q, want it to end in the directory asked for", *got)
	}
}

// Nothing is spawned for a path that cannot be a window: the error names
// the problem instead of a file manager opening on something else, or on
// nothing.
func TestRevealRefusesWhatIsNotADirectory(t *testing.T) {
	got := stubReveal(t)

	file := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RevealDirectory(context.Background(), file); err == nil {
		t.Error("a file was accepted as a folder to open")
	}
	if err := RevealDirectory(context.Background(), filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Error("a missing directory was accepted")
	}
	if err := RevealDirectory(context.Background(), ""); err == nil {
		t.Error("an empty path was accepted")
	}
	if *got != "" {
		t.Errorf("started a file manager on %q despite the path being unusable", *got)
	}
}
