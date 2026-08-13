//go:build windows

package dialog

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"localcode/internal/childproc"
)

// revealDir opens an Explorer window on dir.
//
// Deliberately NOT childproc.Hide: that helper exists for console children
// (bash, hooks, MCP servers) and sets STARTF_USESHOWWINDOW with SW_HIDE
// alongside CREATE_NO_WINDOW. Windows applies wShowWindow to the *first
// top-level window the child creates*, and explorer.exe is a GUI program
// whose whole job is to create one — so the folder window was being opened
// hidden. From the desktop build that is indistinguishable from a button
// that does nothing: no window, no error, nothing in the transcript.
//
// HideConsole keeps the half that is still wanted (no stray console box if
// explorer.exe is ever replaced by a console shim on some system) without
// the half that suppresses the window we are asking for.
func revealDir(ctx context.Context, dir string) error {
	// explorer.exe, not "explorer": resolved against PATH the same way,
	// but the explicit extension is what LookPath needs to report a
	// missing shell as an error instead of finding some other file.
	cmd := exec.CommandContext(ctx, "explorer.exe", dir)
	childproc.HideConsole(cmd)

	if err := cmd.Run(); err != nil {
		// explorer.exe exits 1 on success as often as not — it hands the
		// path to the already-running shell process and reports that it
		// did not open a window itself. Taking it at its word would put
		// an error on screen every time the window opened correctly.
		//
		// Only its *exit code* is forgiven, though. This used to return
		// nil for any error at all, which also swallowed the ones that
		// mean no window opened — explorer.exe not found on PATH, or the
		// process failing to start. Those produced the worst possible
		// result: a button that does nothing and says nothing, with no
		// way to tell a working feature from a broken one.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil
		}
		return fmt.Errorf("open %s in Explorer: %w%s", dir, err, stderrSuffix(err))
	}
	return nil
}
