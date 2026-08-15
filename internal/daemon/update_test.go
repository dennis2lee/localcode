package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A GitHub that answers with one release, so the check can be exercised
// without the internet and without a published version to match against.
func githubWith(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://github.com/o/r/releases/tag/%s","body":"what changed","assets":[
			{"name":"localcode-%s-windows-amd64.msi","browser_download_url":"https://example/msi","size":10,"digest":"sha256:aa"},
			{"name":"localcode-%s-windows-arm64.zip","browser_download_url":"https://example/zip","size":10,"digest":"sha256:bb"},
			{"name":"LocalCode-%s-darwin-universal-app.tar.gz","browser_download_url":"https://example/app","size":10,"digest":"sha256:cc"},
			{"name":"localcode-%s-darwin-universal.tar.gz","browser_download_url":"https://example/tgz","size":10,"digest":"sha256:dd"}
		]}`, tag, tag, strings.TrimPrefix(tag, "v"), strings.TrimPrefix(tag, "v"), strings.TrimPrefix(tag, "v"), strings.TrimPrefix(tag, "v"))
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
