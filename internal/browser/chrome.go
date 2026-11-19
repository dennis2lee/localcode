// Package browser runs a Chrome (or Edge) for the model to work in, the
// way a person would: it looks at the page, clicks, types and scrolls. The
// browser is started by localcode with no window of its own; the GUI shows
// it in the right panel from a stream of frames, and the person can click
// and type in that view too.
//
// One browser serves the whole daemon, so every conversation shares its
// logins, and each conversation has its own tab, kept for as long as the
// conversation is. The browser runs on a profile of its own under
// ~/.localcode/browser, seeded from the person's everyday Chrome profile:
// Chrome refuses remote control of its default profile directory (since
// Chrome 136), and that directory is locked while the person's Chrome is
// open anyway, so a copy is the only way to start already signed in.
package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Kind is which browser a path is, for finding its profile directory.
type Kind string

const (
	Chrome Kind = "chrome"
	Edge   Kind = "edge"
)

// Install is a browser found on this machine.
type Install struct {
	Path string
	Kind Kind
}

// ErrNotFound is returned when neither Chrome nor Edge is installed.
var ErrNotFound = errors.New("no Chrome or Edge found; install Google Chrome, or set browser_path in config.json")

// Find returns the browser to run: override when set, otherwise Chrome,
// then Edge, in the places each installs itself.
func Find(override string) (Install, error) {
	if override != "" {
		// Absolute only: a relative path would be resolved against
		// wherever the daemon happened to start, which is not a place
		// anybody chose.
		if !filepath.IsAbs(override) {
			return Install{}, fmt.Errorf("browser_path %q is not an absolute path", override)
		}
		if _, err := os.Stat(override); err != nil {
			return Install{}, err
		}
		return Install{Path: override, Kind: kindOf(override)}, nil
	}
	for _, c := range candidates(runtime.GOOS, os.Getenv) {
		if c.Path == "" {
			continue
		}
		if filepath.IsAbs(c.Path) {
			if fi, err := os.Stat(c.Path); err == nil && !fi.IsDir() {
				return c, nil
			}
			continue
		}
		if p, err := exec.LookPath(c.Path); err == nil {
			return Install{Path: p, Kind: c.Kind}, nil
		}
	}
	return Install{}, ErrNotFound
}

// candidates lists where Chrome and Edge install on goos, in the order
// they are tried. A bare name is looked up on PATH.
func candidates(goos string, getenv func(string) string) []Install {
	switch goos {
	case "darwin":
		home := getenv("HOME")
		return []Install{
			{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", Chrome},
			{filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"), Chrome},
			{"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge", Edge},
		}
	case "windows":
		var out []Install
		for _, root := range []string{getenv("ProgramFiles"), getenv("ProgramFiles(x86)"), getenv("LOCALAPPDATA")} {
			if root != "" {
				out = append(out, Install{filepath.Join(root, `Google\Chrome\Application\chrome.exe`), Chrome})
			}
		}
		for _, root := range []string{getenv("ProgramFiles(x86)"), getenv("ProgramFiles")} {
			if root != "" {
				out = append(out, Install{filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`), Edge})
			}
		}
		return out
	default:
		return []Install{
			{"google-chrome", Chrome}, {"google-chrome-stable", Chrome},
			{"chromium", Chrome}, {"chromium-browser", Chrome},
			{"microsoft-edge", Edge}, {"microsoft-edge-stable", Edge},
		}
	}
}

func kindOf(path string) Kind {
	base := filepath.Base(path)
	for _, edge := range []string{"msedge.exe", "Microsoft Edge", "microsoft-edge", "microsoft-edge-stable"} {
		if base == edge {
			return Edge
		}
	}
	return Chrome
}

// UserDataDir is where kind keeps the person's own profiles on goos.
func UserDataDir(kind Kind, goos string, getenv func(string) string) string {
	home := getenv("HOME")
	switch goos {
	case "darwin":
		if kind == Edge {
			return filepath.Join(home, "Library/Application Support/Microsoft Edge")
		}
		return filepath.Join(home, "Library/Application Support/Google/Chrome")
	case "windows":
		local := getenv("LOCALAPPDATA")
		if kind == Edge {
			return filepath.Join(local, `Microsoft\Edge\User Data`)
		}
		return filepath.Join(local, `Google\Chrome\User Data`)
	default:
		config := getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		if kind == Edge {
			return filepath.Join(config, "microsoft-edge")
		}
		return filepath.Join(config, "google-chrome")
	}
}
