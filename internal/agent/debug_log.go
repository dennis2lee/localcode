package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"localcode/internal/debuglog"
	"localcode/internal/events"
)

// "/debug-log": every byte between localcode and the model, in a file.
//
// The gap it fills is narrow and was hit repeatedly. The turn log says a
// call happened and what it cost. "/context" says what a request was
// going to contain. Neither shows the request, and the questions that
// have needed answering here — is the tool schema what I think it is,
// did the reasoning line actually go out, what exactly did the server
// send back before it stopped — are all questions about bytes.
//
// Runtime only, and off at every start. The file holds the whole
// conversation: the system prompt, the project's rules, every file the
// model read. That belongs to a question somebody is asking now, not to
// a setting that quietly keeps writing into every workspace they open
// for the rest of the month.

const debugLogToolTip = "Every request to a model and every response, byte for byte, one file per prompt. " +
	"Credentials are redacted; nothing else is."

// openDebugLog opens this prompt's file and puts it on ctx, or returns
// ctx unchanged when logging is off.
//
// A sink already on the context is kept rather than replaced: that is a
// delegated turn inside somebody's prompt, and its calls belong in that
// prompt's file.
func (l *Loop) openDebugLog(ctx context.Context, sessionID string) (context.Context, func()) {
	if !l.DebugLogEnabled() || debuglog.From(ctx) != nil {
		return ctx, func() {}
	}
	dir := l.SessionDir(sessionID)
	sink, err := debuglog.Create(dir, sessionID, time.Now())
	if err != nil {
		// Said in the transcript rather than swallowed: somebody who
		// turned this on is owed the reason nothing appeared.
		l.Store.Append(sessionID, events.TypeError, map[string]any{
			"error":     "debug log: could not open a file in " + dir + ": " + err.Error(),
			"recovered": true,
		})
		return ctx, func() {}
	}
	return debuglog.With(ctx, sink), func() { _ = sink.Close() }
}

// routeDebugLog answers "/debug-log", which toggles.
func (l *Loop) routeDebugLog(sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/debug-log")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	want, valid := toggleArg(arg, l.DebugLogEnabled())
	if !valid {
		return true, l.replyText(sessionID, "usage: /debug-log [on|off]")
	}
	l.SetDebugLogEnabled(want)
	l.announceSettings()

	var b strings.Builder
	fmt.Fprintf(&b, "debug_log: %s", onOff(want))
	if !want {
		b.WriteString("\nThe files already written stay where they are.")
		return true, l.replyText(sessionID, b.String())
	}
	dir := l.SessionDir(sessionID)
	fmt.Fprintf(&b, "\n%s\nOne file per prompt, in %s, named for the moment you pressed enter: "+
		"localcode-debug-<date>-<time>.log", debugLogToolTip, dir)
	b.WriteString("\nThis prompt is not in one — the file opens when the next prompt starts.")
	// The whole conversation ends up in these files, which is the point
	// and is also worth saying once, here, rather than in a note nobody
	// reads afterwards.
	b.WriteString("\nThe file holds the whole conversation: the system prompt, this project's rules, " +
		"and every file the model read. Read one before you share it.")
	b.WriteString("\nThis run only. It is off again the next time localcode starts.")
	return true, l.replyText(sessionID, b.String())
}
