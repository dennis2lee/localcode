package agent

import (
	"fmt"
	"strings"

	"localcode/internal/events"
)

// "/clear": the model starts again, the conversation does not go anywhere.
//
// The distinction this command exists to hold is the one this project has
// already had to write down once: the context window is bounded for the
// model's sake, and a session's own record must survive until the session
// is deleted. So nothing is removed. A barrier is appended, the in-memory
// history is emptied, and every message above it stays exactly where it
// was, still scrollable, still in the log.
//
// Claude Code's "/clear" starts a fresh session and leaves the old one to
// be resumed by id — which is why its rewind menu has a "previous
// session" entry, a feature that exists to undo what /clear did. Here the
// sessions are already first-class and listed, and the transcript is the
// record, so a barrier in place gives both halves at once and creates
// nothing to recover from.

// routeClear answers "/clear".
func (l *Loop) routeClear(sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/clear")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
	if arg != "" {
		// It takes no argument, and a mistyped one must not be read as
		// consent to clear anyway: somebody typing "/clear history" means
		// something, and guessing which half they meant is worse than
		// asking again.
		return true, l.replyText(sessionID,
			"usage: /clear\n"+
				"It takes no argument. The conversation stays on screen and in the log; "+
				"what it resets is what the model is sent.")
	}

	// Counted before the barrier goes in, because that is the number worth
	// saying: "cleared" on its own gives no sense of what was let go of.
	before := len(l.history(sessionID))

	l.Store.Append(sessionID, events.TypeCleared, nil)
	l.setHistory(sessionID, nil)
	// The context gauge is a reading of the history that no longer exists.
	// Left alone it would go on reporting the old fill until the next turn
	// replaced it, which is the one moment somebody is looking at it.
	l.clearUsage(sessionID)

	var b strings.Builder
	b.WriteString("Cleared. The model starts the next message with no history.\n")
	if before > 0 {
		fmt.Fprintf(&b, "%d message(s) were released from its context.\n", before)
	}
	// The half people do not expect, said every time rather than
	// discovered: this is not a delete.
	b.WriteString("Everything above stays in this conversation and in its log — " +
		"scroll up, or reopen it later, and it is all still there. " +
		"Cumulative token usage is unchanged for the same reason: those tokens were spent.")
	return true, l.replyText(sessionID, b.String())
}
