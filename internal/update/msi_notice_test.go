package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Only a not-installed failure for a version newer than the running one
// is drawn on page load: failed and another-installation. Cancelled
// draws nothing, and neither does anything installed.
func TestShouldNotifyMSIFailure(t *testing.T) {
	cases := []struct {
		code    int
		running string
		notify  bool
	}{
		{1603, "0.45.2", true},
		{1625, "0.45.2", true},
		{1, "0.45.2", true},
		{1618, "0.45.2", true},
		{1602, "0.45.2", false},
		{0, "0.45.2", false},
		{3010, "0.45.2", false},
		{1641, "0.45.2", false},
		{1603, "0.46.0", false},
		{1618, "0.46.0", false},
		{1603, "0.47.0", false},
		{1603, "dev", false},
		{1618, "dev", false},
	}
	for _, tc := range cases {
		rec := MSIRecord{Version: "0.46.0", ExitCode: tc.code, Log: "x.log", Time: time.Now()}
		if got := ShouldNotifyMSIFailure(rec, tc.running); got != tc.notify {
			status, _ := ClassifyMSIExit(tc.code)
			t.Errorf("exit %d (%s) running %s: ShouldNotifyMSIFailure = %v, want %v",
				tc.code, status, tc.running, got, tc.notify)
		}
	}
}

// The marker remembers the exact record bytes drawn: the same record is
// not owed again, and a rewritten one is.
func TestMSINoticePendingAndShown(t *testing.T) {
	dir := t.TempDir()
	if _, ok := MSINoticePending(dir, "0.45.2"); ok {
		t.Fatal("a missing record is owed a notice")
	}
	rec := MSIRecord{Version: "0.46.0", ExitCode: 1603, Log: filepath.Join(dir, "x.log"), Time: time.Now().Truncate(time.Second)}
	if err := WriteMSIRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	got, ok := MSINoticePending(dir, "0.45.2")
	if !ok {
		t.Fatal("a failed record for a newer version is owed no notice")
	}
	if got.ExitCode != 1603 || got.Version != "0.46.0" {
		t.Errorf("pending = %+v, want the 0.46.0 failure", got)
	}
	if err := MarkMSINoticeShown(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := MSINoticePending(dir, "0.45.2"); ok {
		t.Error("the record already drawn is owed again on reload")
	}
	// A retry that fails again is a rewritten record: new bytes, so it
	// is drawn again rather than staying silent under the old mark.
	rec.ExitCode = 1625
	rec.Time = rec.Time.Add(time.Second)
	if err := WriteMSIRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	if _, ok := MSINoticePending(dir, "0.45.2"); !ok {
		t.Error("a rewritten failure stays silent under the old mark")
	}
}

// A cancelled record is never owed, marked or not. Marking nothing
// changes that: there is no line to remember having drawn.
func TestMSINoticeNeverOwedForCancelled(t *testing.T) {
	dir := t.TempDir()
	if err := WriteMSIRecord(dir, MSIRecord{Version: "0.46.0", ExitCode: 1602, Log: "x.log", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := MSINoticePending(dir, "0.45.2"); ok {
		t.Error("a cancelled record is owed a notice")
	}
	if err := MarkMSINoticeShown(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := MSINoticePending(dir, "0.45.2"); ok {
		t.Error("a cancelled record is owed a notice after marking")
	}
}

// An unreadable record owes nothing: a corrupt file is not a failure to
// report, and marking it must not invent a marker for it.
func TestMSINoticeUnreadableRecord(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(MSIRecordPath(dir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := MSINoticePending(dir, "0.45.2"); ok {
		t.Error("an unreadable record is owed a notice")
	}
}
