package agent

import (
	"fmt"

	"localcode/internal/config"
)

// What this turn is allowed to do about checking its own work, and how
// much room it has left.
//
// Both are facts the harness knows and the model cannot see. Left unsaid,
// the model substitutes a guess, and the guess is wrong in half the
// sessions: it runs a full test suite in a session where every command
// waits on a person, or it stops at "the change looks right" in a session
// where it could have proved it in four seconds.

// verifyPolicyFor is the sentence about running checks, or "" when there
// is nothing to run.
//
// The check tool is the narrow case — a fixed command from config.json,
// no arguments — so a turn that has it can always be told to use it. A
// turn with only bash is told the same thing about the project's own
// commands, since that is what a person would run.
func (l *Loop) verifyPolicyFor(advertised []string) string {
	has := func(name string) bool {
		for _, t := range advertised {
			if t == name {
				return true
			}
		}
		return false
	}
	checkable := has("check") || has(config.BashToolName)
	if !checkable {
		return ""
	}

	how := "the project's own build and test commands"
	if has("check") {
		how = "the check tool"
	}

	if l.Config.PermissionsSkipped() {
		return fmt.Sprintf("Verifying your work: commands in this session run without asking, so "+
			"finish by running %s and report what it said. Start with the narrowest check that "+
			"covers what you changed, then widen. A change you did not run is a change you are "+
			"guessing about, and saying so is better than implying otherwise.", how)
	}
	return fmt.Sprintf("Verifying your work: every command in this session waits for the person to "+
		"approve it, which costs them attention mid-thought. Do not run %s on your own initiative "+
		"while iterating. Finish the change, then say in one line what you would run to verify it "+
		"and let them decide. Running checks is expected when the task itself is about tests: "+
		"adding them, fixing them, or reproducing a bug.", how)
}

// contextLeftFor is the sentence about remaining room, or "" before the
// session has a measured number.
//
// Estimated numbers are deliberately not used. Every count on this side
// is a four-characters-per-token guess that is wrong in both directions
// for CJK, and a model told "you have 40,000 tokens left" acts on it.
// Until the server has reported real usage there is nothing honest to
// say, so nothing is said.
func (l *Loop) contextLeftFor(sessionID string) string {
	u, ok := l.getUsage(sessionID)
	if !ok || u.MaxContext <= 0 {
		return ""
	}
	left := u.MaxContext - (u.InputTokens + u.OutputTokens)
	if left < 0 {
		left = 0
	}
	pct := float64(left) / float64(u.MaxContext) * 100

	s := fmt.Sprintf("Context: about %s of %s tokens left in this window (%.0f%%), as of the last "+
		"answer the server measured.", thousands(left), thousands(u.MaxContext), pct)
	switch {
	case pct < 10:
		return s + " There is not room for another long file. Finish what you are holding, or say" +
			" what you would do next and stop; the conversation is compacted when it fills, and" +
			" work described is work that survives that."
	case pct < 25:
		return s + " Read in ranges rather than whole files from here, and prefer finishing one" +
			" thread to opening another."
	}
	return s
}

// thousands writes an integer with separators, because a model reading
// "183000" and a model reading "183,000" do not read the same number.
func thousands(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		return s
	}
	var b []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			b = append(b, ',')
		}
		b = append(b, c)
	}
	return string(b)
}
