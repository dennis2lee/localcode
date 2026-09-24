package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"localcode/internal/events"
	"localcode/internal/update"
)

// Updating is a check and an install, and they are separate on purpose.
//
// Neither happens on its own. Checking is an outbound request to GitHub
// that says which version this machine is running, and installing replaces
// the program while someone is using it — so both are things the person in
// front of it asks for, and the install asks again before it starts.

// updateMu stops two clicks from downloading the same release twice.
var updateMu sync.Mutex

// updateChecker is where releases are looked up. UpdateAPI is empty in
// every real build, so this is GitHub; a test points it somewhere it
// controls, because a test that needs the internet and a published release
// is not a test.
// updateSource names where releases are looked up, for a client that
// should say so rather than implying GitHub.
func (d *Daemon) updateSource() string {
	if u := d.Loop.Config.UpdateURL; u != "" {
		return u
	}
	return "https://github.com/" + update.DefaultRepo + "/releases"
}

// updateSourceUnverified reports whether the configured source is fetched
// over plain http. The URL alone does not tell a reader that nothing
// authenticated the host, so handlers report this beside "source" and
// every client says it wherever the source is named, on every check and
// every install offer, not once.
//
// A field beside the source rather than text inside it: the source stays
// the address (something to link, something to compare), and the marker
// stays a decision the panel renders. Folding them together would make
// every client parse the address back out.
func (d *Daemon) updateSourceUnverified() bool {
	raw := strings.TrimSpace(d.Loop.Config.UpdateURL)
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "http")
}

// httpSourceNote is the sentence said wherever an http update source is
// named in prose: the TUI's /update reply, and the refusals that point at
// the source. It states the bound honestly: the network is trusted, the
// host is not authenticated.
const httpSourceNote = "The source uses plain http, so the host was not authenticated: anyone on that network could have substituted the file."

func (d *Daemon) updateChecker() update.Checker {
	// config.json's update_url wins when it is set, which is the whole of
	// how an internal build gets installed instead of a public one.
	return update.Checker{API: d.UpdateAPI, URL: d.Loop.Config.UpdateURL}
}

// handleUpdateCheck asks GitHub what the latest release is and reports it
// against this build.
func (d *Daemon) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	rel, err := d.updateChecker().Latest(r.Context())
	if err != nil {
		// 200 with the reason, not an HTTP error: the panel shows this
		// beside the button that was clicked, and "failed to fetch" with a
		// status code is not what someone whose network is behind a proxy
		// needs to read.
		writeJSON(w, http.StatusOK, map[string]any{
			"current": d.Version,
			"checked": false,
			"detail":  err.Error(),
		})
		return
	}

	body := map[string]any{
		"current": d.Version,
		"checked": true,
		// Which source answered. A machine configured to update from an
		// internal address should say so on the panel: otherwise "0.65.0
		// is available" reads as a public release and nobody notices the
		// build is coming from somewhere else entirely.
		"source": d.updateSource(),
		// Beside the source, not inside it: an http address alone does
		// not say the host was never authenticated, and the panel has to
		// say it on every offer. See updateSourceUnverified.
		"source_unverified": d.updateSourceUnverified(),
		"latest":            rel.Version,
		"tag":               rel.Tag,
		"page_url":          rel.PageURL,
		"notes":             rel.Notes,
		"available":         update.Newer(d.Version, rel.Version),
		"can_install":       d.AllowUpdateInstall,
	}
	if !update.Newer(d.Version, rel.Version) {
		// "dev" is not a version, so nothing is ever newer than it. Saying
		// "up to date" there would be a claim this cannot make.
		if d.Version == "" || d.Version == "dev" {
			body["detail"] = "this is not a release build, so there is nothing to compare " + rel.Version + " against"
		} else {
			body["detail"] = "localcode " + d.Version + " is the latest release"
		}
		if last := d.lastInstallReport(); last != nil {
			body["last_install"] = last
		}
		writeJSON(w, http.StatusOK, body)
		return
	}

	asset, err := rel.AssetFor(runtime.GOOS, runtime.GOARCH, bundledApp())
	if err != nil {
		body["available"] = true
		body["can_install"] = false
		body["detail"] = err.Error()
		if last := d.lastInstallReport(); last != nil {
			body["last_install"] = last
		}
		writeJSON(w, http.StatusOK, body)
		return
	}
	body["asset"] = asset.Name
	body["size"] = asset.Size
	if !d.AllowUpdateInstall {
		// A daemon someone reached over the network. Installing would
		// replace the program on the *server*, at the request of a browser
		// somewhere else, which is not a thing a button should be able to
		// do — the same rule as the folder picker.
		body["detail"] = "install it on the machine running localcode, or from " + rel.PageURL
	}
	// Beside the offer, not instead of it: a failed install does not
	// stop the next one being offered.
	if last := d.lastInstallReport(); last != nil {
		body["last_install"] = last
	}
	writeJSON(w, http.StatusOK, body)
}

// msiRecordDir is where the helper's record is read from. A variable so
// a test can point it at a directory it controls rather than the user's
// cache.
var msiRecordDir = updateDir

// lastInstallReport reads the helper's record for the panel. Included
// when the record did not install the version it was for: cancelled,
// failed, or still not the running version. Cleared when the running
// version is what a successful install put there, which is when the
// record has served its purpose and says nothing.
func (d *Daemon) lastInstallReport() map[string]any {
	dir, err := msiRecordDir()
	if err != nil {
		return nil
	}
	rec, err := update.ReadMSIRecord(dir)
	if err != nil {
		return nil
	}
	if !update.ReportMSIRecord(rec, d.Version) {
		_ = update.ClearMSIRecord(dir)
		return nil
	}
	return map[string]any{
		"version":   rec.Version,
		"exit_code": rec.ExitCode,
		"meaning":   update.MSIExitMeaning(rec.ExitCode),
		"log":       rec.Log,
	}
}

