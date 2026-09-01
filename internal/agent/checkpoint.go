package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"localcode/internal/events"
)

// The file half of "/rewind": a copy of each file as it stood before the
// turn that changed it.
//
// Only files changed through write_file and edit, which is Claude Code's
// documented scope and is enforced here structurally rather than promised
// in a prompt: internal/tools calls the sink for exactly two tool names.
// A file a shell command wrote is not covered and is said so, every time,
// in the reply — a restore that quietly puts back three of the five files
// a turn changed is worse than one that puts back none.
//
// Content-addressed beside the session log rather than inside it. A
// session's whole log is held in memory and replayed to every
// reconnecting client, so a file's bytes in an event would be paid for on
// every reconnect and on every restart; the event names a hash and the
// bytes live in a sidecar directory that goes when the session does.

// maxCheckpointBytes is the largest file a checkpoint copies.
//
// A bound rather than no bound, because the cost is paid on an ordinary
// write with nobody having asked for it. Past it the checkpoint is still
// recorded — the fact that the turn touched the path is worth keeping —
// with too_large set and no copy, and the restore says which files it
// could not put back rather than silently skipping them.
const maxCheckpointBytes = 8 << 20 // 8 MiB

// checkpointDir is where one session's pre-images live.
func checkpointDir(sessionsDir, sessionID string) string {
	return filepath.Join(sessionsDir, sessionID+".checkpoints")
}

// beginTurn forgets which paths this session has already checkpointed.
//
// Called when a turn opens. One copy per path per turn is the whole rule:
// a turn that edits the same file five times should rewind to what was
// there before the first edit, not before the fifth, and taking a copy
// every time would both cost five reads and record the wrong content.
func (l *Loop) beginTurn(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.turnCheckpoints == nil {
		l.turnCheckpoints = map[string]map[string]bool{}
	}
	l.turnCheckpoints[sessionID] = map[string]bool{}
}

// firstWriteThisTurn reports whether path still needs a copy taken, and
// marks it taken.
func (l *Loop) firstWriteThisTurn(sessionID, path string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	seen := l.turnCheckpoints[sessionID]
	if seen == nil {
		// No turn has opened for this session in this process. A tool
		// running outside a turn has nothing to rewind to, so nothing is
		// recorded rather than a checkpoint nobody can use.
		return false
	}
	if seen[path] {
		return false
	}
	seen[path] = true
	return true
}

// CheckpointWrite is Registry.BeforeWrite: copy what is there now.
//
// Exported because it is wired from cmd/localcode, which is the only
// place that knows a daemon is being built rather than a one-shot run.
//
// Everything it declines to do, it declines quietly and on purpose. This
// runs inside an ordinary write, and a checkpoint that fails must never
// be the reason a write fails — the worst case is that "/rewind" has one
// fewer file to put back, and it says so.
func (l *Loop) CheckpointWrite(ctx context.Context, tool, path string) {
	sessionID, ok := SessionIDFromContext(ctx)
	if !ok || sessionID == "" {
		return
	}
	dir := l.checkpointRoot()
	if dir == "" {
		// An in-memory store: a one-shot run, or a test. Nothing outlives
		// this process, so there is nothing to rewind from.
		return
	}
	if !l.firstWriteThisTurn(sessionID, path) {
		return
	}

	data := map[string]any{"tool": tool, "path": path, "existed": true}

	// Lstat, not Stat: a symlink is the thing at this path, and restoring
	// through one would write the target instead — which is a file the
	// turn may never have touched. Recorded and skipped, the way Claude
	// Code's own restore skips them, rather than silently followed.
	info, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		// The turn is creating this file. Restoring it means removing it,
		// which is why "existed" is recorded rather than inferred from
		// the absence of a hash: a hash can also be absent because the
		// file was too large.
		data["existed"] = false
	case err != nil:
		return
	case info.Mode()&fs.ModeSymlink != 0:
		data["symlink"] = true
	case !info.Mode().IsRegular():
		// A device, a socket, a fifo. Nothing to copy and nothing that
		// should be written back.
		return
	case info.Size() > maxCheckpointBytes:
		data["too_large"] = true
		data["mode"] = uint32(info.Mode().Perm())
	default:
		blob, err := os.ReadFile(path)
		if err != nil {
			return
		}
		sum := sha256.Sum256(blob)
		hash := hex.EncodeToString(sum[:])
		if err := writeBlob(checkpointDir(dir, sessionID), hash, blob); err != nil {
			return
		}
		data["sha256"] = hash
		data["mode"] = uint32(info.Mode().Perm())
	}

	l.Store.Append(sessionID, events.TypeCheckpoint, data)
}

// checkpointRoot is the session directory, or "" for a store that keeps
// nothing on disk.
func (l *Loop) checkpointRoot() string {
	if l.Store == nil {
		return ""
	}
	return l.Store.Dir()
}

// writeBlob stores one pre-image under its own hash.
//
// Content-addressed, so a turn that rewrites the same file in five
// sessions stores one copy, and a write that restores a file to what it
// was costs nothing new. Written to a temporary name and renamed, because
// a half-written pre-image that hashes to a name claiming to be complete
// is the one failure that would be found only at restore.
func writeBlob(dir, hash string, blob []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	final := filepath.Join(dir, hash)
	if _, err := os.Stat(final); err == nil {
		return nil
	}
	tmp, err := os.CreateTemp(dir, hash+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, final); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// readBlob reads a pre-image back.
func readBlob(dir, sessionID, hash string) ([]byte, error) {
	if hash == "" {
		return nil, fmt.Errorf("no content recorded")
	}
	return os.ReadFile(filepath.Join(checkpointDir(dir, sessionID), hash))
}
