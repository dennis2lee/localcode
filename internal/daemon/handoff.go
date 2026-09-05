package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"localcode/internal/events"
)

// Handing a daemon to a newer version of itself, without the TUI on top
// of it noticing.
//
// The TUI is an HTTP client, even in the default mode where it shares a
// process with the daemon: it dials the loopback address like any other
// client. So the daemon under it can be replaced the way a web server is
// replaced without dropping connections — the new one is started with
// the listening socket already open, begins accepting, and the old one
// stops accepting and finishes what it was doing. The screen never
// clears, because nothing that draws it was replaced.
//
// The one thing two daemons must never do is write the same session at
// the same time. Each keeps the log in memory and hands out sequence
// numbers from a counter, so a second writer would either collide on a
// number or append to a copy the first one no longer has. The old daemon
// therefore publishes which sessions it is still writing — the ones with
// a turn or a background task — and the new one refuses those with the
// same 409 the clients already queue on, until the old one releases
// them. A released session is re-read from disk before the new daemon
// writes it, because its copy dates from before the old one finished.

// handoffFile is the manifest, in the sessions directory so both
// processes find it by the one path they share.
const handoffFile = ".handoff.json"

// handoffManifest is what the retiring daemon publishes while it drains.
type handoffManifest struct {
	// PID is the retiring daemon's, so a stale file from a process that
	// died mid-drain does not lock its sessions forever.
	PID int `json:"pid"`
	// Sessions it is still writing.
	Sessions []string `json:"sessions"`
}

// ErrOwnedElsewhere says a session is still being written by the daemon
// this one replaced.
var ErrOwnedElsewhere = errors.New("still being finished by the previous localcode")

