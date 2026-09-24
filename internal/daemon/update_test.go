package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/events"
	"localcode/internal/update"
)

// A GitHub that answers with one release, so the check can be exercised
// without the internet and without a published version to match against.
func githubWith(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	// Every asset a real release publishes, all nine of them, and the
	// Linux four are the reason this says so. They were missing, so a
	// daemon asked on Linux found nothing built for it: can_install came
	// back false and TestTheDesktopWindowIsOfferedTheInstall failed
	// there — on this platform only, silently, for as long as the gate
	// ran on one machine. The fixture, not the updater: `make dist`
	// writes all nine and check-dist.sh refuses a release missing any.
	v := strings.TrimPrefix(tag, "v")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://github.com/o/r/releases/tag/%s","body":"what changed","assets":[
			{"name":"localcode-%s-windows-amd64.msi","browser_download_url":"https://example/msi","size":10,"digest":"sha256:aa"},
			{"name":"localcode-%s-windows-arm64.zip","browser_download_url":"https://example/zip","size":10,"digest":"sha256:bb"},
			{"name":"LocalCode-%s-darwin-universal-app.tar.gz","browser_download_url":"https://example/app","size":10,"digest":"sha256:cc"},
			{"name":"localcode-%s-darwin-universal.tar.gz","browser_download_url":"https://example/tgz","size":10,"digest":"sha256:dd"},
			{"name":"localcode-%s-linux-amd64.deb","browser_download_url":"https://example/deb64","size":10,"digest":"sha256:ee"},
			{"name":"localcode-%s-linux-amd64.tar.gz","browser_download_url":"https://example/tgz64","size":10,"digest":"sha256:ff"},
			{"name":"localcode-%s-linux-arm64.deb","browser_download_url":"https://example/debarm","size":10,"digest":"sha256:11"},
			{"name":"localcode-%s-linux-arm64.tar.gz","browser_download_url":"https://example/tgzarm","size":10,"digest":"sha256:22"}
		]}`, tag, tag, v, v, v, v, v, v, v, v)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func checkUpdate(t *testing.T, d *Daemon) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/update", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/update = %d: %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestTheUpdateCheckReportsANewerRelease(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Version = "0.45.2"
	d.UpdateAPI = githubWith(t, "v0.46.0").URL

	body := checkUpdate(t, d)
	if body["available"] != true {
		t.Errorf("available = %v, want true (0.46.0 is newer than 0.45.2)", body["available"])
	}
	if body["latest"] != "0.46.0" {
		t.Errorf("latest = %v", body["latest"])
	}
	if body["current"] != "0.45.2" {
		t.Errorf("current = %v", body["current"])
	}
}

func TestTheUpdateCheckSaysWhenThereIsNothingNew(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Version = "0.46.0"
	d.UpdateAPI = githubWith(t, "v0.46.0").URL

	body := checkUpdate(t, d)
	if body["available"] != false {
		t.Errorf("available = %v, want false", body["available"])
	}
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "latest release") {
		t.Errorf("detail = %q", detail)
	}
}

// A build from a working tree has no version to compare, and claiming it
// is up to date — or offering it a downgrade — would both be wrong.
func TestADevBuildIsNotOfferedARelease(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Version = "dev"
	d.UpdateAPI = githubWith(t, "v0.46.0").URL

	body := checkUpdate(t, d)
	if body["available"] != false {
		t.Errorf("available = %v, want false for a dev build", body["available"])
	}
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "not a release build") {
		t.Errorf("detail = %q, want it to say which build this is", detail)
	}
}

// The check is harmless anywhere; installing is not. A daemon reached over
// the network would be replacing the program on the *server* at the
// request of a browser somewhere else — the same rule as the folder
// picker, and the reason the panel has to be told which kind it is talking
// to rather than assuming.
func TestInstallingIsRefusedUnlessTheDaemonIsTheMachineInFrontOfYou(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Version = "0.45.2"
	d.UpdateAPI = githubWith(t, "v0.46.0").URL

	if body := checkUpdate(t, d); body["can_install"] != false {
		t.Errorf("can_install = %v on a daemon that cannot install", body["can_install"])
	}

	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update/install", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/update/install = %d, want 403: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "releases") {
		t.Errorf("the refusal does not say where to get it: %s", rec.Body)
	}
}

func TestTheDesktopWindowIsOfferedTheInstall(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Version = "0.45.2"
	d.AllowUpdateInstall = true
	d.UpdateAPI = githubWith(t, "v0.46.0").URL

	body := checkUpdate(t, d)
	if body["can_install"] != true {
		t.Errorf("can_install = %v, want true in the desktop window", body["can_install"])
	}
	if name, _ := body["asset"].(string); name == "" {
		t.Error("the check did not say which file it would install")
	}
}

// GitHub being unreachable is a sentence beside the button, not a failed
// request: the panel shows this to someone whose network is behind a
// proxy, and a status code tells them nothing.
func TestAFailedCheckAnswersWithTheReason(t *testing.T) {
	unreachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(unreachable.Close)

	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Version = "0.45.2"
	d.UpdateAPI = unreachable.URL

	body := checkUpdate(t, d)
	if body["checked"] != false {
		t.Errorf("checked = %v, want false", body["checked"])
	}
	if detail, _ := body["detail"].(string); detail == "" {
		t.Error("a check that failed said nothing about why")
	}
}

// And installing a release this build already has is refused rather than
// downloading and running an installer for the version already installed.
func TestInstallingWhatIsAlreadyInstalledIsRefused(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Version = "0.46.0"
	d.AllowUpdateInstall = true
	d.UpdateAPI = githubWith(t, "v0.46.0").URL

	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update/install", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /api/update/install = %d, want 409: %s", rec.Code, rec.Body)
	}
}

// Replacing the binary and then leaving the old one running is the fault
// this pins: the install worked, the reply said so, and the version in
// the header never changed because nothing restarted. Whoever read that
// reasonably concluded the update had done nothing.
func TestAReplacedBinaryIsRestartedWhereItCanBe(t *testing.T) {
	replaced := update.Outcome{Replaced: true, Detail: "installed over /home/u/.local/bin/localcode"}

	detail, restarting := restartPlan(replaced, true)
	if !restarting {
		t.Error("a daemon that can restart itself did not, so the old binary keeps running")
	}
	if !strings.Contains(detail, "restarting") {
		t.Errorf("the reply does not say a restart is coming: %q", detail)
	}

	// The same install on a daemon reached from another machine. Restarting
	// it is not a browser's to order, so the sentence has to carry the
	// instruction instead of the process carrying it out.
	detail, restarting = restartPlan(replaced, false)
	if restarting {
		t.Error("a daemon with no restart hook reported a restart it cannot perform")
	}
	if !strings.Contains(detail, "restart localcode") {
		t.Errorf("the reply does not tell the user to restart: %q", detail)
	}
}

// An installer that is running replaces the files itself once localcode
// exits, and a .deb was only downloaded. Neither is this process being
// replaced, so neither gets a restart — nor a sentence about one.
func TestAnInstallThatReplacedNothingIsNotRestarted(t *testing.T) {
	for _, out := range []update.Outcome{
		{Started: true, Detail: "the installer is running"},
		{Detail: "downloaded to /tmp/x.deb — install it with: sudo apt install /tmp/x.deb"},
	} {
		detail, restarting := restartPlan(out, true)
		if restarting {
			t.Errorf("%q was treated as a replacement of the running binary", out.Detail)
		}
		if detail != out.Detail {
			t.Errorf("the answer was rewritten: %q became %q", out.Detail, detail)
		}
	}
}

// The check reports a recorded install that did not land: the version,
// the exit code and what it means, and the log path.
func TestTheCheckReportsAFailedInstall(t *testing.T) {
	dir := t.TempDir()
	log := dir + "/localcode-0.46.0-msi.log"
	if err := update.WriteMSIRecord(dir, update.MSIRecord{Version: "0.46.0", ExitCode: 1602, Log: log}); err != nil {
		t.Fatal(err)
	}
	old := msiRecordDir
	msiRecordDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { msiRecordDir = old })

	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Version = "0.45.2"
	d.UpdateAPI = githubWith(t, "v0.46.0").URL

	body := checkUpdate(t, d)
	last, ok := body["last_install"].(map[string]any)
	if !ok {
		t.Fatalf("last_install = %v, want the recorded install", body["last_install"])
	}
	if last["version"] != "0.46.0" {
		t.Errorf("version = %v", last["version"])
	}
	if last["exit_code"] != float64(1602) {
		t.Errorf("exit_code = %v", last["exit_code"])
	}
	if meaning, _ := last["meaning"].(string); !strings.Contains(meaning, "cancelled") {
		t.Errorf("meaning = %q, want what 1602 means", meaning)
	}
	if last["log"] != log {
		t.Errorf("log = %v, want %q", last["log"], log)
	}
}

// The terminal install notice is a recovered error event: both clients
// draw that as a note that ends nothing. A plain error ends the TUI's
// turn while the daemon keeps running it, and the Web UI paints it as a
// failure for the same turn.
func TestTheTerminalInstallNoticeEndsNothing(t *testing.T) {
	const detail = "The installer for localcode 0.46.0 starts when localcode exits. Quit localcode to run it."
	ev := msiTerminalNotice(detail)
	if ev.Type != events.TypeError {
		t.Errorf("Type = %q, want the error event both clients already handle", ev.Type)
	}
	if errMsg, _ := ev.Data["error"].(string); errMsg != detail {
		t.Errorf("error = %q, want the install reply", errMsg)
	}
	if recovered, _ := ev.Data["recovered"].(bool); !recovered {
		t.Error("recovered is not true, so the TUI ends its turn while the daemon keeps running it")
	}
}

// An install that succeeded and is now the running version says nothing,
// and the record is cleared.
func TestTheCheckSaysNothingAboutTheRunningInstall(t *testing.T) {
	dir := t.TempDir()
	if err := update.WriteMSIRecord(dir, update.MSIRecord{Version: "0.46.0", ExitCode: 0, Log: dir + "/x.log"}); err != nil {
		t.Fatal(err)
	}
	old := msiRecordDir
	msiRecordDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { msiRecordDir = old })

	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Version = "0.46.0"
	d.UpdateAPI = githubWith(t, "v0.46.0").URL

	body := checkUpdate(t, d)
	if last, ok := body["last_install"]; ok && last != nil {
		t.Errorf("last_install = %v, want nothing for the running version", last)
	}
	if _, err := update.ReadMSIRecord(dir); err == nil {
		t.Error("the spent record was kept")
	}
}
