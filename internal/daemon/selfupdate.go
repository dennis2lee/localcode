package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"localcode/internal/events"
	"localcode/internal/update"
)

// The half of updating that is the same wherever it is asked for: work
// out whether there is a newer release, pick the asset for this machine
// and this install shape, and get it onto disk with its checksum checked.
//
// Two callers ask: the settings window's install button, over HTTP, and
// "/update" typed into the prompt box. They differ in what they do with a
// refusal — one has status codes, the other has a sentence — and in
// nothing else, so the refusals are values here and the phrasing belongs
// to whoever is answering.
var (
	errAlreadyLatest   = errors.New("already the latest release")
	errNoAssetForBuild = errors.New("no asset for this platform")
)

// fetchLatest resolves the newest release and downloads it, verified.
//
// It stops short of installing. Applying an update replaces the running
// program, which is the one step whose consequences differ per caller:
// the button restarts a TUI it can see, and a command has to say what it
// is about to do to the conversation it is answering in.
func (d *Daemon) fetchLatest(ctx context.Context, forHandoff bool) (rel update.Release, path string, verified bool, err error) {
	return d.fetchLatestFrom(ctx, d.Version, forHandoff)
}

// fetchLatestFrom is fetchLatest measured against a version other than
// this process's own: the one that will actually run the daemon after a
// startup handoff, which on a Windows install under Program Files is a
// staged copy and not this binary.
func (d *Daemon) fetchLatestFrom(ctx context.Context, running string, forHandoff bool) (rel update.Release, path string, verified bool, err error) {
	rel, err = d.updateChecker().Latest(ctx)
	if err != nil {
		return rel, "", false, err
	}
	if !update.Newer(running, rel.Version) {
		return rel, "", false, fmt.Errorf("localcode %s is %w", running, errAlreadyLatest)
	}
	var asset update.Asset
	if forHandoff {
		asset, err = rel.HandoffAssetFor(runtime.GOOS, runtime.GOARCH, bundledApp())
	} else {
		asset, err = rel.AssetFor(runtime.GOOS, runtime.GOARCH, bundledApp())
	}
	if err != nil {
		return rel, "", false, fmt.Errorf("%w: %v", errNoAssetForBuild, err)
	}
	dir, err := updateDir()
	if err != nil {
		return rel, "", false, err
	}
	// A file share publishes the installer and usually nothing else, so
	// there is no checksum in the listing to check the download against. A
	// sibling "<asset>.sha256" is used where somebody published one.
	if d.Loop.Config.UpdateURL != "" && asset.Digest == "" {
		asset.Digest = d.updateChecker().DigestFor(ctx, asset.URL)
	}
	// Deliberately not the caller's context for the transfer itself: a
	// browser that gives up on a slow download would otherwise abort a
	// twenty-megabyte transfer most of the way through.
	path, err = update.Download(context.Background(), nil, asset, dir)
	if err != nil {
		return rel, "", false, err
	}
	return rel, path, asset.Digest != "", nil
}

// updateHTTPStatus maps a refusal to the code the settings window reads.
func updateHTTPStatus(err error) int {
	switch {
	case errors.Is(err, errAlreadyLatest):
		return http.StatusConflict
	case errors.Is(err, errNoAssetForBuild):
		return http.StatusNotImplemented
	default:
		return http.StatusBadGateway
	}
}