// OwnedSessions reports every session this daemon is still writing: one
// with a turn in flight, one with a background task, and one a task
// will report back to when it ends.
func (d *Daemon) OwnedSessions() []string {
	set := map[string]bool{}
	for _, id := range d.turns.running() {
		set[id] = true
	}
	if d.Tasks != nil {
		for _, id := range d.Tasks.Running() {
			set[id] = true
		}
		for _, id := range d.Tasks.RunningParents() {
			set[id] = true
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (d *Daemon) handoffPath() string {
	dir := d.Loop.Store.Dir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, handoffFile)
}

// publishOwned writes the manifest, or removes it when nothing is owned.
// Written whole and renamed into place, so the other daemon never reads
// half a list.
func (d *Daemon) publishOwned() error {
	path := d.handoffPath()
	if path == "" {
		return nil
	}
	owned := d.OwnedSessions()
	if len(owned) == 0 {
		err := os.Remove(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(handoffManifest{PID: os.Getpid(), Sessions: owned})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ownedElsewhere reports whether the daemon this one replaced is still
// writing the session. Read from disk on every call: the file is tiny,
// it changes as the other daemon finishes turns, and caching it would be
// caching the one fact that is supposed to change.
func (d *Daemon) ownedElsewhere(sessionID string) bool {
	path := d.handoffPath()
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var m handoffManifest
	if json.Unmarshal(data, &m) != nil || m.PID == os.Getpid() {
		return false
	}
	if !pidAlive(m.PID) {
		// A retiring daemon that died mid-drain. Its sessions are nobody's
		// now, and a file that says otherwise would lock them forever.
		_ = os.Remove(path)
		return false
	}
	for _, id := range m.Sessions {
		if id == sessionID {
			return true
		}
	}
	return false
}

// takeOwnership is what the new daemon does the first time it is about
// to write a session the old one has released: read the session again,
// because the copy it loaded at startup predates the old daemon's last
// writes to it.
//
// Once per session, tracked in memory. The manifest going away is the
// signal that the old daemon is done with all of them; a session it
// never listed needs no reload.
func (d *Daemon) takeOwnership(sessionID string) error {
	d.takeoverMu.Lock()
	if d.tookOver == nil {
		d.tookOver = map[string]bool{}
	}
	// Sessions the retiring daemon never listed were not being written
	// after this daemon loaded them, so their copies are current and
	// there is nothing to re-read.
	suspect := d.ownedAtStart[sessionID] && !d.tookOver[sessionID]
	d.tookOver[sessionID] = true
	d.takeoverMu.Unlock()
	if !suspect {
		return nil
	}
	// Outside the lock: a reload reads a file, and the gate runs this on
	// every write to the session, some of them concurrent.
	d.Loop.ClearSessionState(sessionID)
	return d.Loop.Store.Reload(sessionID)
}

// NoteTakeover records, at startup, which sessions the retiring daemon
// was still writing, so takeOwnership knows which copies are suspect.
// Called by the process that was started with an inherited listener,
// before it serves anything.
func (d *Daemon) NoteTakeover() {
	path := d.handoffPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m handoffManifest
	if json.Unmarshal(data, &m) != nil {
		return
	}
	d.takeoverMu.Lock()
	d.ownedAtStart = map[string]bool{}
	for _, id := range m.Sessions {
		d.ownedAtStart[id] = true
	}
	d.takeoverMu.Unlock()
}

// claimSession is the check every write path to a session goes through
// on a daemon that took over: refuse while the old daemon still owns it,
// and re-read it the first time it is free.
func (d *Daemon) claimSession(sessionID string) error {
	if d.ownedElsewhere(sessionID) {
		return ErrOwnedElsewhere
	}
	return d.takeOwnership(sessionID)
}

// Retire is the retiring daemon's half: stop taking work, finish what is
// going, and say so on every stream.
//
// The listener has already been handed to the new daemon and closed
// here by the time this runs, so no new connection reaches this process;
// what remains is the turns and tasks already inside it, and the streams
// clients are holding. Those get one event naming the replacement, so a
// client reconnects now rather than at its next retry, and then the
// streams end the way a lagging one does. The manifest is rewritten as
// each owned session finishes, so the new daemon can start on it without
// waiting for the whole drain.
//
// Reports whether everything finished before ctx ended.
func (d *Daemon) Retire(ctx context.Context, newVersion string, newPID int) bool {
	if d.Loop.Schedules != nil {
		d.Loop.Schedules.Disarm()
	}
	if err := d.publishOwned(); err != nil {
		fmt.Fprintf(os.Stderr, "handoff: could not publish owned sessions: %v\n", err)
	}
	d.daemonEvents.send(events.Event{
		Type: events.TypeDaemonReplaced,
		Data: map[string]any{"version": newVersion, "pid": newPID},
	})

	// Re-publish as sessions finish. The tracker's change hook is the
	// natural place, but it is one function and it is already taken, so
	// a short poll does the same job with nothing to unwire afterwards.
	stopWatch := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-tick.C:
				_ = d.publishOwned()
			}
		}
	}()

	done := d.Loop.Drain(ctx)
	close(stopWatch)
	wg.Wait()
	if err := d.publishOwned(); err != nil {
		fmt.Fprintf(os.Stderr, "handoff: could not clear owned sessions: %v\n", err)
	}
	d.Loop.Store.EndAllStreams()
	// And let go of the session logs.
	//
	// A daemon used to own its files for as long as it ran, and that was
	// true until a handoff made two daemons exist at once. After one, the
	// retiring daemon is still a live process — the window, or the
	// terminal the TUI is in — holding every sessions/*.jsonl open, while
	// the successor is the one being asked to write and delete them. On
	// Windows a file cannot be removed while another process has it open,
	// so "delete session" answered:
	//
	//   remove ...\sessions\s-1788558914648805100.jsonl: The process
	//   cannot access the file because it is being used by another process
	//
	// Nothing is lost by closing here. The work has drained, the streams
	// have ended, and what this daemon owned has been published to the
	// successor; an append after this point would be a second writer on a
	// file the new daemon owns, which is worse than the event going
	// nowhere.
	d.Loop.Store.Close()
	return done
}

// handleShutdown stops this daemon at a client's request.
//
// It exists for one client: the TUI that used to be this daemon's own
// process and now is not, because the daemon under it was replaced. When
// that TUI exits, the daemon it left behind would otherwise run on with
// nothing attached to it. The same guard as installing: only where the
// client and the daemon share a machine.
func (d *Daemon) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if !d.AllowUpdateInstall {
		writeError(w, http.StatusForbidden, fmt.Errorf("this daemon is not one a client may stop"))
		return
	}
	if d.Shutdown == nil {
		writeError(w, http.StatusNotImplemented, fmt.Errorf("this daemon has no shutdown wired"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopping": true})
	go func() {
		time.Sleep(restartDelay)
		d.Shutdown()
	}()
}

// ownershipGate refuses, with the 409 both clients queue on, any write to
// a session the daemon this one replaced is still finishing — and reads
// such a session again the first time it is free.
//
// In front of the mux rather than in each handler, because the rule is
// about the session and not about what is being done to it: renaming,
// archiving, booking work, answering a prompt and sending a message all
// append to the same log, and a handler added next month must not be the
// one that forgot. Reads are let through: the copy this daemon holds is
// complete up to the moment it started, which is what a client scrolling
// back is looking at, and the stream reconnects on its own once the
// session changes hands.
//
// The path is parsed here because the mux has not matched yet. Every
// session route is "/api/sessions/{id}" or "/api/sessions/{id}/..."; the
// list route has no id and is not a write to any one session.
func (d *Daemon) ownershipGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if id := sessionIDInPath(r.URL.Path); id != "" {
				if err := d.claimSession(id); err != nil {
					if errors.Is(err, ErrOwnedElsewhere) {
						writeJSON(w, http.StatusConflict, map[string]any{
							"error": fmt.Sprintf("session %s is %v; it will be free in a moment", id, err),
						})
						return
					}
					writeError(w, http.StatusInternalServerError, fmt.Errorf("take over session %s: %w", id, err))
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sessionIDInPath is the {id} of a session route, or "" for any other
// path.
func sessionIDInPath(path string) string {
	const prefix = "/api/sessions/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
