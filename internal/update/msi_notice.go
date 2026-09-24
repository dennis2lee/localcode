package update

import (
	"bytes"
	"os"
	"path/filepath"
)

// This file is the "shown once" half of the failed-install notice: which
// records the reopened window says without being asked, and how the
// daemon remembers that it already said it.
//
// The window draws the line, not a message box, because the box is modal:
// while it is up the helper cannot relaunch the window, and a box nobody
// can see holds the window back. The record stays where it is, so the
// settings panel keeps reporting it under the ReportMSIRecord rule when
// somebody clicks Check. What is remembered separately is whether this
// exact record was already drawn on a page load.

// msiNotifiedName is the marker beside the record, holding the exact
// record bytes that were already drawn once.
const msiNotifiedName = "last-install-notified.json"

// MSINotifiedPath is the marker file beside the record.
func MSINotifiedPath(dir string) string {
	return filepath.Join(dir, msiNotifiedName)
}

// ShouldNotifyMSIFailure says whether the reopened window draws one line
// about the record on page load. Only the not-installed failures: status
// failed or another-installation, while the record's version is newer
// than the running one. A cancelled record draws nothing, because the
// person cancelled it themselves. An installed record draws nothing
// either: the window coming back is the whole of that report.
func ShouldNotifyMSIFailure(rec MSIRecord, running string) bool {
	if !ReportMSIRecord(rec, running) {
		return false
	}
	status, _ := ClassifyMSIExit(rec.ExitCode)
	return status == "failed" || status == "another-installation"
}

// MSINoticePending returns the record the window should draw once, and
// whether one is owed. A record already drawn (the marker holds these
// exact bytes) is not owed again: a reload is not a new failure. A
// rewritten record is new bytes, so a retry that fails again is drawn
// again.
func MSINoticePending(dir, running string) (MSIRecord, bool) {
	var none MSIRecord
	raw, err := os.ReadFile(MSIRecordPath(dir))
	if err != nil {
		return none, false
	}
	var rec MSIRecord
	if err := readMSIRecordBytes(raw, &rec); err != nil {
		return none, false
	}
	if !ShouldNotifyMSIFailure(rec, running) {
		return none, false
	}
	if shown, err := os.ReadFile(MSINotifiedPath(dir)); err == nil && bytes.Equal(shown, raw) {
		return none, false
	}
	return rec, true
}

// MarkMSINoticeShown remembers that the current record was drawn, so the
// next page load stays silent. It copies the record's exact bytes: only
// those bytes count as shown.
func MarkMSINoticeShown(dir string) error {
	raw, err := os.ReadFile(MSIRecordPath(dir))
	if err != nil {
		return err
	}
	return os.WriteFile(MSINotifiedPath(dir), raw, 0o644)
}
