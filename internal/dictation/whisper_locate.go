package dictation

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// whisperBinName is whisper.cpp's server executable, as its own build
// names it.
func whisperBinName() string {
	if runtime.GOOS == "windows" {
		return "whisper-server.exe"
	}
	return "whisper-server"
}

// findWhisperBin locates the engine executable, preferring an explicit
// path and otherwise looking where an install would have put it.
//
// There is deliberately no PATH lookup: a
// whisper-server someone installed for unrelated reasons is not
// necessarily the build this was tested against, and silently using it
// turns a version mismatch into a mystery.
func findWhisperBin(explicit string) string {
	if explicit != "" {
		if fileExists(explicit) {
			return explicit
		}
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	cands := []string{
		// Beside the binary, for a locally built engine dropped in by
		// hand.
		filepath.Join(dir, whisperBinName()),
		// Inside the macOS bundle the executable lives in
		// Contents/MacOS, and engines ship beside it in Contents/
		// Resources.
		filepath.Join(dir, "..", "Resources", whisperBinName()),
	}
	// Where `dictation install` puts it: under models/, with everything
	// else that was downloaded rather than shipped.
	for _, parent := range []func() (string, error){BundledModelParent, HomeModelParent} {
		if p, err := parent(); err == nil {
			cands = append(cands, filepath.Join(p, whisperBinName()))
		}
	}
	for _, cand := range cands {
		if fileExists(cand) {
			return cand
		}
	}
	return ""
}

// findWhisperModel locates a ggml model file.
//
// When several are installed the largest wins. Whisper's model files are
// ordered by capability almost exactly as they are by size, and someone
// who has downloaded a bigger one has said which they would rather use.
func findWhisperModel(explicit string) string {
	if explicit != "" {
		if fileExists(explicit) {
			return explicit
		}
		// A directory is accepted too, since "the model" is a file but
		// people reasonably point at the folder holding it.
		if info, err := os.Stat(explicit); err == nil && info.IsDir() {
			return largestGGML(explicit)
		}
		return ""
	}
	for _, parent := range []func() (string, error){BundledModelParent, HomeModelParent} {
		dir, err := parent()
		if err != nil {
			continue
		}
		if m := largestGGML(dir); m != "" {
			return m
		}
	}
	return ""
}

func largestGGML(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	type cand struct {
		path string
		size int64
	}
	var found []cand
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "ggml-") || !strings.HasSuffix(name, ".bin") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		found = append(found, cand{filepath.Join(dir, name), info.Size()})
	}
	if len(found) == 0 {
		return ""
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].size != found[j].size {
			return found[i].size > found[j].size
		}
		return found[i].path < found[j].path
	})
	return found[0].path
}

// remoteHost normalizes WhisperURL to the "host:port" the transcribe
// call dials, or "" when no remote engine is configured.
//
// Deliberately forgiving about the form: people copy an address out of a
// browser bar or a colleague's message, so "http://box:8080",
// "box:8080", and a trailing slash all mean the same thing. A missing
// port gets whisper.cpp's own default rather than an error, since 8080
// is what its server prints when started without --port.
func (c Config) remoteHost() string {
	raw := strings.TrimSpace(c.WhisperURL)
	if raw == "" {
		return ""
	}
	raw = strings.TrimSuffix(raw, "/")
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		raw = u.Host
	}
	if _, _, err := net.SplitHostPort(raw); err != nil {
		raw = net.JoinHostPort(raw, "8080")
	}
	return raw
}

// whisperPaths resolves the engine and model this config would run,
// naming whichever is missing.
func (c Config) whisperPaths() (bin, model string, err error) {
	bin = findWhisperBin(c.WhisperBin)
	if bin == "" {
		if c.WhisperBin != "" {
			return "", "", fmt.Errorf("no whisper engine at %s", c.WhisperBin)
		}
		return "", "", fmt.Errorf("no speech engine installed (found no %s) — run `localcode dictation install`", whisperBinName())
	}
	model = findWhisperModel(c.WhisperModel)
	if model == "" {
		if c.WhisperModel != "" {
			return "", "", fmt.Errorf("no whisper model at %s", c.WhisperModel)
		}
		return "", "", fmt.Errorf("no speech model installed (no ggml-*.bin found) — run `localcode dictation install`")
	}
	return bin, model, nil
}

func (c Config) whisperReady() error {
	// A remote engine needs nothing installed here, so there is nothing
	// to check beyond having been told where it is. Whether it actually
	// answers is checked when a dictation starts, where the address can
	// be put in the error.
	if c.remoteHost() != "" {
		return nil
	}
	_, _, err := c.whisperPaths()
	return err
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func defaultThreads() int {
	// Half the cores, at least one, at most four. This runs beside a
	// language model doing the actual work: taking every core to
	// transcribe speech would slow down the thing being asked for.
	n := runtime.NumCPU() / 2
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	return n
}
