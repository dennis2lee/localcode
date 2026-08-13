package dialog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// RevealDirectory opens dir in the operating system's file manager — a new
// Explorer or Finder window on the folder localcode is working in.
//
// It belongs beside the folder picker for the same reason: it only makes
// sense when the machine running the daemon is the machine with the screen
// on it. A daemon reached over the network would open a window in front of
// nobody, so the caller gates this on the same condition as PickDirectory
// (see Daemon.RevealDirectory).
//
// The per-OS half lives in reveal_windows.go / reveal_darwin.go /
// reveal_other.go, one file each, because the three do genuinely different
// things: Windows hands the path to the shell and must NOT be started
// hidden, macOS goes through Finder and has to be brought to the front, and
// the rest of the world has xdg-open or nothing at all. A single switch
// with three branches kept hiding that the flags of one were wrong for the
// others — which is exactly how the Windows window went missing.
func RevealDirectory(ctx context.Context, dir string) error {
	if dir == "" {
		return fmt.Errorf("no workspace directory to open")
	}
	// Absolute and in the OS's own notation before anything sees it. A
	// workspace typed (or read from config.json) as C:/Users/me/proj is a
	// perfectly good path to every Go file API and to this daemon, and
	// explorer.exe silently ignores it: it opens the default Documents
	// window instead, which looks exactly like the button working and
	// going to the wrong place.
	dir = filepath.Clean(filepath.FromSlash(dir))
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}

	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return openInFileManager(ctx, dir)
}

// openInFileManager is revealDir behind a variable so a test can watch what
// RevealDirectory hands the platform half without actually opening a window
// on the machine running the test suite.
var openInFileManager = revealDir
