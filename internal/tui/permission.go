package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type pendingPermission struct {
	id, tool, description string
	// rule is the pattern a "session" or "always" answer would grant
	// (e.g. "git *" for a bash call, or the exact path for a file tool) —
	// shown in the prompt so approving a wider scope is an informed
	// choice, not a guess.
	rule string
	// canAlways is false when the daemon has no config.json path to write
	// to (started with neither --config nor a resolvable global config),
	// in which case "always" isn't offered — only once/session/deny.
	canAlways bool
	// once is a question whose answer covers this use only (a resume of
	// a run that writes): only y and n are offered or taken.
	once bool
	// long is a question whose text does not fit the one line the footer
	// gives it. Its whole text is in the transcript, which scrolls and
	// wraps, and the footer says so. See longPermission.
	long bool

	// outside is "read" or "write" when this question exists because the
	// path leaves the workspace, and empty otherwise. It changes both
	// halves of the prompt: what it says, and which answers it takes.
	//
	// A boundary question is about a place, not about a tool call, and a
	// place is answered at one of two sizes. Offering only "once" and
	// "for the session" would make the useful answer impossible to give:
	// a model told to read the sibling repository reads forty files in
	// it, and forty prompts is one decision and thirty-nine keystrokes.
	outside string
	// outsideDir is the directory "d" approves; workspace is the project
	// the path is outside of, which is the half that makes the question
	// legible at all.
	outsideDir string
	workspace  string
}

// parts is the modal in its two halves: what is asked, and how to answer.
// The keys are the last rows, always, with the line about typing above
// them, so a terminal too short for all of it can cut the question from
// its top and keep what answers it. See Model.fitQuestion.
func (p pendingPermission) parts(typing bool) (string, string) {
	tool := expandTabs(p.tool)
	head := fmt.Sprintf("Permission request [%s]: %s", tool, expandTabs(p.description))
	if p.long {
		// One line for the head, so where the rest is stays its second.
		tool, _, _ = strings.Cut(tool, "\n")
		head = fmt.Sprintf("Permission request [%s]: %s …\n(the whole request is in the transcript above)",
			tool, footerLine(tool, p.description))
	}
	keys := "y: allow once  n: deny  s: allow for session"
	if p.once {
		keys = "y: allow this once  n: deny  (asked every time)"
	} else if p.canAlways {
		// Quoted, not %q: the rule is already drawn safe (events.VisibleText),
		// and %q would escape that escaping a second time.
		if p.rule == "" {
			keys += "  a: always allow"
		} else {
			keys += fmt.Sprintf("  a: always allow \"%s\"", ruleHint(p.rule))
		}
	}

	if p.outside != "" {
		// Why, before what to press. Without this line the request reads
		// as an ordinary write and there is nothing on screen saying the
		// path is in another project.
		head += fmt.Sprintf("\noutside this session's workspace (%s)", escapeBreaks(p.workspace))
		// "y" and "n" last: a short terminal cuts from the top, and those
		// are the answers every question takes.
		keys = fmt.Sprintf("d: allow %s under %s for this session\ns: allow %s anywhere outside the workspace for this session\ny: allow once  n: deny",
			p.outside, escapeBreaks(p.outsideDir), p.outside)
	}

	// Said plainly, because otherwise the keys look broken: pressing y
	// with a half-written message in the box types a y, and the reason
	// is not visible anywhere on screen. Above the keys rather than under
	// them, so the keys are the last rows.
	if typing {
		keys = "(clear the prompt box to answer — those are ordinary letters while you are writing)\n" + keys
	}
	return head, keys
}

// dropHeldAsk lets go of a model question still held when a turn ends.
// The daemon resolves a question before the turn that asked it can end, so
// one still held was asked by a daemon that has gone, and the next Enter
// would be sent as its answer and refused. Not a permission request: one
// mirrored from a background task outlives the turn of the conversation
// it is shown in.
func (m *Model) dropHeldAsk() {
	m.asking = nil
}

// staleQuestion reports whether the daemon marked a replayed
// permission.request or input.request as one it is not waiting on: a
// question left open by a daemon that has since stopped, or copied into
// a fork. Nothing will resolve it, and an answer gets the daemon's 404,
// so it is not shown. See markStaleQuestions in internal/daemon.
func staleQuestion(data map[string]any) bool {
	stale, _ := data["stale"].(bool)
	return stale
}

// escapeBreaks draws a field the modal shows inside a line of its own words:
// a line break or a tab in it as its escape.
func escapeBreaks(s string) string {
	return strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(s)
}

// longPermission reports whether a question needs more than the one line
// the footer gives it: a line break, or a head line wider than footerCols
// columns as a terminal draws it, tabs and wide characters counted.
//
// The footer is not where such a question can be read. The layout keeps a
// line or two for it, a line wider than the terminal is cut at its edge,
// and the keys to answer it are under it. So the whole of it is written
// into the transcript, where it scrolls and wraps, before it can be
// answered. Measured in columns, not characters: a prompt of Korean, or
// one padded with tabs, is twice or eight times as wide as its count.
func longPermission(tool, description string) bool {
	head := fmt.Sprintf("Permission request [%s]: %s", tool, description)
	if strings.Contains(head, "\n") {
		return true
	}
	return lipgloss.Width(expandTabs(head)) > footerCols
}

// footerCols is the width a short question is held to. Eighty, the width a
// terminal starts at; the footer wraps to the real width as well, so a
// narrower one wraps a short question rather than cutting it.
const footerCols = 80

// footerLine is the first line of a long question, cut to the columns that
// leave its head, tool name and " …" included, inside footerCols. Ten at
// the least, for a tool name that takes the rest.
func footerLine(tool, description string) string {
	line, _, _ := strings.Cut(description, "\n")
	budget := footerCols - lipgloss.Width("Permission request []: ") - lipgloss.Width(tool) - lipgloss.Width(" …")
	if budget < 10 {
		budget = 10
	}
	return ansi.Truncate(expandTabs(line), budget, "")
}

