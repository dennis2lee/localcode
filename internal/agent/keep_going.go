package agent

import (
	"fmt"
	"strings"

	"localcode/internal/config"
	"localcode/internal/provider"
)

// Carrying on after a model stops with the task unfinished.
//
// The habit this answers is specific and, on some local models, constant:
// a tool comes back, the model reads the result, writes down what still
// has to happen — "global_init.cpp also has to be updated" — and ends its
// turn. Typing "carry on" makes it pick up exactly where it left off, and
// then it stops again a step later. The person is being used as the
// model's own loop.
//
// This is not the same fault as the one fixed in v0.46.0. That one was
// mechanical: the reply *did* ask for tools and localcode ended the turn
// anyway, because the server had labelled the stop reason "stop" or sent
// none at all. That is fixed where it belongs, in the provider. What is
// left is a model that genuinely stops, with nothing in the reply to run.
//
// There is no way to tell that apart from a model that has finished. A
// turn that ends after tool use with a paragraph of prose is the shape of
// both "here is what I did" and "here is what remains". So this is opt-in
// per profile — it is a property of the model, not of the work — and
// bounded, because the one thing worse than a model that stops early is a
// session that prompts itself forever.

// keepGoingApplies reports whether the feature exists for this model at
// all: only when its id contains "muse", case-insensitively.
//
// A hard gate rather than a default, and the distinction matters. The
// quirk table already made muse the only family with a budget, but a
// profile's own keep_going number could opt any model in, and the switch
// below could be read as arming the feature everywhere. Neither is
// wanted: the habit this compensates for is one family's, a nudge sent
// to a model without it is localcode second-guessing a finished answer,
// and the person flipping "/keep-going on" should not have to know which
// of their profiles it could reach. On anything that is not muse, this
// feature does not exist, whatever else is configured.
func keepGoingApplies(model string) bool {
	return strings.Contains(strings.ToLower(model), "muse")
}

// effectiveKeepGoing resolves the carry-on budget for a profile.
//
// Zero unless the feature applies to the model (see keepGoingApplies)
// and the daemon-wide switch is on. Then the profile's own number wins
// when set — including -1, which is "never, whatever the switch says" —
// and unset falls back to the family default, which is what makes the
// fix arrive with the release: the person who reported the stall gets a
// model that finishes by installing the update, not by finding a config
// key.
func (l *Loop) effectiveKeepGoing(profile config.Profile) int {
	if !keepGoingApplies(profile.Model) || !l.KeepGoingEnabled() {
		return 0
	}
	if profile.KeepGoing != 0 {
		return profile.KeepGoing
	}
	return modelKeepGoing(profile.Model)
}

// keepGoingPrompt is what the model is told. It is a user message,
// because that is the only thing a model answers.
//
// It says what may have happened rather than just "continue": the failure
// is the model treating a description of the next step as an acceptable
// end to a turn, and naming that is what stops the next reply being
// another description.
//
// What it must not do is assert that the work is unfinished. localcode
// cannot tell a stall from a finished task — that is stated plainly at
// the top of this file — so a prompt that says "you did not finish" is a
// claim it has no evidence for, aimed at a model whose whole training is
// to comply with the last instruction. A finished model told it has not
// finished goes and finds something to do: it re-reads a file, re-runs
// the build, redoes the work it just did. That is the "it keeps repeating
// a task it already completed" report, and it comes from this sentence
// rather than from the budget.
//
// So the question is put as a question, and "it is already done" is named
// as an acceptable answer. A model that genuinely stalled still has its
// next step to take; one that finished now has somewhere to go that is
// not more work.
const keepGoingPrompt = "Check whether the task you were given is actually complete. " +
	"If it is, say so in one line and stop — do not re-run, re-check or redo work you have already done. " +
	"If it is not, take the next step now using the tools instead of describing it, and keep going until the " +
	"work is done. If you need a decision only the user can make, ask for it plainly."

