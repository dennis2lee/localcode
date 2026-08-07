package dictation

import (
	"fmt"
	"os"
	"path/filepath"
)

// Where a model installed by `localcode dictation install` ends up, and
// where the daemon looks when dictation_model_dir is not set.
//
// This exists so installing a model is the whole setup. Requiring a
// config edit afterwards would put the doubled-backslash JSON path
// problem in front of every Windows user for no reason, and would leave
// the installer either editing a file it does not own or printing
// instructions nobody reads.
const modelsDirName = "models"

// BundledModelParent is the models directory beside the running binary.
//
// Beside the binary rather than in the home directory because that is
// where the Windows installer can write: it runs elevated and installs
// per-machine, so one download serves every account on the box. A
// per-user location would mean each account downloading its own 400MB
// copy, which for a shared or managed machine is the wrong answer.
func BundledModelParent() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate this binary: %w", err)
	}
	// Symlinks resolved so a binary reached through one (a Homebrew-style
	// bin shim, say) looks beside the real install rather than beside the
	// link.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), modelsDirName), nil
}

// HomeModelParent is ~/.localcode/models, the natural place for someone
// installing a model for themselves on a machine where they cannot write
// beside the binary.
func HomeModelParent() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".localcode", modelsDirName), nil
}

// DefaultModelDir finds an installed model without being told where one
// is, returning "" when there is none.
//
// Beside the binary first: on Windows that is what the installer filled
// in, and it is the copy that is kept up to date with the program. A
// home-directory model is the fallback for anyone who installed one for
// themselves.
func DefaultModelDir() string {
	for _, parent := range []func() (string, error){BundledModelParent, HomeModelParent} {
		dir, err := parent()
		if err != nil {
			continue
		}
		if found := Installed(dir); found != "" {
			return found
		}
	}
	return ""
}
