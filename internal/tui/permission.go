package tui

import (
	"fmt"
	"strings"
	"time"
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

// prompt renders the permission modal, listing exactly the answers this
// request will accept.
func (p pendingPermission) prompt(typing bool) string {
	head := fmt.Sprintf("Permission request [%s]: %s", p.tool, p.description)
	keys := "y: allow once  n: deny  s: allow for session"
	if p.canAlways {
		keys += fmt.Sprintf("  a: always allow %q", p.rule)
	}

	if p.outside != "" {
		// Why, before what to press. Without this line the request reads
		// as an ordinary write and there is nothing on screen saying the
		// path is in another project.
		head += fmt.Sprintf("\noutside this session's workspace (%s)", p.workspace)
		keys = fmt.Sprintf("y: allow once  n: deny\nd: allow %s under %s for this session\ns: allow %s anywhere outside the workspace for this session",
			p.outside, p.outsideDir, p.outside)
	}

	// Said plainly, because otherwise the keys look broken: pressing y
	// with a half-written message in the box types a y, and the reason
	// is not visible anywhere on screen.
	if typing {
		keys += "\n(clear the prompt box to answer — those are ordinary letters while you are writing)"
	}
	return head + "\n" + keys
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
