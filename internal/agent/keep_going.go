package agent

import (
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

// effectiveKeepGoing resolves the carry-on budget for a profile.
//
// The profile's own number wins when set — including -1, which is "never,
// whatever the model". Unset falls back to the model's default from the
// quirk table, which is what makes the fix arrive with the release: the
// person who reported the stall gets a model that finishes by installing
// the update, not by finding a config key.
func effectiveKeepGoing(profile config.Profile) int {
	if profile.KeepGoing != 0 {
		return profile.KeepGoing
	}
	return modelKeepGoing(profile.Model)
}

// keepGoingPrompt is what the model is told. It is a user message,
// because that is the only thing a model answers.
//
// It says what happened rather than just "continue": the failure is the
// model treating a description of the next step as an acceptable end to a
// turn, and naming that is what stops the next reply being another
// description.
const keepGoingPrompt = "Continue. You ended your turn with the task unfinished — you described the next step " +
	"instead of taking it. Take it now, using the tools, and keep going until the work is actually done. " +
	"End your turn only when the task is complete, or when you need a decision that only the user can make " +
	"— and if you need one, ask for it plainly."

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
	if budget := effectiveKeepGoing(profile); budget <= 0 || nudges >= budget {
		return "", false
	}
	if !ranTools || refused || stopReason == "max_tokens" {
		return "", false
	}
	// Told to carry on once already, and it answered with more prose. A
	// model that has genuinely finished says so twice; one that has
	// stalled picks up a tool the moment it is prodded. Taking the second
	// paragraph as the answer is what keeps a finished task from costing
	// the whole keep_going budget in turns that say "anything else?".
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
