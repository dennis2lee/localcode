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
	// Path is where the downloaded file is, which is the whole answer on a
	// platform where localcode cannot install for you.
	Path string `json:"path"`
	// Detail is what to tell the user, in one sentence.
	Detail string `json:"detail"`
}

// Apply installs a downloaded release.
//
// What it does not do is replace files itself. Unpacking an archive over a
// running install is how an update leaves a machine with half of two
// versions on it, and localcode has an installer on the platform where
// most of this matters — the MSI knows how to upgrade in place, remove
// what the old version left, and keep the Start menu entry pointing at
// something real. So: run the installer, or say plainly where the file is.
func Apply(path string) (Outcome, error) {
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

	// An archive. Extracting it over the running install is exactly what
	// this refuses to do, so the file and the instruction are the answer.
	return Outcome{
		Path:   path,
		Detail: "downloaded to " + path + " — close localcode and unpack it over the installed copy",
	}, nil
}
