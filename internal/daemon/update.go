package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

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
func (d *Daemon) updateChecker() update.Checker {
	return update.Checker{API: d.UpdateAPI}
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
		"current":     d.Version,
		"checked":     true,
		"latest":      rel.Version,
		"tag":         rel.Tag,
		"page_url":    rel.PageURL,
		"notes":       rel.Notes,
		"available":   update.Newer(d.Version, rel.Version),
		"can_install": d.AllowUpdateInstall,
	}
	if !update.Newer(d.Version, rel.Version) {
		// "dev" is not a version, so nothing is ever newer than it. Saying
		// "up to date" there would be a claim this cannot make.
		if d.Version == "" || d.Version == "dev" {
			body["detail"] = "this is not a release build, so there is nothing to compare " + rel.Version + " against"
		} else {
			body["detail"] = "localcode " + d.Version + " is the latest release"
		}
		writeJSON(w, http.StatusOK, body)
		return
	}

	asset, err := rel.AssetFor(runtime.GOOS, runtime.GOARCH, bundledApp())
	if err != nil {
		body["available"] = true
		body["can_install"] = false
		body["detail"] = err.Error()
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
	writeJSON(w, http.StatusOK, body)
}

// handleUpdateInstall downloads the release and hands it to the platform's
// installer.
func (d *Daemon) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if !d.AllowUpdateInstall {
		writeError(w, http.StatusForbidden, fmt.Errorf("this localcode cannot install updates for you; download it from https://github.com/%s/releases", update.DefaultRepo))
		return
	}

	updateMu.Lock()
	defer updateMu.Unlock()

	rel, err := d.updateChecker().Latest(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if !update.Newer(d.Version, rel.Version) {
		writeError(w, http.StatusConflict, fmt.Errorf("localcode %s is already the latest release", d.Version))
		return
	}
	asset, err := rel.AssetFor(runtime.GOOS, runtime.GOARCH, bundledApp())
	if err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}

	dir, err := updateDir()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Deliberately not r.Context(): a browser that gives up on a slow
	// download would otherwise abort a twenty-megabyte transfer most of
	// the way through, and the client's own request has a deadline it
	// cannot usefully hold for the whole of one.
	path, err := update.Download(context.Background(), nil, asset, dir)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	out, err := update.Apply(path)
	if err != nil {
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
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    rel.Version,
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
		// an installer is running and will do it. Both already say what
		// happens next in their own words.
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
