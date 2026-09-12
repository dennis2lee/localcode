package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"localcode/internal/events"
)

// "/export": a conversation as a file.
//
// There was no way to get one out. A conversation lived in the event log
// and on two screens, and anybody who wanted it somewhere else — into a
// pull request, into a bug report, into a message to a colleague — took
// a screenshot or scrolled and copied. The log is JSONL and is the wrong
// thing to hand anybody.
//
// Markdown, and written to a file rather than printed. Printing it puts
// a copy of the whole conversation inside the conversation, which is
// both absurd and expensive: the next request would carry it.
//
// Rendered from the log rather than from a client's screen, so the two
// clients cannot produce different files from the same conversation, and
// so an archived conversation exports as readily as an open one.

// exportLimit bounds one exported file. A conversation with a hundred
// large tool results is a file nobody opens twice, and the truncation is
// named in the file rather than left to be discovered at the bottom.
const exportToolLimit = 4000

// routeExport answers "/export" and "/export <path>".
func (l *Loop) routeExport(sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/export")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	evs, err := l.Store.Events(sessionID, 0)
	if err != nil {
		return true, l.replyText(sessionID, "could not read this conversation: "+err.Error())
	}
	sess, err := l.Store.Get(sessionID)
	if err != nil {
		return true, l.replyText(sessionID, "could not read this conversation: "+err.Error())
	}

	path, err := exportPath(l.SessionDir(sessionID), sessionID, arg)
	if err != nil {
		return true, l.replyText(sessionID, "export: "+err.Error())
	}
	body := renderTranscript(sess.Title, sessionID, applyRewinds(evs))
	if err := os.WriteFile(path, []byte(body), logFileModeForExport); err != nil {
		return true, l.replyText(sessionID, "export: "+err.Error())
	}
	return true, l.replyText(sessionID, fmt.Sprintf("Written to %s (%d lines).\n\n"+
		"Rendered from this conversation's log, so it holds what happened rather than what is on screen. "+
		"Tool output longer than %d characters is cut, and the file says where.",
		path, strings.Count(body, "\n")+1, exportToolLimit))
}

// logFileModeForExport is 0600, the mode the session logs use.
//
// The same reasoning: this file is the conversation, in one piece and in
// a directory somebody chose. A default of 0644 would put a readable
// copy of everything in a project directory on a shared machine.
const logFileModeForExport = 0o600

// exportPath resolves where the file goes.
func exportPath(dir, sessionID, arg string) (string, error) {
	if arg == "" {
		short := sessionID
		if len(short) > 8 {
			short = short[len(short)-8:]
		}
		return filepath.Join(dir, fmt.Sprintf("session-%s.md", short)), nil
	}
	p, err := ResolveExportTarget(arg, dir)
	if err != nil {
		return "", err
	}
	return p, nil
}

// ResolveExportTarget turns what somebody typed into a file to write.
//
// A directory means "in there, under the default name", because that is
// what a person means by "/export ~/Desktop". Anything else is taken as
// the file itself.
func ResolveExportTarget(arg, dir string) (string, error) {
	p := strings.TrimSpace(arg)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		return filepath.Join(p, "session.md"), nil
	}
	if parent := filepath.Dir(p); parent != "" {
		if _, err := os.Stat(parent); err != nil {
			return "", fmt.Errorf("%s does not exist", parent)
		}
	}
	return p, nil
}

// renderTranscript is the conversation as Markdown.
func renderTranscript(title, sessionID string, evs []events.Event) string {
	var b strings.Builder
	if title == "" {
		title = "Conversation " + sessionID
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "_localcode conversation `%s`, exported %s._\n",
		sessionID, time.Now().Format(time.RFC3339))

	// Tool calls are matched to their results by id, because they do not
	// arrive adjacent: a turn asking for three tools writes three starts
	// and then three ends.
	pending := map[string]string{}

	for _, ev := range evs {
		switch ev.Type {
		case events.TypeUserMessage:
			text := dataString(ev.Data, "text")
			if text == "" {
				continue
			}
			// A command answered locally, and localcode's own nudges, are
			// part of what happened and are marked as what they are
			// rather than as something the person said.
			switch {
			case isTrue(ev.Data["auto"]):
				fmt.Fprintf(&b, "\n> localcode: %s\n", oneLine(text))
			case isTrue(ev.Data["local"]):
				fmt.Fprintf(&b, "\n> %s\n", oneLine(text))
			default:
				fmt.Fprintf(&b, "\n## %s\n\n%s\n", stamp(ev.Timestamp), text)
			}
		case events.TypeMessagePartEnd:
			if text := dataString(ev.Data, "text"); text != "" {
				fmt.Fprintf(&b, "\n%s\n", text)
			}
		case events.TypeToolStart:
			pending[dataString(ev.Data, "tool_use_id")] = dataString(ev.Data, "name")
		case events.TypeToolEnd:
			id := dataString(ev.Data, "tool_use_id")
			name := pending[id]
			delete(pending, id)
			if name == "" {
				name = "a tool"
			}
			out := cut(dataString(ev.Data, "content"))
			fmt.Fprintf(&b, "\n<details><summary><code>%s</code></summary>\n\n```\n%s\n```\n\n</details>\n", name, out)
		case events.TypeError:
			// Recovered conditions included: a turn that was retried is
			// part of what happened, and a file that hides it reads as a
			// conversation that went smoothly.
			if msg := dataString(ev.Data, "error"); msg != "" {
				fmt.Fprintf(&b, "\n> **%s**\n", oneLine(msg))
			}
		case events.TypeCompacted:
			fmt.Fprintf(&b, "\n---\n\n_Compacted here: the model kept a summary of everything above._\n")
		case events.TypeCleared:
			fmt.Fprintf(&b, "\n---\n\n_Cleared here: the model started fresh. Everything above is still in the conversation._\n")
		case events.TypeRewound:
			fmt.Fprintf(&b, "\n_One turn was undone here._\n")
		case events.TypeTurnCancelled:
			fmt.Fprintf(&b, "\n_Stopped here._\n")
		}
	}
	return b.String()
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "Message"
	}
	return t.Format("2006-01-02 15:04")
}

func oneLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// cut bounds one tool result, and says so where it cut.
func cut(s string) string {
	if len(s) <= exportToolLimit {
		return s
	}
	return s[:exportToolLimit] + fmt.Sprintf("\n... cut here: %d more characters", len(s)-exportToolLimit)
}
