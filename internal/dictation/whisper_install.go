package dictation

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// What `localcode dictation install` fetches for the whisper engine.
//
// Two downloads, from two places, because they are two different kinds
// of thing: the engine is a platform binary and the model is not.
const (
	// WhisperModelName is the model file's name once installed.
	WhisperModelName = "ggml-small-q5_1.bin"

	// WhisperModelURL is Hugging Face's copy of whisper.cpp's own model
	// repository — the same file whisper.cpp's download script fetches.
	WhisperModelURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/" + WhisperModelName

	// WhisperModelSHA256 pins the model file. Computed from the download
	// that produced the Korean transcripts in whisper_live_test, so this
	// is the file those results are a claim about.
	WhisperModelSHA256 = "ae85e4a935d7a567bd102fe55afc16bb595bdb618e11b2fc7591bc08120411bb"

	// WhisperModelSize renders progress. ~190MB.
	WhisperModelSize = 190085487

	// whisperRelease is the upstream tag the engine comes from. Pinned
	// rather than "latest": the engine is a binary this runs, and it
	// should change when someone changes it here, not when upstream
	// publishes.
	whisperRelease = "v1.9.2"
)

// small-q5_1 rather than the alternatives, for a reason worth writing
// down since it is the kind of choice that gets revisited blindly.
//
// It is quantised, so it is 190MB instead of 466MB and holds a
// correspondingly smaller amount of memory while resident. On the
// Korean reference set it transcribed all four sentences with correct
// spacing, which is the bar this had to clear; medium is more accurate
// on unclear speech at roughly twice the compute, and for a real-time
// feature latency is felt far more than the last two points of
// accuracy. large-v3-turbo is the interesting third option — similar
// size to medium but optimised for inference — and is worth
// benchmarking if small is ever observed struggling.

// whisperEngineAsset is the upstream archive holding whisper-server for
// this platform, and the checksum it must have.
//
// Only Windows has an upstream binary release that contains the server.
// macOS and Linux publish no such archive, so on those platforms the
// engine has to be built and placed beside the binary; install says so
// rather than pretending.
func whisperEngineAsset() (url, sum string, err error) {
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		return "https://github.com/ggml-org/whisper.cpp/releases/download/" + whisperRelease + "/whisper-bin-x64.zip",
			"49dcc16de826f20bd53d44f947a1ae49dfa81f86cad67a64d80820cb192d674a",
			nil
	}
	return "", "", fmt.Errorf(
		"no prebuilt speech engine is published for %s/%s — build whisper.cpp and put %s beside this binary",
		runtime.GOOS, runtime.GOARCH, whisperBinName())
}

// whisperEngineFiles are the archive members worth keeping.
//
// The archive holds every example whisper.cpp builds — a talk-llama, a
// chess demo, an SDL2 — and unpacking the lot would put 20MB of programs
// nobody asked for into the install directory. The ggml-cpu-* set stays
// whole on purpose: ggml picks one at run time by what the processor
// supports, and choosing here would mean deciding which CPUs to support.
func whisperEngineFiles(name string) bool {
	base := strings.ToLower(filepath.Base(filepath.FromSlash(name)))
	switch base {
	case "whisper-server.exe", "whisper.dll", "ggml.dll", "ggml-base.dll":
		return true
	// The running platform's own name for the engine, which on anything
	// but Windows has no .exe. Only the Windows archive is downloaded, so
	// this never matters for extraction — it matters for removal, where
	// leaving it out meant uninstalling on macOS deleted the model and
	// left the engine sitting beside the binary.
	case strings.ToLower(whisperBinName()):
		return true
	}
	// Shared libraries the engine loads, under whichever suffix the
	// platform uses. Only .dll ships today; the others are here so a
	// locally built engine uninstalls as cleanly as a downloaded one.
	if !strings.HasPrefix(base, "ggml-cpu-") {
		return false
	}
	return strings.HasSuffix(base, ".dll") || strings.HasSuffix(base, ".dylib") || strings.Contains(base, ".so")
}

// WhisperInstalled reports whether an engine and a model are both in
// place under parent, since either alone cannot dictate.
func WhisperInstalled(parent string) bool {
	return fileExists(filepath.Join(parent, whisperBinName())) && largestGGML(parent) != ""
}

// InstallWhisper puts the engine and its model in parent.
//
// The engine goes beside the binary rather than into a subdirectory
// because that is where findWhisperBin looks, and because on Windows the
// DLLs beside it are how the process resolves its imports at all.
func InstallWhisper(ctx context.Context, parent string, progress func(stage string, done, total int64)) error {
	if progress == nil {
		progress = func(string, int64, int64) {}
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", parent, err)
	}

	if !fileExists(filepath.Join(parent, whisperBinName())) {
		url, sum, err := whisperEngineAsset()
		if err != nil {
			return err
		}
		archive, err := download(ctx, url, sum, parent, func(d, t int64) { progress("engine", d, t) })
		if err != nil {
			return err
		}
		defer os.Remove(archive)
		if err := extractZip(archive, parent, whisperEngineFiles); err != nil {
			return err
		}
		if !fileExists(filepath.Join(parent, whisperBinName())) {
			return fmt.Errorf("the downloaded engine archive contained no %s", whisperBinName())
		}
	}

	if largestGGML(parent) == "" {
		model, err := download(ctx, WhisperModelURL, WhisperModelSHA256, parent, func(d, t int64) { progress("model", d, t) })
		if err != nil {
			return err
		}
		dest := filepath.Join(parent, WhisperModelName)
		if err := os.Rename(model, dest); err != nil {
			os.Remove(model)
			return fmt.Errorf("move model into place: %w", err)
		}
		// Renamed out of a temp file created with 0600: it has to be
		// readable by whatever account runs the program, which on a
		// machine-wide Windows install is not the account that installed
		// it.
		if err := os.Chmod(dest, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// RemoveWhisper deletes the engine and model from parent.
//
// Named files only, never a directory sweep: this runs elevated from the
// Windows uninstaller against a path built from an installer property,
// which is exactly where "delete everything under here" removes someone's
// Program Files.
func RemoveWhisper(parent string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if !whisperEngineFiles(name) && !(strings.HasPrefix(name, "ggml-") && strings.HasSuffix(name, ".bin")) {
			continue
		}
		if err := os.Remove(filepath.Join(parent, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	return nil
}

// extractZip unpacks the entries keep accepts, flattened into dest.
//
// Flattened because the archive nests everything under "Release/" and
// the engine has to end up beside the binary, not in a subdirectory of
// it. That also removes the traversal question a zip normally raises:
// only the base name is ever used, so no entry can name a path at all.
func extractZip(archive, dest string, keep func(string) bool) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("read engine archive: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if f.FileInfo().IsDir() || !keep(f.Name) {
			continue
		}
		if err := extractZipFile(f, filepath.Join(dest, filepath.Base(filepath.FromSlash(f.Name)))); err != nil {
			return err
		}
	}
	return nil
}

func extractZipFile(f *zip.File, target string) error {
	in, err := f.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	// 0755: whisper-server is a program, and on a machine-wide install
	// every account has to be able to run it.
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	// No size limit: the archive is checksum-pinned, so its declared
	// sizes are the ones that were reviewed.
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
