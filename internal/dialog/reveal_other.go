//go:build !windows && !darwin

package dialog

import (
	"context"
	"fmt"
	"os/exec"
)

// revealDir opens dir in whatever file manager the desktop registered with
// xdg-open. There is no fallback worth writing: without xdg-open there is
// no portable way to ask, and guessing at nautilus/dolphin/thunar by name
// would open the wrong one as often as the right one. Reported as
// ErrUnsupported so the caller can say so plainly.
func revealDir(ctx context.Context, dir string) error {
	if _, err := exec.LookPath("xdg-open"); err != nil {
		return ErrUnsupported
	}
	if err := exec.CommandContext(ctx, "xdg-open", dir).Run(); err != nil {
		return fmt.Errorf("open %s in a file manager: %w%s", dir, err, stderrSuffix(err))
	}
	return nil
}