// handleUpdateInstall downloads the release and hands it to the platform's
// installer.
func (d *Daemon) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	updateMu.Lock()
	defer updateMu.Unlock()

	// No busy check here, unlike "/update". This button is clicked by
	// somebody looking at the window that shows them the turn they would
	// be interrupting; a command typed in one conversation replaces the
	// program for every conversation, including the ones nobody is
	// watching. See Daemon.SelfUpdate.
	if !d.AllowUpdateInstall {
		src := d.updateSource()
		if d.updateSourceUnverified() {
			src += ". " + httpSourceNote
		}
		writeError(w, http.StatusForbidden, fmt.Errorf(
			"this localcode cannot install updates for you; download it from %s", src))
		return
	}
	rel, path, verified, err := d.fetchLatest(r.Context(), false)
	if err != nil {
		writeError(w, updateHTTPStatus(err), err)
		return
	}
	out, err := update.Apply(path)
	if err != nil {
		// A helper still waiting or installing refuses a second one.
		// 409, not 500: the request was understood and the state, not
		// the server, is what refuses it.
		var pending *update.ErrMSIPending
		if errors.As(err, &pending) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// The binary on disk is the new one and this process is still the old
	// one, so something has to bring it back. Nothing did: the reply said
	// "restart localcode to run the new version" and left it there, which
	// is how an update that worked reads as an update that did nothing —
	// the version in the header does not change, and the next thing the
	// user does is run the same old build.
	detail, restarting := restartPlan(out, d.Restart != nil)
	if out.Started {
		// The window's reply is the window's: the installer starts when
		// it closes, and it opens again when the installer has
		// finished. Everywhere else the person at the terminal has to
		// quit, so the reply says that instead.
		detail = update.MSIDetail(rel.Version, d.DesktopWindow)
		if !d.DesktopWindow {
			// The browser that clicked is not the only client, and the
			// person at the terminal is the one who has to quit. Said
			// on every stream, the way a failed handoff is: the reply
			// above goes to the browser, and the terminal never sees
			// it otherwise.
			d.daemonEvents.send(events.Event{
				Type: events.TypeError,
				Data: map[string]any{"error": detail},
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": rel.Version,
		"source":  d.updateSource(),
		// Said here too, not only on the check: the install reply is its
		// own offer, read on its own.
		"source_unverified": d.updateSourceUnverified(),
		// Whether what was downloaded could be checked against a
		// published checksum. False is not a failure and is not hidden:
		// it is a true thing about a file that has just been run as an
		// installer, and the panel says it.
		"verified":   verified,
		"started":    out.Started,
		"replaced":   out.Replaced,
		"restarting": restarting,
		"path":       out.Path,
		"detail":     detail,
	})
	if restarting {
		// After the response, and after enough of a pause for it to reach
		// the browser: the restart takes the HTTP server with it, and a
		// client that never sees the answer cannot say what happened.
		go func() {
			time.Sleep(restartDelay)
			d.Restart()
		}()
	}
}

// restartDelay is how long the reply is given to reach the client before
// the process that sent it goes away. A variable so a test does not have
// to wait it out.
var restartDelay = 400 * time.Millisecond

// restartPlan decides what the install reply says, and whether this
// process is about to be replaced by the version it just installed.
//
// Separate from the handler because it is the part worth pinning: the
// handler around it downloads a release and writes over this program's
// own binary, which is not something a test can be asked to do to itself.
func restartPlan(out update.Outcome, canRestart bool) (detail string, restarting bool) {
	if !out.Replaced {
		// Either nothing was replaced (a .deb, a Windows zip, a bundle) or
		// an installer is staged and will do it once localcode exits.
		// Both already say what happens next in their own words.
		//
		// The installer case does not become a restart. A terminal or a
		// headless daemon is never started back: a new console is not the
		// terminal the person is sitting in. The window is started back
		// by the helper, on its own terms rather than this process's, so
		// there is nothing here to wait on either. Saying "restarting
		// localcode now" and then doing anything else would be worse than
		// saying nothing.
		return out.Detail, false
	}
	if canRestart {
		return out.Detail + " — restarting localcode now", true
	}
	// Replaced, and nobody here can bring it back: a daemon reached over
	// the network, whose restart is not a browser's to order. Saying so is
	// the whole of what is left, and it has to be said — an update that
	// worked and changes nothing on screen reads as one that did not.
	return out.Detail + " — restart localcode to run the new version", false
}

// updateDir is where downloads are kept: the user's cache directory, since
// an installer is disposable the moment it has run.
func updateDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("no cache directory to download into: %w", err)
	}
	return filepath.Join(base, "localcode", "updates"), nil
}

// bundledApp reports whether this copy of localcode came from a package
// rather than from an archive someone unpacked, which decides which of a
// platform's two downloads is the right one.
//
// Two different signals for the two platforms that have two downloads: a
// macOS .app has the binary inside the bundle, and the Debian package
// puts it in /usr/bin. Neither is proof — a tarball unpacked into
// /usr/bin looks packaged, and there is nothing better to go on without
// asking dpkg, which is a subprocess and a distribution assumption to
// answer a question this size.
func bundledApp() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	path := filepath.ToSlash(exe)
	if strings.Contains(path, ".app/Contents/MacOS/") {
		return true
	}
	return runtime.GOOS == "linux" && path == "/usr/bin/localcode"
}
