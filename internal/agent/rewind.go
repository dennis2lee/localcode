package agent

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"localcode/internal/events"
	"localcode/internal/session"
)

// Undoing the last turn.
//
// Two halves that have to agree: what the model remembers, and what is on
// disk. This file owns the first and the arithmetic both share; the files
// are in checkpoint.go.
//
// The log is not edited. A rewind appends a marker naming the sequence it
// undid back to, and every reader that rebuilds history filters the log
// through applyRewinds before replaying it. That is the same shape
// compaction already uses — the record is what happened, and what the
// model is sent is a reading of it — and it is what keeps a rewind
// surviving a restart without a truncate API existing anywhere.

// applyRewinds returns evs with the events of every undone turn removed.
//
// One pass, oldest marker first, each dropping the half-open range
// [from_seq, marker) — the events from the user message that opened the
// turn up to but not including the marker itself. Sequences are absolute
// and are never reused, so markers compose: three rewinds in a row name
// three earlier anchors and each range is still exactly the turn it
// undid.
//
// Filtering before the replay rather than teaching the replay about
// rewinds is the whole design. rehydrateHistory accumulates a turn across
// many events — text deltas, tool.start/tool.end pairs, injected messages
// riding out with the tool results — and it mutates the message it
// already emitted as later events arrive. Unwinding that mid-loop would
// mean reversing every accumulator; removing a contiguous run of events
// before it starts means it never sees the turn at all, and needs no
// knowledge that rewinding exists.
//
// The marker itself is kept. It is a record, it is what the transcript
// renders, and it is what the next rewind reads to know where the
// previous one stopped.
func applyRewinds(evs []events.Event) []events.Event {
	// Nothing to do is the overwhelmingly common case, and it should not
	// cost an allocation on every restart of every session.
	any := false
	for _, ev := range evs {
		if ev.Type == events.TypeRewound {
			any = true
			break
		}
	}
	if !any {
		return evs
	}

	dropped := make(map[uint64]bool)
	for _, ev := range evs {
		if ev.Type != events.TypeRewound {
			continue
		}
		from := dataUint(ev.Data, "from_seq")
		if from == 0 {
			// A marker with no anchor cannot say what it undid. Skipped
			// rather than guessed at: dropping "everything before it"
			// would silently erase a conversation on a malformed line.
			continue
		}
		for _, e := range evs {
			if e.Seq >= from && e.Seq < ev.Seq {
				dropped[e.Seq] = true
			}
		}
	}

	out := make([]events.Event, 0, len(evs))
	for _, ev := range evs {
		if !dropped[ev.Seq] {
			out = append(out, ev)
		}
	}
	return out
}

// lastTurnStart is the sequence of the user message that opened the most
// recent turn, and whether there is one to undo.
//
// evs must already be filtered by applyRewinds, so "most recent" means
// most recent that still counts — which is what makes /rewind twice walk
// back two turns rather than undoing the same one again.
//
// Three kinds of user message are not a turn opening and are skipped:
//
//   - "local": a command answered by the daemon with no model call.
//     /usage, /config, /rewind itself. Undoing back to one of these would
//     undo nothing and consume the real turn behind it.
//   - "injected": typed while a turn was running. It went to the model as
//     trailing text inside that turn's tool_result message, so it is part
//     of the turn already under way, not the start of a new one.
//     Anchoring on it cuts a turn in half and leaves a tool_use with no
//     tool_result, a shape some providers reject outright.
//   - "auto": localcode's own nudge rather than something a person typed.
//
// A barrier at or after the anchor stops it. Rewinding across a
// compaction or a clear would ask replay to rebuild a turn whose earlier
// half the barrier has already discarded, and the honest answer is that
// there is nothing to go back to.
func lastTurnStart(evs []events.Event) (uint64, bool) {
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		switch ev.Type {
		case events.TypeCompacted:
			if dataString(ev.Data, "summary") != "" {
				return 0, false
			}
		case events.TypeCleared:
			return 0, false
		case events.TypeUserMessage:
			if isTrue(ev.Data["local"]) || isTrue(ev.Data["injected"]) || isTrue(ev.Data["auto"]) {
				continue
			}
			return ev.Seq, true
		}
	}
	return 0, false
}

