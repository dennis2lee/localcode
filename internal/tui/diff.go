package tui

import (
	"encoding/json"
	"fmt"
	"strings"
)

// A diff for edit and write_file tool results, for the terminal.
//
// The Web UI draws the same change as a collapsible card (see
// internal/daemon/static/js/diff.js); the terminal has no collapsible
// anything, so "collapsed" here means capped: the first toolDiffMaxLines
// rows and a count of what did not fit, so a fifty-line change does not
// push the conversation off the screen.
//
// The rows are plain text with "- "/"+" markers, stored exactly so in
// the transcript entry. No escape sequences reach the stored text —
// renderTranscript styles the entry at display time, and
// TestStylingNeverReachesTheStoredTranscript pins that rule for model
// output; the diff rows below obey it the same way.

// toolDiffMaxLines is how many diff rows one tool call may add to the
// transcript. Twenty is enough to read a real edit at a glance; past it
// the count line says how much is behind the cut.
const toolDiffMaxLines = 20

// pendingToolCall is what a tool.start leaves behind so its tool.end
// can say what changed. The end event carries no name — only the id,
// the content and the input — so without this the end handler could not
// tell an edit from a bash call.
type pendingToolCall struct {
	name  string
	input string
}

// toolDiffLine is one rendered row: removed, added, or shared context.
type toolDiffLine struct {
	kind string // "del" | "add" | "ctx"
	text string
}

// diffToolLines reduces two texts to removed/added rows with their
// shared head and tail kept as one line of context each. Only the
// common prefix and suffix are trimmed and the middle is one removed
// block followed by one added block, rather than a full LCS: an edit's
// old_string/new_string are the changed region itself, so the middle IS
// the change, and the linear scan stays honest on inputs where a
// quadratic diff would not. Mirrors diffLines in static/js/diff.js.
func diffToolLines(oldText, newText string) []toolDiffLine {
	oldL := strings.Split(oldText, "\n")
	newL := strings.Split(newText, "\n")
	head := 0
	for head < len(oldL) && head < len(newL) && oldL[head] == newL[head] {
		head++
	}
	tail := 0
	for tail < len(oldL)-head && tail < len(newL)-head &&
		oldL[len(oldL)-1-tail] == newL[len(newL)-1-tail] {
		tail++
	}
	var out []toolDiffLine
	if head > 0 {
		out = append(out, toolDiffLine{kind: "ctx", text: oldL[head-1]})
	}
	for _, t := range oldL[head : len(oldL)-tail] {
		out = append(out, toolDiffLine{kind: "del", text: t})
	}
	for _, t := range newL[head : len(newL)-tail] {
		out = append(out, toolDiffLine{kind: "add", text: t})
	}
	if tail > 0 {
		out = append(out, toolDiffLine{kind: "ctx", text: oldL[len(oldL)-tail]})
	}
	// All shared: the tool reported an edit that changed nothing.
	// No rows, so the caller adds no entry.
	changed := false
	for _, r := range out {
		if r.kind != "ctx" {
			changed = true
			break
		}
	}
	if !changed {
		return nil
	}
	return out
}

// editToolDiff decides whether a finished tool call gets a diff, and
// what it shows. name and inputJSON identify the call, result is the
// result text, read only for the created/replaced distinction write.go
// draws. A replaced file cannot be shown: the result carries counts
// ("it had 340 line(s), it now has 12") and the input carries only the
// new text, so the before half is nowhere the client can reach without
// a wire change — and that wire change belongs in its own task, not
// here. Mirrors editDiffForTool in static/js/diff.js.
func editToolDiff(name, inputJSON, result string) []toolDiffLine {
	// Pointers rather than strings: presence matters apart from value.
	// An edit of "" to "" is a no-change the trim above already
	// answers, but a missing key means this was not an edit-shaped
	// call at all, and a plain string decode mistakes one for the
	// other.
	var input struct {
		OldString *string `json:"old_string"`
		NewString *string `json:"new_string"`
		Content   *string `json:"content"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return nil
	}
	switch name {
	case "edit":
		if input.OldString == nil || input.NewString == nil {
			return nil
		}
		return diffToolLines(*input.OldString, *input.NewString)
	case "write_file":
		if input.Content == nil {
			return nil
		}
		if !strings.HasPrefix(result, "created ") {
			return nil
		}
		lines := strings.Split(*input.Content, "\n")
		out := make([]toolDiffLine, 0, len(lines))
		for _, t := range lines {
			out = append(out, toolDiffLine{kind: "add", text: t})
		}
		return out
	default:
		return nil
	}
}

// renderToolDiff lays rows out as stored transcript text: "- " before a
// removed line, "+ " before an added one, two spaces before context.
// Capped at toolDiffMaxLines rows with a count of what did not fit, the
// terminal's version of the Web UI card starting collapsed.
func renderToolDiff(rows []toolDiffLine) string {
	shown := rows
	more := 0
	if len(rows) > toolDiffMaxLines {
		shown = rows[:toolDiffMaxLines]
		more = len(rows) - toolDiffMaxLines
	}
	out := make([]string, 0, len(shown)+1)
	for _, r := range shown {
		switch r.kind {
		case "del":
			out = append(out, "- "+r.text)
		case "add":
			out = append(out, "+ "+r.text)
		default:
			out = append(out, "  "+r.text)
		}
	}
	if more > 0 {
		out = append(out, fmt.Sprintf("… (%d more diff lines, not shown)", more))
	}
	return strings.Join(out, "\n")
}
