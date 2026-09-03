package update

import (
	"context"
	"fmt"
	"localcode/internal/childproc"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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
	// Binary is the program to run as the new version, when Replaced is
	// set. Usually the same path this process was started from, now
	// holding the new file; on a Windows install under Program Files it
	// is a copy staged where this user can write, because the installed
	// one cannot be replaced without elevation and a handoff must not
	// wait on a dialog.
	Binary string `json:"binary,omitempty"`
	// Detail is what to tell the user, in one sentence.
	Detail string `json:"detail"`
}

// installerArgs is the command line the Windows installer is started with.
//
// Here rather than beside startInstaller, which is behind a windows build
// tag, because the flags are the whole of what makes an update apply and a
// fact nobody can check from another machine is one that goes wrong
// quietly. See install_windows.go for what each one is for.
func installerArgs(path string) []string {
	return []string{"/i", path, "/qb"}
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
			Detail: "the installer is running and will close localcode to replace its files; " +
				"start it again when it has finished",
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
		Binary:   exe,
		Detail:   "installed over " + exe,
	}, nil
}

// ApplyForHandoff installs an update the way a handoff needs it: the new
// program has to exist on disk, runnable by this user, without anything
// closing the running one.
//
// Everywhere but Windows that is Apply. On Windows the packaged install
// is an MSI under Program Files, and applying one runs msiexec, which
// asks for elevation and then closes localcode to replace its files —
// two things a handoff cannot have. So the archive is the zip, and the
// binary goes where this user can write: over the running one if that
// directory is writable (a portable install), and otherwise into a
// staging directory of localcode's own. The Program Files copy stays as
// it was; the settings window's install button, which runs the MSI, is
// how that copy is brought up to date.
func ApplyForHandoff(path string) (Outcome, error) {
	if runtime.GOOS != "windows" {
		return Apply(path)
	}
	exe, err := currentBinary()
	if err != nil {
		return Outcome{Path: path}, fmt.Errorf("find this program: %w", err)
	}
	if dirWritable(filepath.Dir(exe)) {
		if err := selfInstall(path, exe); err != nil {
			return Outcome{Path: path}, err
		}
		return Outcome{Replaced: true, Path: path, Binary: exe, Detail: "installed over " + exe}, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return Outcome{Path: path}, fmt.Errorf("no directory to stage the new localcode in: %w", err)
	}
	staged := filepath.Join(base, "localcode", "bin", filepath.Base(exe))
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		return Outcome{Path: path}, err
	}
	if err := selfInstall(path, staged); err != nil {
		return Outcome{Path: path}, err
	}
	return Outcome{
		Replaced: true,
		Path:     path,
		Binary:   staged,
		Detail: "staged at " + staged + " and running from there; the copy under " + filepath.Dir(exe) +
			" is still the old version, and the settings window's install button updates it",
	}, nil
}

// StagedBinary is where ApplyForHandoff puts a new binary when the one
// this process runs from cannot be replaced: the same file name, under
// the user's own cache directory. Empty when nothing has been staged.
//
// Startup asks for it before asking the network. A Program Files install
// on Windows is never itself replaced by an update — that copy waits for
// the MSI — so the binary that runs the daemon is the staged one, every
// start, and comparing the release against only the Program Files
// version would download the same release again each time.
func StagedBinary() string {
	exe, err := currentBinary()
	if err != nil {
		return ""
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	staged := filepath.Join(base, "localcode", "bin", filepath.Base(exe))
	if staged == exe {
		return ""
	}
	if _, err := os.Stat(staged); err != nil {
		return ""
	}
	return staged
}

// VersionOf asks a localcode binary what version it is. Running it is
// the only way to know: the number is stamped into the file at build
// time and nowhere else.
func VersionOf(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	childproc.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s does not run here: %w", path, err)
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", fmt.Errorf("%s printed no version", path)
	}
	return v, nil
}

// dirWritable reports whether this user can create a file in dir, which
// on Windows is the difference between a portable install and one under
// Program Files.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".localcode-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