// turnEvents are the events one turn produced: everything from its
// opening user message to the end of the log.
//
// "To the end" rather than "to the turn's last event" because /rewind
// undoes the most recent turn and refuses while one is in flight, so
// there is nothing after it that belongs to anybody else. Written as a
// range so the checkpoint half and the history half read the same set.
func turnEvents(evs []events.Event, from uint64) []events.Event {
	var out []events.Event
	for _, ev := range evs {
		if ev.Seq >= from {
			out = append(out, ev)
		}
	}
	return out
}

// dataUint reads a numeric field back out of an event.
//
// Its own function because a sequence survives a round trip through JSON
// as a float64, and reading it as anything else silently yields zero —
// which applyRewinds would then treat as a marker with no anchor.
func dataUint(data map[string]any, key string) uint64 {
	switch v := data[key].(type) {
	case uint64:
		return v
	case int:
		if v > 0 {
			return uint64(v)
		}
	case int64:
		if v > 0 {
			return uint64(v)
		}
	case float64:
		if v > 0 {
			return uint64(v)
		}
	}
	return 0
}

// routeRewind answers "/rewind": undo the last turn, conversation and
// files together.
//
// One turn, by design. A picker would need a list, a selection channel
// between the daemon and two clients, and a way to name a turn that is
// not its first line — and repeating "/rewind" already walks back, since
// each one anchors on what the previous one left.
func (l *Loop) routeRewind(ctx context.Context, sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/rewind")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
	if arg != "" {
		return true, l.replyText(sessionID,
			"usage: /rewind\n"+
				"It takes no argument and undoes the last turn. Run it again to go back another.")
	}
	// Nobody is watching, and this one writes to the working tree. A
	// scheduled run has no turn of its own to undo and inherits the
	// conversation's project, so the files it would restore are somebody
	// else's, at a moment nobody chose.
	if Unattended(ctx) {
		return true, l.replyText(sessionID,
			"/rewind restores files, so it only runs in a conversation somebody is having — "+
				"not in a scheduled run or a one-shot.")
	}
	if l.Tasks != nil && len(l.Tasks.RunningIn(childIDs(l.Store.Children(sessionID)))) > 0 {
		return true, l.replyText(sessionID,
			"a background sub-agent from this conversation is still working. "+
				"Undoing the turn that launched it would leave it running against files it no longer agrees with. "+
				"Wait for it, or stop it, then try again.")
	}

	evs, err := l.Store.Events(sessionID, 0)
	if err != nil {
		return true, l.replyText(sessionID, fmt.Sprintf("could not read this conversation's log: %v", err))
	}
	filtered := applyRewinds(evs)
	from, ok := lastTurnStart(filtered)
	if !ok {
		return true, l.replyText(sessionID,
			"nothing to undo: there is no earlier turn in this conversation that has not already been "+
				"cleared, compacted, or rewound.")
	}

	undone := turnEvents(filtered, from)
	restored, removed, skipped := l.restoreCheckpoints(sessionID, undone)

	first := turnLabel(dataString(eventAt(undone, from).Data, "text"))
	l.Store.Append(sessionID, events.TypeRewound, map[string]any{
		"from_seq": from, "turn_text": first,
		"restored": len(restored), "created": len(removed), "skipped": len(skipped),
	})

	// Re-read, so the marker just appended is part of what history is
	// rebuilt from. Rebuilding from the old slice would leave this
	// process one turn ahead of what a restart would produce.
	after, err := l.Store.Events(sessionID, 0)
	if err != nil {
		after = append(evs, events.Event{Seq: ^uint64(0), Type: events.TypeRewound,
			Data: map[string]any{"from_seq": from}})
	}
	l.setHistory(sessionID, rehydrateHistory(applyRewinds(after)))
	l.clearUsage(sessionID)

	return true, l.replyText(sessionID, rewindReport(first, restored, removed, skipped))
}

