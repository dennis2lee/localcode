package update

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Outcome says what installing did, because it is not the same thing on
// every platform and the difference is the user's business.
type Outcome struct {
	// Started reports that an installer is now running and will replace
	// this install. Nothing else here is going to happen on its own: the
	// caller has to let localcode exit, since a running program's files
	// cannot be replaced while it holds them.
	Started bool `json:"started"`
	// Replaced reports that this program's own binary has been written
	// over, so the version now on disk is not the one running. Nothing
	// about that is visible from inside the process — the old image stays
	// mapped until it exits — which is why it is said here: whoever asked
	// for the update is the one who can act on it.
	Replaced bool `json:"replaced"`
	// Path is where the downloaded file is, which is the whole answer on a
	// platform where localcode cannot install for you.
	Path string `json:"path"`
	// Detail is what to tell the user, in one sentence.
	Detail string `json:"detail"`
}

// Apply installs a downloaded release.
//
// What it will not do is unpack an archive over an install it does not
// own. On Windows that is the MSI's job, and it does it properly: upgrade
// in place, remove what the old version left, keep the Start menu entry
// pointing at something real. A .deb needs root, which is a password and
// a decision belonging to the person at the keyboard.
//
// What is left is the install with no package manager behind it at all —
// a binary unpacked into somewhere like ~/.local/bin. That one this user
// already owns, so localcode replaces it itself, and a root-free install
// updates with a click like every other one.
func Apply(path string) (Outcome, error) { return apply(path, currentBinary) }

// apply is Apply with the running binary's location injected, so a test
// can exercise the replacement without the test binary being the thing
// that gets replaced.
func apply(path string, target func() (string, error)) (Outcome, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Outcome{}, fmt.Errorf("the downloaded update is not there: %w", err)
	}
	if info.IsDir() {
		return Outcome{}, fmt.Errorf("%s is a directory, not an installer", path)
	}

	if strings.EqualFold(filepath.Ext(path), ".msi") && runtime.GOOS == "windows" {
		if err := startInstaller(path); err != nil {
			return Outcome{Path: path}, err
		}
		return Outcome{
			Started: true,
			Path:    path,
			Detail:  "the installer is running; localcode has to close for it to replace the files",
		}, nil
	}

	// A Debian package. Installing it needs root, and localcode is not
	// going to ask for root or run a package manager on someone's behalf
	// — that is a decision with a password attached to it. The command is
	// one line and it is theirs to run.
	if strings.EqualFold(filepath.Ext(path), ".deb") {
		return Outcome{
			Path:   path,
			Detail: "downloaded to " + path + " — install it with: sudo apt install " + path,
		}, nil
	}

	// An archive, and this is the install nobody else manages: if the
	// running binary sits somewhere this user can write, the new one is
	// unpacked beside it and renamed over it.
	//
	// Everything that makes that the wrong move says so instead — a
	// /usr/bin copy only root can write, a .app bundle that is replaced
	// whole, a Windows zip — and says why, because "unpack it yourself"
	// with no reason reads like localcode never tried.
	exe, err := target()
	if err != nil {
		return Outcome{
			Path:   path,
			Detail: "downloaded to " + path + " — close localcode and unpack it over the installed copy",
		}, nil
	}
	if err := selfInstall(path, exe); err != nil {
		return Outcome{
			Path:   path,
			Detail: "downloaded to " + path + " — localcode did not replace itself (" + err.Error() + "); unpack it over the installed copy",
		}, nil
	}
	return Outcome{
		Replaced: true,
		Path:     path,
		Detail:   "installed over " + exe,
	}, nil
}
