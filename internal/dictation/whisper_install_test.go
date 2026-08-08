package dictation

import (
	"archive/zip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The archive holds every example whisper.cpp builds — a talk-llama, a
// chess demo, an SDL2, a pile of test binaries. Unpacking the lot would
// put 20MB of programs nobody asked for beside the binary, and missing
// one real dependency would produce a Windows loader failure before any
// of our code runs.
func TestWhisperEngineFilesKeepsTheEngineAndNothingElse(t *testing.T) {
	keep := []string{
		"Release/whisper-server.exe",
		"Release/whisper.dll",
		"Release/ggml.dll",
		"Release/ggml-base.dll",
		// The whole CPU set: ggml picks one at run time by what the
		// processor supports, so choosing here would be choosing which
		// CPUs to support.
		"Release/ggml-cpu-haswell.dll",
		"Release/ggml-cpu-sse42.dll",
		"Release/ggml-cpu-x64.dll",
	}
	drop := []string{
		"Release/whisper-talk-llama.exe",
		"Release/whisper-cli.exe",
		"Release/wchess.exe",
		"Release/SDL2.dll",
		"Release/parakeet.dll",
		"Release/test-vad.exe",
		"Release/bench.exe",
		"Release/main.exe",
	}
	for _, n := range keep {
		if !whisperEngineFiles(n) {
			t.Errorf("%s dropped, but the engine needs it", n)
		}
	}
	for _, n := range drop {
		if whisperEngineFiles(n) {
			t.Errorf("%s kept, but nothing uses it", n)
		}
	}
}

// Entries are flattened to their base name, which is what puts the
// engine beside the binary instead of in a Release/ subdirectory — and
// also why no archive entry can name a path outside the destination.
func TestExtractZipFlattensAndFilters(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.zip")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, name := range []string{
		"Release/whisper-server.exe",
		"Release/ggml.dll",
		"Release/wchess.exe",
		"../../escape.dll",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(name))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dest := t.TempDir()
	if err := extractZip(src, dest, func(n string) bool {
		return whisperEngineFiles(n) || strings.HasSuffix(n, "escape.dll")
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)

	// escape.dll lands as a plain file in dest, not two directories up:
	// only the base name is ever used.
	want := []string{"escape.dll", "ggml.dll", "whisper-server.exe"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("extracted %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(dest)), "escape.dll")); err == nil {
		t.Error("an entry escaped the destination directory")
	}
}

// Both halves are required: an engine with no model cannot transcribe,
// and a model with no engine has nothing to run it.
func TestWhisperInstalledNeedsBothHalves(t *testing.T) {
	dir := t.TempDir()
	if WhisperInstalled(dir) {
		t.Error("reported installed with an empty directory")
	}

	os.WriteFile(filepath.Join(dir, whisperBinName()), []byte("x"), 0o755)
	if WhisperInstalled(dir) {
		t.Error("reported installed with an engine but no model")
	}

	os.WriteFile(filepath.Join(dir, "ggml-small-q5_1.bin"), []byte("x"), 0o644)
	if !WhisperInstalled(dir) {
		t.Error("reported not installed with both an engine and a model")
	}
}

// Remove names the files it deletes rather than sweeping the directory:
// it runs elevated from the Windows uninstaller against a path built
// from an installer property, which is where a sweep removes someone's
// Program Files.
func TestRemoveWhisperLeavesEverythingElseAlone(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{
		whisperBinName(), "ggml.dll", "ggml-cpu-haswell.dll", "ggml-small-q5_1.bin",
		"localcode.exe", "config.json", "notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveWhisper(dir); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	sort.Strings(left)
	want := []string{"config.json", "localcode.exe", "notes.txt"}
	if strings.Join(left, ",") != strings.Join(want, ",") {
		t.Errorf("left %v, want %v", left, want)
	}
}

// A missing directory is not a failure to uninstall from.
func TestRemoveWhisperOnNothing(t *testing.T) {
	if err := RemoveWhisper(filepath.Join(t.TempDir(), "gone")); err != nil {
		t.Errorf("removing from a missing directory: %v", err)
	}
}

// Checked against the real upstream archive when it is present, because
// the member names are upstream's to change and the filter above is a
// guess about them until something compares the two. Downloaded by
// scripts/check-whisper-asset.sh, or skipped.
func TestFilterAgainstTheRealArchive(t *testing.T) {
	path := os.Getenv("LC_TEST_WHISPER_ZIP")
	if path == "" {
		t.Skip("set LC_TEST_WHISPER_ZIP to the upstream whisper-bin-x64.zip to run")
	}
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var kept []string
	for _, f := range r.File {
		if !f.FileInfo().IsDir() && whisperEngineFiles(f.Name) {
			kept = append(kept, filepath.Base(f.Name))
		}
	}
	sort.Strings(kept)
	t.Logf("keeping %d of %d members: %v", len(kept), len(r.File), kept)

	for _, required := range []string{"whisper-server.exe", "whisper.dll", "ggml.dll", "ggml-base.dll"} {
		found := false
		for _, k := range kept {
			if k == required {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not in the archive under that name any more", required)
		}
	}
	var cpus int
	for _, k := range kept {
		if strings.HasPrefix(k, "ggml-cpu-") {
			cpus++
		}
	}
	if cpus == 0 {
		t.Error("no ggml-cpu-*.dll kept; ggml would have no backend to load")
	}
}