// ruleHint is the rule "always allow" would write, on one line: a line
// break or a tab in it shown as its escape, and past 120 characters cut
// with a count of the rest. An exact shell rule is the whole command,
// which the question above already shows in full, and a command of twenty
// lines drawn as twenty rows pushed the keys off the screen.
func ruleHint(rule string) string {
	r := []rune(escapeBreaks(rule))
	if len(r) <= 120 {
		return string(r)
	}
	cut := escapeSafeCut(r, 120)
	return string(r[:cut]) + fmt.Sprintf("… (%d more characters)", len(r)-cut)
}

// escapeSafeCut moves a cut at n back to before an escape it would split:
// "\x1b", "\u202e", "\U000e0041", "\r" and the rest. The text has been
// through events.VisibleText and escapeBreaks, so a backslash starts an escape.
func escapeSafeCut(r []rune, n int) int {
	for i := n - 1; i >= 0 && i >= n-10; i-- {
		if r[i] != '\\' {
			continue
		}
		size := 2
		if i+1 < len(r) {
			switch r[i+1] {
			case 'x':
				size = 4
			case 'u':
				size = 6
			case 'U':
				size = 10
			}
		}
		if i+size > n {
			return i
		}
		return n
	}
	return n
}

// expandTabs draws a tab as four spaces, so a width is the width shown.
func expandTabs(s string) string { return strings.ReplaceAll(s, "\t", "    ") }

// enqueuePermission shows a request, or queues it behind the one on
// screen. The screen always holds the oldest unresolved request: a newer
// arrival must not cover an older question whose turn is still blocked
// on it, or answering the second would strand the first with no way to
// reach it again.
func (m *Model) enqueuePermission(p *pendingPermission) {
	if m.pending == nil {
		m.pending = p
		m.pendingHintShown = false
		m.pendingSince = time.Now()
		return
	}
	if m.pending.id == p.id {
		m.pending = p
		return
	}
	for i, q := range m.pendingQueue {
		if q.id == p.id {
			m.pendingQueue[i] = p
			return
		}
	}
	m.pendingQueue = append(m.pendingQueue, p)
}

// settlePermission drops the request with this id wherever it is. A
// resolution for the request on screen promotes the oldest queued one;
// a resolution for a queued request just removes it. That second half
// is what keeps replay honest: both halves of every permission live in
// the log, so reopening a session replays each old resolution in turn,
// and without it every request answered days ago would pile up as a
// modal behind the live one.
//
// A promoted request gets a fresh pendingSince, so the arm delay applies
// to it as its own modal: a keypress already travelling when the first
// answer went out must not land on the question that appears in its
// place.
func (m *Model) settlePermission(id string) {
	if m.pending != nil && m.pending.id == id {
		if m.answering == nil {
			m.answering = map[string]*pendingPermission{}
		}
		m.answering[id] = m.pending
		m.pendingHintShown = false
		if len(m.pendingQueue) == 0 {
			m.pending = nil
			return
		}
		m.pending = m.pendingQueue[0]
		m.pendingQueue = m.pendingQueue[1:]
		m.pendingSince = time.Now()
		return
	}
	for i, q := range m.pendingQueue {
		if q.id == id {
			m.pendingQueue = append(m.pendingQueue[:i], m.pendingQueue[i+1:]...)
			return
		}
	}
}

// resolvePermissionEvent settles a request from a daemon permission.resolved
// event and drops any in-flight answer for that id so a later failed POST
// does not bring the modal back.
func (m *Model) resolvePermissionEvent(id string) {
	m.settlePermission(id)
	delete(m.answering, id)
	// Answered, so a later request under the same id is a new question,
	// and its text is written into the transcript again. Ids were numbered
	// per daemon run before they carried a prefix, so a replayed log can
	// reuse one.
	delete(m.longShown, id)
}

// restorePermission puts an answered request back through the queue when its
// resolve POST fails, unless a permission.resolved event already cleared it.
func (m *Model) restorePermission(id string) {
	p, ok := m.answering[id]
	if !ok {
		return
	}
	delete(m.answering, id)
	m.enqueuePermission(p)
}

// permissionArmDelay is how long after a request appears before a single
// letter can answer it.
//
// It exists for the keystroke already travelling when the modal arrives:
// nothing interrupts the user, the textarea keeps focus, and the request
// simply appears below the prompt box. Without a pause, the very next
// character typed is read as an answer to a question that was not on
// screen when the finger started moving.
const permissionArmDelay = 750 * time.Millisecond

// canAnswerPermission reports whether a bare "y"/"n"/"s"/"a"/"d" should
// be taken as an answer rather than as a character to type.
//
// Two conditions, and both exist because the alternative was silently
// approving a command the user never read. "y" is an ordinary letter:
// typing "yes, use the second approach" while a turn runs used to have
// its first "y" intercepted and sent as allow — and "s" granted the tool
// for the whole session, "a" wrote a permanent rule into config.json,
// with no confirmation and nothing to undo it with.
//
//   - The prompt box must be empty. Text in it means the user is writing
//     a message, so the letter belongs to that message.
//   - The request must have been on screen for permissionArmDelay, so a
//     keypress already in flight cannot land on a modal that appeared
//     between the keydown and its delivery.
//
// Neither costs anything in the ordinary case, which is a user waiting on
// the model with an empty box.
func (m Model) canAnswerPermission() bool {
	if strings.TrimSpace(m.input.Value()) != "" {
		return false
	}
	return time.Since(m.pendingSince) >= permissionArmDelay
}
