package agent

import (
	"context"
	"fmt"
	"strings"
)

// "/review": read what changed and say what is wrong with it.
//
// The most-run command in a coding agent, and the one localcode did not
// have. It came first on a parity review's candidate list and then did
// not get built, which is worth recording: the cycle that followed
// implemented the *verification* of that list and the list itself was
// never picked back up.
//
// A prompt template rather than a tool or a mode, for the same reason
// "/init" is one. What a review needs is not a capability the model
// lacks — it can already read a diff and a file — it is being asked the
// right question about the right range, with the standards stated. Every
// user who wanted this wrote it into `.localcode/commands/review.md`
// themselves, which is one per repository and none of them the same.
//
// It is not "/debate". Debate sends the work to other agents and runs
// rounds; this is one turn, by whoever is answering, about a diff.

// reviewPrompt is the question. The range is spliced in by routeReview.
const reviewPrompt = `Review the changes described below. Read them, then read enough of the surrounding code to judge them: a diff is not reviewable on its own, and a change that looks right in isolation is the one worth checking against its callers.

Look for, in this order:

1. Correctness. Does it do what it says, on the inputs it will actually see? Name the input and the wrong result, not "could fail".
2. What it breaks. Callers, tests, saved data, anything reading the same file or table from elsewhere.
3. What it forgot. An error dropped, a boundary not checked, a case the tests do not cover.
4. Simplification. Code already here that does this, or the same work expressed in less.

For each finding: the file and line, one sentence on what is wrong, and a concrete failing case. Rank the findings, worst first.

Say so plainly when the change is fine. A review that manufactures findings to look thorough is worse than a short one, because the next one is read the same way.

Do not change any file. This is a review; the person asked what is wrong, not for it to be fixed.`

// reviewRange turns the argument into the range to describe, and the
// command to get it.
//
// Four shapes, because a review is asked about four different things and
// a person types them all the same way. Whatever is given is passed
// through to git rather than parsed further: a revision, a range, a
// branch and a path are the same characters to this function, and git is
// the thing that knows them apart.
func reviewRange(arg string) (what, how string) {
	arg = strings.TrimSpace(arg)
	switch {
	case arg == "":
		// The default, and the common case: what is in the tree and not
		// yet committed. Staged and unstaged together, because that
		// split is about how somebody is committing rather than about
		// what they changed.
		return "the uncommitted changes in this working tree",
			"git status --short && git --no-pager diff HEAD"
	case strings.EqualFold(arg, "staged"):
		return "the staged changes", "git --no-pager diff --cached"
	case strings.EqualFold(arg, "head"), strings.EqualFold(arg, "last"):
		return "the most recent commit", "git --no-pager show HEAD"
	}
	// Anything else is handed to git as it was typed.
	return fmt.Sprintf("the changes in %q", arg),
		fmt.Sprintf("git --no-pager diff %s", arg)
}

// routeReview answers "/review" and "/review <what>".
func (l *Loop) routeReview(ctx context.Context, sessionID, agentName, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/review")
	if !ok {
		return false, nil
	}
	what, how := reviewRange(arg)

	var b strings.Builder
	fmt.Fprintf(&b, "Review %s.\n\nStart by running:\n\n    %s\n\n", what, how)
	// Named rather than left for the model to work out, because a review
	// of the wrong range reads exactly like a review of the right one.
	b.WriteString("If that shows nothing, say so and stop rather than reviewing something else.\n\n")
	b.WriteString(reviewPrompt)

	return true, l.sendWithModelText(ctx, sessionID, agentName, text, b.String(), "", "",
		messageOrigin{source: "command.review"})
}
