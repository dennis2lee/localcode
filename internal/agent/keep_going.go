package agent

import (
	"fmt"
	"strings"
	"unicode"

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
// both "here is what I did" and "here is what remains". So the model is
// asked which it was — and asked in a way it cannot answer with more
// work: the question goes out with the tools switched off (see
// keepGoingVerdictPrompt), and only an answer of MORE is followed by a
// carry-on with the tools back on. It is a property of one model family,
// not of the work, and bounded, because the one thing worse than a model
// that stops early is a session that prompts itself forever.

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

// keepGoingVerdictPrompt is the question, and the request that carries
// it has tool_choice "none": the tool definitions stay on the wire, so a
// server's prefix cache still holds, but the model may not call one.
//
// That is the whole design, and it comes from a measurement. The old
// carry-on was one message that asked whether the work was complete
// and, if not, told the model to take the next step with the tools.
// Sent to a model that had finished, it did what a compliant model does
// with tools in reach: it checked — `grep timeout`, `find . -type f`,
// `grep -r 30` — then said the task was complete, then was asked again.
// On a task finished in six requests that cost seven more and the whole
// budget, and every guard on what counted as work (v0.53.0, v0.107.0)
// was a patch over the same hole: the model was handed the means to
// redo the task in the same breath as the question of whether it needed
// to.
//
// With no tool to reach for, the only thing a finished model can do is
// say so, and that takes one request and a few tokens. A stalled model
// says MORE and names the step, and the carry-on that follows goes to a
// model that has just said, in its own words, that there is one.
//
// It still must not assert that the work is unfinished. localcode has
// no evidence either way, and a model told it has not finished finds
// something to finish. The question names both answers.
const keepGoingVerdictPrompt = "Is the task you were given complete? " +
	"Answer on the first line with one word: DONE if it is, or MORE if steps remain. " +
	"If MORE, say on the next line what remains. Answer only; do not start the work here."

// keepGoingPrompt is the carry-on. It is sent only after the model has
// answered MORE, so unlike its predecessor it may take the model at its
// word that there is a step to take.
const keepGoingPrompt = "Take the next step now using the tools instead of describing it, and keep going until " +
	"the work is done. If you need a decision only the user can make, ask for it plainly."

// stopVerdict is the model's answer to keepGoingVerdictPrompt as
// localcode reads it.
type stopVerdict int

const (
	// stopUnclear is an answer that said neither. It ends the turn:
	// the cost of a carry-on the model did not ask for is the loop this
	// file exists to prevent, and the cost of a missed one is the person
	// typing "continue", which is what they did before the feature.
	stopUnclear stopVerdict = iota
	stopDone
	stopMore
)

// parseVerdict reads the first line the model wrote. The two words are
// the ones the prompt asked for and they win outright; the rest are the
// plain ways of saying the same thing, because a model that answers "Not
// yet — config.go still calls it" has answered.
//
// The order matters and was chosen against real sentences. "No further
// changes are needed" opens with "no" and means done; "No, global_init.go
// still needs the change" opens with the same word and means the
// opposite. So the phrases that say "nothing left" are read before the
// phrases that say "something left", and a bare yes or no is the last
// resort rather than the first.
func parseVerdict(text string) stopVerdict {
	line := strings.ToUpper(strings.Trim(firstNonEmptyLine(text), "*_`#>-—: \t.!"))
	word := line
	if i := strings.IndexFunc(line, func(r rune) bool { return !unicode.IsLetter(r) }); i >= 0 {
		word = line[:i]
	}
	switch word {
	case "MORE":
		return stopMore
	case "DONE":
		return stopDone
	}
	has := func(phrases ...string) bool {
		for _, p := range phrases {
			if strings.Contains(line, p) {
				return true
			}
		}
		return false
	}
	switch {
	case has("NOTHING", "NO FURTHER", "NO MORE", "NO REMAINING", "NO OTHER", "ALL DONE"):
		return stopDone
	case has("NOT DONE", "NOT COMPLETE", "NOT YET", "NOT FINISHED", "INCOMPLETE", "UNFINISHED",
		"STILL NEED", "STILL HAS", "STILL HAVE", "REMAIN", "미완", "남아", "남았"):
		return stopMore
	case has("DONE", "COMPLETE", "FINISHED", "완료", "끝났"):
		return stopDone
	}
	switch word {
	case "YES":
		return stopDone
	case "NO":
		return stopMore
	}
	return stopUnclear
}

// firstNonEmptyLine is the first line of a reply that says anything,
// trimmed. Not firstLine: that one is a log-line cut of a tool result
// and keeps a leading blank line.
func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// verdictSummary is what the notice quotes: the answer and, when the
// model went on to name what remains, that line too. Bounded, because
// the notice is one line in a transcript.
func verdictSummary(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
			if len(lines) == 2 {
				break
			}
		}
	}
	s := strings.Join(lines, " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}

// keepGoing decides whether this stop is one to question — whether the
// verdict prompt goes out at all.
//
// Every condition here is a case where stopping was the right thing to do
// and even asking would override the wrong person:
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
func (l *Loop) keepGoing(sessionID string, profile config.Profile, stopReason string, reply []provider.Block, ranTools, refused, nudgedSinceWork bool, nudges int) bool {
	if budget := l.effectiveKeepGoing(profile); budget <= 0 || nudges >= budget {
		return false
	}
	if !ranTools || refused || stopReason == "max_tokens" {
		return false
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
		return false
	}
	// The user has already typed what happens next. It reaches the model
	// as soon as this turn ends, which is now.
	if l.UserWaiting != nil && l.UserWaiting(sessionID) {
		return false
	}
	if endsWithQuestion(replyText(reply)) {
		return false
	}
	return true
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

// changedSomething reports whether a step did anything to the project, as
// opposed to looking at it again.
//
// This is what clears the carry-on guard, and "a call this turn has not
// made before" was the wrong test for it. Measured against the model the
// feature exists for, on a task it had already finished correctly: told
// to check whether the work was complete, it ran `grep timeout`, then
// `grep 30`. Neither was a repeat — both were calls it had not made — so
// both counted as work, both cleared the guard, and both bought another
// nudge. A finished turn of nine requests became thirteen, all four of
// them re-confirming a change already made.
//
// The file's own comment already said what the test should be: re-running
// the build after fixing what broke it is work, because the fix is a call
// of its own; re-running it to admire the result is not. A model prodded
// into looking somewhere new is admiring the result with more steps.
//
// Paired with newWork rather than replacing it: a call has to be new AND
// a change. New alone let a prodded model look somewhere it had not
// looked; a change alone would let it re-run the same command.
//
// So only the three calls that can change the workspace count. read_file,
// grep, glob and check are observations, and an observation is what a
// model reaches for when it has nothing left to do and has been told to
// carry on. Task is excluded on the same ground and one more: on a
// one-profile roster it is the model delegating to itself, which is the
// last thing another nudge should pay for.
func changedSomething(toolUses []provider.Block) bool {
	for _, tu := range toolUses {
		switch tu.ToolName {
		case "write_file", "edit", "bash":
			return true
		}
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
// before the turn is ended, once the guard is on. The guard is off
// unless somebody turns it on (see config.RepeatLimit for why); the live
// value is Loop.RepeatLimit, and this is what "on" means.
//
// Deliberately small. A step that repeats every call it has already made
// has, by construction, changed nothing, so the next one has the same
// input and no reason to differ; three of them is not a model working
// slowly, it is a model that will not stop. The cost of being wrong in
// this direction is one ended turn with a message saying why. The cost of
// the other direction was measured: a thousand requests and a session
// that could never be spoken to again.
const maxRepeatSteps = config.RepeatLimitOn

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
