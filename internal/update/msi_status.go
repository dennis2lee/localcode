package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This file is the platform-independent half of the Windows MSI install:
// exit-code classification, the install record, helper file names, and
// the reply text. It has no build tag so the macOS gate tests it. The
// Windows-only half (staging the helper, waiting, running msiexec) is in
// msi_helper_windows.go.

// MSI exit codes the helper records and the panel explains.
const (
	msiOK              = 0
	msiRebootRequired  = 3010
	msiRebootInitiated = 1641
	msiCancelled       = 1602
	msiBusy            = 1618
)

// ClassifyMSIExit says what an msiexec exit code means. The first word is
// the machine-readable status. Installed is true when the version the
// helper ran is now on disk, even when a reboot is still needed to finish
// it.
func ClassifyMSIExit(code int) (status string, installed bool) {
	switch code {
	case msiOK:
		return "installed", true
	case msiRebootRequired:
		return "installed-restart-needed", true
	case msiRebootInitiated:
		return "installed-restart-started", true
	case msiCancelled:
		return "cancelled", false
	case msiBusy:
		return "another-installation", false
	default:
		return "failed", false
	}
}

// MSIExitMeaning is the human phrase for an msiexec exit code, for the
// panel line that names the code. One phrase per code, so the panel says
// what happened without making the reader look the number up.
func MSIExitMeaning(code int) string {
	switch code {
	case msiOK:
		return "installed"
	case msiRebootRequired:
		return "installed, restart needed"
	case msiRebootInitiated:
		return "installed, restart started"
	case msiCancelled:
		return "cancelled"
	case msiBusy:
		return "another installation in progress"
	default:
		return fmt.Sprintf("failed (exit code %d)", code)
	}
}

// MSIRecord is what the helper writes after msiexec exits. The update
// check reads it to report an install that did not land.
type MSIRecord struct {
	Version  string    `json:"version"`
	ExitCode int       `json:"exit_code"`
	Log      string    `json:"log"`
	Time     time.Time `json:"time"`
}

// MSIPending is what the parent writes when it stages the helper. The
// helper reads it to learn what to run and where its parent ran from.
// The environment carries only the path to this file, so no argument
// list has to fit into an environment variable.
type MSIPending struct {
	Version    string    `json:"version"`
	MSI        string    `json:"msi"`
	Log        string    `json:"log"`
	ParentExe  string    `json:"parent_exe"`
	ParentArgs []string  `json:"parent_args"`
	GUI        bool      `json:"gui"`
	Time       time.Time `json:"time"`
}

// Names under the updates directory.
const (
	msiHelperBase     = "localcode-msi-helper"
	msiPendingName    = "install-pending.json"
	msiRecordName     = "last-install.json"
	msiLogSuffix      = "-msi.log"
	msiProductPrefix  = "localcode-"
	msiProductSuffix  = "-windows-amd64.msi"
	guiExecutableName = "localcode-gui.exe"
)

// EnvMSIHelper marks a process as the install helper and points at its
// pending file. An environment variable rather than a flag, the way the
// handoff successor is told what it is (LOCALCODE_TAKEOVER): a hidden
// mode must not appear in the flag roster or the usage text.
const EnvMSIHelper = "LOCALCODE_MSI_HELPER"

// EnvMSIParent carries the waiting handle on Windows: the value of an
// inherited handle to the parent process. A handle, not a PID, so PID
// reuse cannot fool the wait.
const EnvMSIParent = "LOCALCODE_MSI_PARENT"

// MSIHelperName is the helper copy's file name on an OS.
func MSIHelperName(goos string) string {
	if goos == "windows" {
		return msiHelperBase + ".exe"
	}
	return msiHelperBase
}

// HelperCopyPath is where the helper copy lives: the updates directory,
// never the install directory, so it holds no file the installer has to
// replace.
func HelperCopyPath(dir, goos string) string {
	return filepath.Join(dir, MSIHelperName(goos))
}

// MSIPendingPath is the pending file beside the helper copy.
func MSIPendingPath(dir string) string {
	return filepath.Join(dir, msiPendingName)
}

