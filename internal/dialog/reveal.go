package dialog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"localcode/internal/childproc"
)

// RevealDirectory opens dir in the operating system's file manager — a new
// Explorer or Finder window on the folder localcode is working in.
//
// It belongs beside the folder picker for the same reason: it only makes
// sense when the machine running the daemon is the machine with the screen
// on it. A daemon reached over the network would open a window in front of
// nobody, so the caller gates this on the same condition as PickDirectory
// (see Daemon.RevealDirectory).
func RevealDirectory(ctx context.Context, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", dir)
	case "windows":
		cmd = exec.CommandContext(ctx, "explorer", dir)
	default:
		if _, err := exec.LookPath("xdg-open"); err != nil {
			return ErrUnsupported
		}
		cmd = exec.CommandContext(ctx, "xdg-open", dir)
	}
	childproc.Hide(cmd)

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
		if runtime.GOOS == "windows" && errors.As(err, &exitErr) {
			return nil
		}
		return fmt.Errorf("open %s: %w%s", dir, err, stderrSuffix(err))
	}
	return nil
}
