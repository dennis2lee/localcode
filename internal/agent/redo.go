package agent

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"localcode/internal/events"
)

// Putting back a turn that "/rewind" undid.
//
// It is the half of opencode's "/redo" this project could not offer for a
// while, and the reason is worth stating: rewinding keeps the pre-images
// as a matter of course, so undoing is always possible, and nothing kept
// the post-images, so redoing was not. The fix is not a new store but a
// moment — during a rewind, just before each pre-image goes back, the
// turn's own result is still on disk. That is when it is copied. See
// keepPostImage.
//
// The conversation half works the way the rewind half does: nothing is
// edited, a marker is appended naming the rewind it cancels, and
// applyRewinds reads both. A rewind that has been redone stops filtering.

// routeRedo answers "/redo".
func (l *Loop) routeRedo(ctx context.Context, sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/redo")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
	if arg != "" {
		return true, l.replyText(sessionID,
			"usage: /redo\nIt takes no argument and puts back the turn /rewind just undid.")
	}
	// The same two refusals /rewind carries, for the same reasons: it
	// writes to the working tree, and a sub-agent still running was
	// launched by a turn whose files are about to move under it.
	if Unattended(ctx) {
		return true, l.replyText(sessionID,
			"/redo writes files, so it only runs in a conversation somebody is having — "+
				"not in a scheduled run or a one-shot.")
	}
	if l.Tasks != nil && len(l.Tasks.RunningIn(childIDs(l.Store.Children(sessionID)))) > 0 {
		return true, l.replyText(sessionID,
			"a background sub-agent from this conversation is still working. Wait for it, or stop it, then try again.")
	}

	evs, err := l.Store.Events(sessionID, 0)
	if err != nil {
		return true, l.replyText(sessionID, "could not read this conversation: "+err.Error())
	}
	marker, ok := redoableRewind(evs)
	if !ok {
		return true, l.replyText(sessionID,
			"there is nothing to put back. /redo undoes a /rewind, and only while the rewind is still "+
				"the last thing that happened — once the conversation has moved on, the turn it undid is gone for good.")
	}

	written, skipped := l.replayPostImages(sessionID, marker)
	l.Store.Append(sessionID, events.TypeRedone, map[string]any{
		"rewind_seq": marker.Seq,
		"turn_text":  dataString(marker.Data, "turn_text"),
		"written":    len(written),
		"skipped":    len(skipped),
	})

	// Re-read, so the marker just appended is part of what history is
	// rebuilt from — the same reason /rewind re-reads.
	after, err := l.Store.Events(sessionID, 0)
	if err != nil {
		after = evs
	}
	l.setHistory(sessionID, rehydrateHistory(applyRewinds(after)))
	l.clearUsage(sessionID)

	return true, l.replyText(sessionID, redoReport(dataString(marker.Data, "turn_text"), written, skipped))
}

// redoableRewind is the rewind "/redo" would cancel, and whether there is
// one.
//
// The last marker in the log, and only while nothing has happened since:
// a real turn after a rewind is the conversation moving on, and putting
// the undone turn back underneath it would interleave two histories. The
// same rule opencode's has — a new message commits the cleanup — arrived
// at because the alternative is a conversation nobody can read.
//
// A rewind already redone does not count, so "/redo" twice does not put
// the same turn back twice.
func redoableRewind(evs []events.Event) (events.Event, bool) {
	redone := map[uint64]bool{}
	for _, ev := range evs {
		if ev.Type == events.TypeRedone {
			redone[dataUint(ev.Data, "rewind_seq")] = true
		}
	}
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		switch ev.Type {
		case events.TypeRewound:
			if redone[ev.Seq] {
				return events.Event{}, false
			}
			return ev, true
		case events.TypeUserMessage:
			// A command answered locally is not the conversation moving
			// on — "/usage" between a rewind and a redo is a question,
			// not a turn. Anything else is.
			if isTrue(ev.Data["local"]) {
				continue
			}
			return events.Event{}, false
		case events.TypeCompacted, events.TypeCleared, events.TypeRedone:
			// A barrier, or a redo that already happened with no rewind
			// of its own before it.
			return events.Event{}, false
		}
	}
	return events.Event{}, false
}

// replayPostImages writes back what the rewind took away.
func (l *Loop) replayPostImages(sessionID string, marker events.Event) (written, skipped []string) {
	entries, _ := marker.Data["redo"].([]any)
	if entries == nil {
		// Written in this process rather than read back from JSON.
		if typed, ok := marker.Data["redo"].([]map[string]any); ok {
			for _, e := range typed {
				entries = append(entries, any(e))
			}
		}
	}
	dir := l.checkpointRoot()
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path := dataString(entry, "path")
		if path == "" {
			continue
		}
		blob, err := readBlob(dir, sessionID, dataString(entry, "sha256"))
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (its saved copy is gone)", path))
			continue
		}
		mode := fs.FileMode(0o644)
		if m := dataInt(entry, "mode"); m > 0 {
			mode = fs.FileMode(m)
		}
		// A file the turn created was removed by the rewind, so its
		// directory may have gone with it if something else cleaned up.
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", path, err))
			continue
		}
		if err := os.WriteFile(path, blob, mode); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", path, err))
			continue
		}
		_ = os.Chmod(path, mode)
		written = append(written, path)
	}
	return written, skipped
}

// redoReport is what the person is told.
//
// It names the same limit the rewind report does, from the other side: a
// rewind that could not restore a file did not copy one either, so a redo
// has nothing to put back for it. Saying so beats a count that reads as
// though the tree is where the turn left it.
func redoReport(turn string, written, skipped []string) string {
	var b strings.Builder
	b.WriteString("Put the turn back")
	if turn != "" {
		b.WriteString(": " + turn)
	}
	b.WriteString(".\n")
	if len(written) > 0 {
		fmt.Fprintf(&b, "\nWritten again (%d):\n", len(written))
		for _, p := range written {
			fmt.Fprintf(&b, "  %s\n", p)
		}
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&b, "\nLeft alone (%d):\n", len(skipped))
		for _, p := range skipped {
			fmt.Fprintf(&b, "  %s\n", p)
		}
	}
	if len(written) == 0 && len(skipped) == 0 {
		b.WriteString("\nNo files: the turn changed none that were tracked.")
	}
	b.WriteString("\nOnly what /rewind was able to copy comes back. Anything a shell command wrote, " +
		"anything too large to keep, and anything behind a symlink was never copied either way.")
	return b.String()
}