// MSIRecordPath is the record file beside the helper copy.
func MSIRecordPath(dir string) string {
	return filepath.Join(dir, msiRecordName)
}

// MSILogPath is the installer log: beside the MSI, named after the
// version.
func MSILogPath(msiPath, version string) string {
	return filepath.Join(filepath.Dir(msiPath), msiProductPrefix+version+msiLogSuffix)
}

// MSIVersionFromName parses the version out of the release MSI file name,
// localcode-<version>-windows-amd64.msi. Empty when the name is not one.
func MSIVersionFromName(path string) string {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, msiProductPrefix) || !strings.HasSuffix(base, msiProductSuffix) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(base, msiProductPrefix), msiProductSuffix)
}

// GUIExecutableBeside returns the window binary beside an executable
// path: the same directory the parent ran from.
func GUIExecutableBeside(exe string) string {
	return filepath.Join(filepath.Dir(exe), guiExecutableName)
}

// ErrMSIPending is a second install request while a helper is still
// waiting or installing. It carries the pending version for the reply.
type ErrMSIPending struct {
	Version string
}

func (e *ErrMSIPending) Error() string {
	if e.Version != "" {
		return fmt.Sprintf("an install of localcode %s is already pending; it starts when localcode exits", e.Version)
	}
	return "an install is already pending; it starts when localcode exits"
}

// WriteMSIPending stages the helper's instructions.
func WriteMSIPending(dir string, p MSIPending) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := MSIPendingPath(dir)
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ReadMSIPending reads the helper's instructions.
func ReadMSIPending(path string) (MSIPending, error) {
	var p MSIPending
	raw, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("the pending install at %s is unreadable: %w", path, err)
	}
	return p, nil
}

// WriteMSIRecord records what msiexec did.
func WriteMSIRecord(dir string, r MSIRecord) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(MSIRecordPath(dir), raw, 0o644)
}

// ErrNoMSIRecord is a missing record: no install has been recorded.
var ErrNoMSIRecord = errors.New("no recorded install")

// ReadMSIRecord reads the last recorded install.
func ReadMSIRecord(dir string) (MSIRecord, error) {
	raw, err := os.ReadFile(MSIRecordPath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return MSIRecord{}, ErrNoMSIRecord
		}
		return MSIRecord{}, err
	}
	var r MSIRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return MSIRecord{}, fmt.Errorf("the recorded install is unreadable: %w", err)
	}
	return r, nil
}

// ClearMSIRecord drops the record: a new install supersedes it, or the
// running version is what it installed and there is nothing to report.
func ClearMSIRecord(dir string) error {
	err := os.Remove(MSIRecordPath(dir))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ClearMSIPending drops the pending file: the helper has run, or a stale
// one from a process that never started it is in the way.
func ClearMSIPending(dir string) error {
	err := os.Remove(MSIPendingPath(dir))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReportMSIRecord says whether GET /api/update should include a record.
// A record is reported only while its version is newer than the running
// version, by the same comparison the check offers updates with. Every
// status follows the one rule: a cancelled 0.149.0 installed by hand
// afterwards must not keep saying it did not install while 0.149.0 is
// running. Otherwise the record says nothing and is cleared. A dev
// build reports nothing, since no release is newer than it.
func ReportMSIRecord(r MSIRecord, running string) bool {
	return Newer(running, r.Version)
}

// MSIDetailWindow is the install reply in the desktop window: the
// installer starts when the window closes, and LocalCode opens again
// when the installer has finished. Two sentences, one period each.
func MSIDetailWindow(version string) string {
	return "The installer for localcode " + version + " starts when this window closes. " +
		"LocalCode opens again when the installer has finished."
}

// MSIDetailTerminal is the install reply in a terminal or a headless
// daemon: the installer starts when localcode exits, and the person has
// to quit it. Two sentences, one period each.
func MSIDetailTerminal(version string) string {
	return "The installer for localcode " + version + " starts when localcode exits. " +
		"Quit localcode to run it."
}

// MSIDetail picks the reply for where the parent runs.
func MSIDetail(version string, gui bool) string {
	if gui {
		return MSIDetailWindow(version)
	}
	return MSIDetailTerminal(version)
}
