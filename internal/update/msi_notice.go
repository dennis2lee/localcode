package update

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// This file is the "shown once" half of the failed-install notice: which
// records the next stream to open says without being asked, and how the
// daemon remembers that it already said it.
//
// The stream draws the line, not a message box, because the box is modal:
// while it is up the helper cannot relaunch the window, and a box nobody
// can see holds the window back. The record stays where it is, so the
// settings panel keeps reporting it under the ReportMSIRecord rule when
// somebody clicks Check. What is remembered separately is whether this
// exact record was already drawn on a stream.
//
// The line is carried after the backlog, so it lands at the end of what
// the client has drawn: a page-load fetch usually resolved before the
// replay arrived and drew it first, above the fold in any conversation
// longer than a screen. It is a transient event (no seq, no `id:` line),
// so it never moves the resume point and is never written to any
// session's log. Both clients already draw a recovered error as a note
// that ends nothing, which is why the next window or terminal to open
// says it, not just the window.

// msiNotifiedName is the marker beside the record, holding the exact
// record bytes that were already drawn once.
const msiNotifiedName = "last-install-notified.json"

// MSINotifiedPath is the marker file beside the record.
func MSINotifiedPath(dir string) string {
	return filepath.Join(dir, msiNotifiedName)
}

// ShouldNotifyMSIFailure says whether the next stream to open draws one
// line about the record. Only the not-installed failures: status failed
// or another-installation, while the record's version is newer than the
// running one. A cancelled record draws nothing, because the person
// cancelled it themselves. An installed record draws nothing either: the
// window coming back is the whole of that report.
func ShouldNotifyMSIFailure(rec MSIRecord, running string) bool {
	if !ReportMSIRecord(rec, running) {
		return false
	}
	status, _ := ClassifyMSIExit(rec.ExitCode)
	return status == "failed" || status == "another-installation"
}

// MSIFailureLine is the one line the stream carries for a failed
// install: the version, the exit code and what it means, and the log
// path. It is the panel's wording, not a second one: lastInstallLine in
// settings.js says this same sentence for the same record on a check,
// and the TestMSIFailureLineMatchesThePanel test here and the panel test
// in test/webui/update.test.js pin the same literal, so the two cannot
// drift apart without a test failing.
func MSIFailureLine(rec MSIRecord) string {
	return fmt.Sprintf("Update to %s did not install: the installer exited %d (%s). Log: %s.",
		rec.Version, rec.ExitCode, MSIExitMeaning(rec.ExitCode), rec.Log)
}

// MSINoticePending returns the record the next stream should draw once,
// and whether one is owed. A record already drawn (the marker holds
// these exact bytes) is not owed again: a reconnect is not a new
// failure. A rewritten record is new bytes, so a retry that fails again
// is drawn again.
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
// next stream stays silent. It copies the record's exact bytes: only
// those bytes count as shown. Call it only after the write to the stream
// succeeded: a stream that fails on the write leaves the notice owed for
// the next one.
func MarkMSINoticeShown(dir string) error {
	raw, err := os.ReadFile(MSIRecordPath(dir))
	if err != nil {
		return err
	}
	return os.WriteFile(MSINotifiedPath(dir), raw, 0o644)
}