// SelfUpdate answers "/update" for the session that typed it.
//
// It refuses while anything is running, and the refusal names what. This
// is not the caution the install button needs: that button is clicked by
// somebody looking at the screen, in a window that shows them the turn
// they would be interrupting. A command is typed into one conversation
// and replaces the program for all of them, so the thing it is about to
// kill is routinely in a session nobody is currently looking at.
//
// The caller's own session is left out of the turn check. A command
// cannot be sent into a turn in the first place — both clients refuse it
// there — so a turn recorded against this session is this command, and
// counting it would make "/update" refuse itself every time.
func (d *Daemon) SelfUpdate(sessionID string) (string, error) {
	updateMu.Lock()
	defer updateMu.Unlock()

	if !d.AllowUpdateInstall {
		// The same rule the settings window's install button follows: this
		// daemon can be reached from another machine, so replacing the
		// program it runs is not a request a client gets to make.
		//
		// Said together with the startup half, because otherwise the two
		// look like a contradiction — this daemon does update itself, just
		// not when asked to from somewhere that might not be here.
		return "", fmt.Errorf("this daemon can be reached from another machine, so a client cannot replace the program it runs. "+
			"It still installs updates at startup unless auto_update is off; otherwise get it from %s", d.updateSource())
	}

	// What is running decides how the update lands, not whether. With a
	// handoff wired, work in flight is the reason to hand off rather
	// than restart: the new daemon takes the address and this one
	// finishes its turns. Without one — a desktop window, Windows, a
	// daemon attached over the network — a restart is the only way back
	// onto the new binary, and it would cut that work, so it is refused
	// and the refusal names what it found.
	busy := othersThan(d.turns.running(), sessionID)
	var tasks []string
	if d.Tasks != nil {
		tasks = d.Tasks.Running()
	}
	if err := updateWhileBusy(d.Handoff != nil, busy, tasks); err != nil {
		return "", err
	}

	// With a handoff wired the install is the one a handoff needs — a
	// runnable binary, nothing closing this process — which on Windows is
	// the zip rather than the MSI. See update.ApplyForHandoff.
	handoff := d.Handoff != nil
	rel, path, verified, err := d.fetchLatest(context.Background(), handoff)
	switch {
	case errors.Is(err, errAlreadyLatest):
		return fmt.Sprintf("localcode %s is already the latest release.", d.Version), nil
	case err != nil:
		return "", err
	}

	var out update.Outcome
	if handoff {
		out, err = update.ApplyForHandoff(path)
	} else {
		out, err = update.Apply(path)
	}
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "localcode %s installed from %s.\n", rel.Version, d.updateSource())
	if !verified {
		// Said rather than left unsaid: it is a true thing about a file
		// that has just been run as an installer.
		b.WriteString("The download could not be checked against a published checksum.\n")
	}

	if out.Replaced && d.Handoff != nil {
		// The binary on disk is the new one. Hand the address to it and
		// finish here: nothing on screen changes hands, and a turn that
		// is running keeps running where it started.
		b.WriteString(out.Detail)
		b.WriteString("\nHanding over to it now. This conversation stays where it is: the new localcode takes the " +
			"address, and anything still running finishes in this one first. " +
			"The next message goes to the new version.")
		if n := len(busy) + len(tasks); n > 0 {
			fmt.Fprintf(&b, " %d conversation(s) still have work in flight and will be the last to move.", n)
		}
		version, binary := rel.Version, out.Binary
		go func() {
			time.Sleep(restartDelay)
			if err := d.Handoff(version, binary); err != nil {
				// Said on every stream, since the reply above promised
				// something that did not happen.
				d.daemonEvents.send(events.Event{
					Type: events.TypeError,
					Data: map[string]any{"error": "handoff to localcode " + version + " failed: " + err.Error()},
				})
			}
		}()
		return b.String(), nil
	}

	detail, restarting := restartPlan(out, d.Restart != nil)
	b.WriteString(detail)
	if restarting {
		// What a restart does and does not cost, because the version in
		// the header changing is not by itself an explanation for a
		// conversation appearing to reload.
		b.WriteString("\nThis conversation and every other one comes back: the session list, " +
			"the history and the token totals are read from disk, and the browser reconnects on its own.")
		go func() {
			time.Sleep(restartDelay)
			d.Restart()
		}()
	}
	return b.String(), nil
}

// updateWhileBusy is the one decision "/update" makes about work in
// flight: with a handoff available it is not an obstacle, and without
// one it is a refusal that names what it found.
func updateWhileBusy(canHandoff bool, busy, tasks []string) error {
	if canHandoff || (len(busy) == 0 && len(tasks) == 0) {
		return nil
	}
	if len(busy) > 0 {
		return fmt.Errorf("%d other conversation(s) have a turn in progress: %s. "+
			"Updating replaces this program and takes every one of them with it. "+
			"Wait for them, or stop them, then run /update again",
			len(busy), strings.Join(busy, ", "))
	}
	// Scheduled runs are not asked about separately: one runs in a session
	// of its own and holds a turn there for as long as it lasts, so it is
	// already in the answer above, under the id it runs as.
	return fmt.Errorf("%d background task(s) are still working: %s. "+
		"Updating would stop them where they are. "+
		"Wait for them, or cancel them with /tasks cancel <id>, then run /update again",
		len(tasks), strings.Join(tasks, ", "))
}

// ErrNoUpdate is "there was nothing to install", which is the ordinary
// answer and not a failure. Startup separates it from the rest so it can
// say nothing at all in the common case.
var ErrNoUpdate = errors.New("no newer release")

// InstallAtStartup installs a newer release before anything is served.
//
// No busy check and no restart: at startup there is nothing running to
// refuse for, and bringing the new binary up is the caller's to do — it
// is the one that knows whether it can exec, and it has not yet built
// anything it would have to take down first.
//
// forHandoff asks for the install a handoff needs rather than the
// platform's packaged one — on Windows the zip and a rename or a staged
// copy, never the MSI — because the caller is about to start the new
// binary beside this process rather than exec into it. See
// update.ApplyForHandoff. running is the version to compare the release
// against, which is this process's own except where a staged copy is
// already newer than it.
func (d *Daemon) InstallAtStartup(ctx context.Context, running string, forHandoff bool) (update.Outcome, string, error) {
	updateMu.Lock()
	defer updateMu.Unlock()

	rel, path, verified, err := d.fetchLatestFrom(ctx, running, forHandoff)
	switch {
	case errors.Is(err, errAlreadyLatest), errors.Is(err, errNoAssetForBuild):
		// Both are "this machine has nothing to install", not a fault:
		// the release is the one already running, or it ships nothing for
		// this platform and install shape.
		return update.Outcome{}, "", ErrNoUpdate
	case err != nil:
		return update.Outcome{}, "", err
	}
	var out update.Outcome
	if forHandoff {
		out, err = update.ApplyForHandoff(path)
	} else {
		out, err = update.Apply(path)
	}
	if err != nil {
		return update.Outcome{}, "", err
	}
	if !out.Replaced {
		// An installer is running, or a package was left on disk for a
		// package manager. Either way this process is not about to become
		// the new version, so the caller must not exec into it.
		return out, "", fmt.Errorf("localcode %s: %s", rel.Version, out.Detail)
	}
	line := fmt.Sprintf("Updated to localcode %s before starting.", rel.Version)
	if !verified {
		line += " The download could not be checked against a published checksum."
	}
	return out, line, nil
}

// othersThan is the running-session list without the one asking.
func othersThan(ids []string, self string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != self {
			out = append(out, id)
		}
	}
	return out
}