// restoreCheckpoints puts back every file the undone turn changed, and
// reports what it did: the paths restored, the paths removed because the
// turn created them, and the paths deliberately left alone.
//
// Newest-first over a set that holds one entry per path per turn, so the
// order is only about determinism. Every skip is counted rather than
// swallowed: a restore that silently left a file changed is the failure
// this whole feature would be judged on.
func (l *Loop) restoreCheckpoints(sessionID string, undone []events.Event) (restored, removed, skipped []string) {
	dir := l.checkpointRoot()
	for _, e := range undone {
		if e.Type != events.TypeCheckpoint {
			continue
		}
		path := dataString(e.Data, "path")
		if path == "" {
			continue
		}
		switch {
		case isTrue(e.Data["symlink"]):
			// Writing through it would change the target, which this turn
			// may never have touched.
			skipped = append(skipped, path+" (a symlink)")
			continue
		case isTrue(e.Data["too_large"]):
			skipped = append(skipped, fmt.Sprintf("%s (larger than %d MiB, no copy was kept)", path, maxCheckpointBytes>>20))
			continue
		}

		if !isTrue(e.Data["existed"]) {
			// The turn created it. Undoing that is a removal, and only if
			// it is still the file the turn left: something else may have
			// been put here since.
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				skipped = append(skipped, fmt.Sprintf("%s (could not remove: %v)", path, err))
				continue
			}
			removed = append(removed, path)
			continue
		}

		blob, err := readBlob(dir, sessionID, dataString(e.Data, "sha256"))
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (its saved copy is gone)", path))
			continue
		}
		mode := fs.FileMode(0o644)
		if m := dataInt(e.Data, "mode"); m > 0 {
			mode = fs.FileMode(m)
		}
		if err := os.WriteFile(path, blob, mode); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (%v)", path, err))
			continue
		}
		// WriteFile does not change an existing file's mode, so a file
		// whose permissions the turn changed needs this to come back.
		_ = os.Chmod(path, mode)
		restored = append(restored, path)
	}
	return restored, removed, skipped
}

// rewindReport is what the person is told.
//
// The last paragraph is the part that must not be dropped: this restores
// what write_file and edit changed, and nothing else. Somebody who reads
// "rewound" and assumes their tree is back where it was has been misled
// by a message that was technically true.
func rewindReport(turn string, restored, removed, skipped []string) string {
	var b strings.Builder
	b.WriteString("Rewound one turn.")
	if turn != "" {
		fmt.Fprintf(&b, " Undone: %q", turn)
	}
	b.WriteString("\nThe model no longer has that exchange; it is still in this conversation and in the log.\n")

	if len(restored) == 0 && len(removed) == 0 && len(skipped) == 0 {
		b.WriteString("\nNo files were changed by that turn through write_file or edit.\n")
	}
	for _, group := range []struct {
		label string
		paths []string
	}{
		{"Restored", restored},
		{"Removed (created by that turn)", removed},
		{"Left alone", skipped},
	} {
		if len(group.paths) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s (%d):\n", group.label, len(group.paths))
		for _, p := range group.paths {
			fmt.Fprintf(&b, "  %s\n", p)
		}
	}

	b.WriteString("\nOnly files changed through write_file and edit are tracked. " +
		"Anything a shell command wrote, moved or deleted, anything a background sub-agent changed, " +
		"and anything you edited yourself is untouched by this. It is not a substitute for version control.")
	return b.String()
}

// eventAt finds one event by sequence, or the zero value.
func eventAt(evs []events.Event, seq uint64) events.Event {
	for _, e := range evs {
		if e.Seq == seq {
			return e
		}
	}
	return events.Event{}
}

// turnLabel is a prompt reduced to something that fits on a line, for
// naming the turn that was undone.
//
// Not turn.go's firstLine, which exists for a different job and is
// allowed to change with it: this one is read by a person deciding
// whether the right turn was undone, so it keeps enough to recognise.
func turnLabel(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if len(line) > 72 {
		return line[:69] + "..."
	}
	return line
}

// childIDs is a session list reduced to ids, for asking the task manager
// which of them are still working.
func childIDs(children []session.Session) []string {
	ids := make([]string, 0, len(children))
	for _, c := range children {
		ids = append(ids, c.ID)
	}
	return ids
}