// keepGoing decides whether this turn should carry on by itself, and with
// what.
//
// Every condition here is a case where stopping was the right thing to do
// and saying "continue" would override the wrong person:
//
//   - nothing has run yet, so this was a question and its answer;
//   - the last tool call was refused, so the model stopped because it was
//     told to;
//   - the reply ends in a question, which is the model asking rather than
//     stalling;
//   - the reply was cut off by max_tokens, which needs a bigger cap and
//     not another turn — and is already reported;
//   - the last carry-on produced no work, so the model is saying it is
//     finished rather than stalling;
//   - the user has typed something that has not reached the model yet,
//     which is a better continuation than an invented one.
func (l *Loop) keepGoing(sessionID string, profile config.Profile, stopReason string, reply []provider.Block, ranTools, refused, nudgedSinceWork bool, nudges int) (string, bool) {
	if budget := l.effectiveKeepGoing(profile); budget <= 0 || nudges >= budget {
		return "", false
	}
	if !ranTools || refused || stopReason == "max_tokens" {
		return "", false
	}
	// Told to carry on once already, and nothing new came of it.
	//
	// "Nothing new" is the important part, and it is what this guard got
	// wrong. It used to clear on any tool call at all, on the reasoning
	// that a stalled model picks up a tool the moment it is prodded and a
	// finished one answers with more prose. A finished model does not
	// answer with prose: told it has not finished, it goes and does
	// something — reads a file it has already read, re-runs the build it
	// has already run — and every one of those cleared the guard and
	// bought another nudge. A completed task was re-executed for the whole
	// budget, which is the fault this reads as from the outside.
	//
	// So work now means a call this turn has not already made. Re-running
	// the same build after fixing the file that broke it is still work,
	// because the fix is a call of its own; re-running it to admire the
	// result is not.
	if nudgedSinceWork {
		return "", false
	}
	// The user has already typed what happens next. It reaches the model
	// as soon as this turn ends, which is now.
	if l.UserWaiting != nil && l.UserWaiting(sessionID) {
		return "", false
	}
	if endsWithQuestion(replyText(reply)) {
		return "", false
	}
	return keepGoingPrompt, true
}

// replyText is the text of one assistant message, tool calls aside.
func replyText(blocks []provider.Block) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == provider.BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// endsWithQuestion reports whether the last thing the model wrote was a
// question to the user.
//
// The last non-empty line rather than the whole text, because a reply
// that asks something and then adds a closing line is still asking. The
// full-width mark is there because this is used against Korean and
// Japanese text as often as English.
func endsWithQuestion(text string) bool {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(strings.TrimSpace(lines[i]), "*_`\"'）)】」”’ ")
		if line == "" {
			continue
		}
		return strings.HasSuffix(line, "?") || strings.HasSuffix(line, "？")
	}
	return false
}

// newWork records this step's tool calls and reports whether any of them
// is one this turn has not made before.
//
// The key is the tool's name and its arguments exactly as the model sent
// them, so "the same call" means the same call and not merely the same
// tool: editing two files is two pieces of work, and reading one file
// twice is one.
//
// Called for its side effect as much as its answer — every step's calls
// are recorded, whether or not a nudge is in play, because the comparison
// a nudge needs is against the whole turn rather than the step before it.
func newWork(seen map[string]bool, toolUses []provider.Block) bool {
	fresh := false
	for _, tu := range toolUses {
		key := tu.ToolName + "\x00" + string(tu.ToolInput)
		if !seen[key] {
			seen[key] = true
			fresh = true
		}
	}
	return fresh
}

// maxRepeatSteps is how many steps in a row may ask for nothing new
// before the turn is ended, when nobody has said otherwise. The live
// value is Loop.RepeatLimit, which a person can move or zero; this is
// where the default is explained.
//
// Deliberately small. A step that repeats every call it has already made
// has, by construction, changed nothing, so the next one has the same
// input and no reason to differ; three of them is not a model working
// slowly, it is a model that will not stop. The cost of being wrong in
// this direction is one ended turn with a message saying why. The cost of
// the other direction was measured: a thousand requests and a session
// that could never be spoken to again.
const maxRepeatSteps = config.DefaultRepeatLimit

// describeCalls names a step's tool calls for the repeat notice: the tool
// and the first stretch of its arguments, at most three of them, so the
// person reading "repeated itself" can see what it repeated.
func describeCalls(calls []provider.Block) string {
	const most = 3
	var parts []string
	for i, tu := range calls {
		if i == most {
			parts = append(parts, fmt.Sprintf("and %d more", len(calls)-most))
			break
		}
		args := strings.Join(strings.Fields(string(tu.ToolInput)), " ")
		if r := []rune(args); len(r) > 60 {
			args = string(r[:60]) + "…"
		}
		parts = append(parts, tu.ToolName+" "+args)
	}
	if len(parts) == 0 {
		return "no calls"
	}
	return strings.Join(parts, "; ")
}
