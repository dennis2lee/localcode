//go:build darwin

package dialog

import (
	"context"
	"fmt"
	"os/exec"
)

// revealDir opens a Finder window on dir and brings Finder to the front.
//
// `open <dir>` alone is enough to get the window, but not reliably enough
// to get it *in front of the desktop window that asked for it*: the GUI
// build is the frontmost app at that moment, and a Finder window opened
// behind it is a button that appears to do nothing. osascript's `activate`
// is the same fix the folder picker needed, for the same reason.
//
// `open` remains the fallback: it is part of the base system and cannot be
// disabled, whereas automation of Finder can be refused by the privacy
// settings on a managed Mac. Getting the window behind the app beats not
// getting one at all.
func revealDir(ctx context.Context, dir string) error {
	script := `tell application "Finder"
	activate
	open POSIX file ` + appleScriptString(dir) + `
end tell`
	if err := exec.CommandContext(ctx, "osascript", "-e", script).Run(); err == nil {
		return nil
	}
	if err := exec.CommandContext(ctx, "open", dir).Run(); err != nil {
		return fmt.Errorf("open %s in Finder: %w%s", dir, err, stderrSuffix(err))
	}
	return nil
}
