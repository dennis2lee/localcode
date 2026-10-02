package agent

import (
	"fmt"
)

// applyStageOutcome sorts one round of units into the stage report: failed,
// unanswered, and kept. It returns the kept outcomes, plus why the run must
// stop when the stage says unanswered: fail. Every outcome is stamped with
// its round, so a repeated stage's answers do not print as identical lines.
func applyStageOutcome(stage Stage, round int, got []outcome, sr *stageReport) ([]outcome, string) {
	var kept []outcome
	for _, o := range got {
		o.round = round
		switch {
		case o.err != nil:
			sr.failed++
		case len(stage.Returns) > 0 && o.data == nil:
			sr.unanswered++
			if stage.Unanswered == "fail" {
				return nil, fmt.Sprintf("stopped in %s: an agent did not answer in the shape the stage declared, and the stage says unanswered: fail", stage.Name)
			}
			if stage.Unanswered == "keep" {
				o.kept = true
				kept = append(kept, o)
			}
		default:
			if stage.Keep == "" || truthy(o.data[stage.Keep]) {
				o.kept = true
				kept = append(kept, o)
			}
		}
		sr.answers = append(sr.answers, o)
	}
	return kept, ""
}

// repeatSettled reports whether a repeat_until stage is done: every kept
// result carries the field holding it true. An empty kept set settles
// nothing, and one false copy keeps the loop running: settling on a
// majority would declare done work one agent says is not.
func repeatSettled(stage Stage, kept []outcome) bool {
	if len(kept) == 0 {
		return false
	}
	for _, o := range kept {
		v, ok := o.data[stage.RepeatUntil]
		if !ok || !truthy(v) {
			return false
		}
	}
	return true
}

// resultsWith is the carried-input view with one stage's results replaced:
// the next round of a repeat_until stage sees the rounds so far.
func resultsWith(results map[string][]outcome, stage string, kept []outcome) map[string][]outcome {
	out := make(map[string][]outcome, len(results)+1)
	for k, v := range results {
		out[k] = v
	}
	out[stage] = kept
	return out
}
