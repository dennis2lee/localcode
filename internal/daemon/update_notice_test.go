package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localcode/internal/update"
)

// The notice endpoint answers from the local record alone, with no
// check: the window draws it on page load, before anybody clicks
// anything and whether or not the network is up.
func getInstallNotice(t *testing.T, d *Daemon) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/update/install-notice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/update/install-notice = %d: %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func writeNoticeRecord(t *testing.T, dir string, code int) {
	t.Helper()
	if err := update.WriteMSIRecord(dir, update.MSIRecord{
		Version: "0.46.0", ExitCode: code,
		Log:  dir + "/localcode-0.46.0-msi.log",
		Time: time.Now().Truncate(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
}

func noticeDaemon(t *testing.T, dir string) *Daemon {
	t.Helper()
	old := msiRecordDir
	msiRecordDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { msiRecordDir = old })
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Version = "0.45.2"
	return d
}

// A failed record is drawn once: the first load carries the version,
// the exit code and its meaning, and the log, and the second load gets
// nothing.
func TestTheInstallNoticeIsOwedOnce(t *testing.T) {
	dir := t.TempDir()
	writeNoticeRecord(t, dir, 1625)
	d := noticeDaemon(t, dir)

	body := getInstallNotice(t, d)
	notice, ok := body["notice"].(map[string]any)
	if !ok {
		t.Fatalf("notice = %v, want the 1625 failure on first load", body["notice"])
	}
	if notice["version"] != "0.46.0" {
		t.Errorf("version = %v", notice["version"])
	}
	if notice["exit_code"] != float64(1625) {
		t.Errorf("exit_code = %v", notice["exit_code"])
	}
	if notice["status"] != "failed" {
		t.Errorf("status = %v, want failed", notice["status"])
	}
	if meaning, _ := notice["meaning"].(string); !strings.Contains(meaning, "1625") {
		t.Errorf("meaning = %q, want what 1625 means", meaning)
	}
	if notice["log"] != dir+"/localcode-0.46.0-msi.log" {
		t.Errorf("log = %v", notice["log"])
	}
	if body := getInstallNotice(t, d); body["notice"] != nil {
		t.Errorf("notice = %v on second load, want nothing: the line is drawn once", body["notice"])
	}
}

// Another installation in progress is a failure owed a line too.
func TestTheInstallNoticeIsOwedForAnotherInstallation(t *testing.T) {
	dir := t.TempDir()
	writeNoticeRecord(t, dir, 1618)
	d := noticeDaemon(t, dir)

	body := getInstallNotice(t, d)
	notice, ok := body["notice"].(map[string]any)
	if !ok {
		t.Fatalf("notice = %v, want the 1618 refusal on first load", body["notice"])
	}
	if notice["status"] != "another-installation" {
		t.Errorf("status = %v, want another-installation", notice["status"])
	}
	if body := getInstallNotice(t, d); body["notice"] != nil {
		t.Errorf("notice = %v on second load, want nothing", body["notice"])
	}
}

// A cancelled record draws nothing, because the person cancelled it
// themselves. An installed record draws nothing either: the window
// coming back is the whole of that report.
func TestTheInstallNoticeIsSilentForCancelledAndInstalled(t *testing.T) {
	for _, code := range []int{1602, 0, 3010, 1641} {
		dir := t.TempDir()
		writeNoticeRecord(t, dir, code)
		d := noticeDaemon(t, dir)

		if body := getInstallNotice(t, d); body["notice"] != nil {
			status, _ := update.ClassifyMSIExit(code)
			t.Errorf("exit %d (%s): notice = %v, want nothing drawn", code, status, body["notice"])
		}
	}
}

// A record for the running version draws nothing: the install landed
// another way and there is no failure to say.
func TestTheInstallNoticeIsSilentForTheRunningVersion(t *testing.T) {
	dir := t.TempDir()
	writeNoticeRecord(t, dir, 1603)
	d := noticeDaemon(t, dir)
	d.Version = "0.46.0"

	if body := getInstallNotice(t, d); body["notice"] != nil {
		t.Errorf("notice = %v for the running version, want nothing", body["notice"])
	}
}

// A new failure after the first was drawn is drawn again: shown-once is
// per record, not per window.
func TestTheInstallNoticeIsOwedAgainForANewFailure(t *testing.T) {
	dir := t.TempDir()
	writeNoticeRecord(t, dir, 1603)
	d := noticeDaemon(t, dir)

	if body := getInstallNotice(t, d); body["notice"] == nil {
		t.Fatal("first failure drew nothing")
	}
	if body := getInstallNotice(t, d); body["notice"] != nil {
		t.Fatal("first failure drew twice")
	}
	rec, err := update.ReadMSIRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec.ExitCode = 1625
	rec.Time = rec.Time.Add(time.Second)
	if err := update.WriteMSIRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	body := getInstallNotice(t, d)
	notice, ok := body["notice"].(map[string]any)
	if !ok {
		t.Fatalf("notice = %v, want the new 1625 failure drawn again", body["notice"])
	}
	if notice["exit_code"] != float64(1625) {
		t.Errorf("exit_code = %v, want the new failure", notice["exit_code"])
	}
}

// No record means no notice, and the endpoint still answers 200: a
// window that never lost an install has nothing to say.
func TestTheInstallNoticeIsEmptyWithNoRecord(t *testing.T) {
	d := noticeDaemon(t, t.TempDir())
	if body := getInstallNotice(t, d); body["notice"] != nil {
		t.Errorf("notice = %v with no record, want nothing", body["notice"])
	}
}

// Drawing the line leaves the record where it is: the settings panel
// keeps reporting it when somebody clicks Check.
func TestDrawingTheNoticeKeepsTheRecordForThePanel(t *testing.T) {
	dir := t.TempDir()
	writeNoticeRecord(t, dir, 1603)
	d := noticeDaemon(t, dir)

	if body := getInstallNotice(t, d); body["notice"] == nil {
		t.Fatal("failure drew nothing")
	}
	if _, err := update.ReadMSIRecord(dir); err != nil {
		t.Errorf("the drawn record is gone: %v", err)
	}
}
