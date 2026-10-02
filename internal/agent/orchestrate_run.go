package agent

import (
	"context"
	"fmt"
	"time"

	"localcode/internal/events"
	"localcode/internal/smart"
)

// Running a plan.
//
// A plain Go loop over stages, the shape runDebate already established, and
// three decisions worth stating because each was the alternative's failure.
//
// It runs INLINE, inside the tool call that asked for it, rather than
// booking itself the way a debate does. A debate has to defer because it
// appends to the conversation it was started from, and two writers on one
// history is the problem pendingDebate exists to avoid. Every stage here
// runs in a child session instead, so nothing else touches the parent's
// history and there is nothing to defer.
//
// Every launch is SYNCHRONOUS. That is what makes Esc work: spawnSync
// derives the child's context from the caller's, so cancelling the turn
// cancels the whole run, down to the stage in flight. The background path
// deliberately roots children in the daemon's context so they outlive their
// turn, which is right for a task somebody comes back for and exactly wrong
// here: it would leave a fan-out running, spending, and writing into the
// workspace after the person who started it had stopped watching.
//
// The concurrency inside a fanout is the run's own, not the task manager's
// semaphore. Synchronous delegation deliberately takes no global slot (see
// the comment in TaskManager.run: an ancestor holding one while waiting on
// a descendant is a deadlock, not a race), so a fanout has to bound itself.

// Timeouts. Nothing else in this repository bounds a single turn's wall
// clock, and a fan-out is where that stops being tolerable: one local
// server that has stopped answering holds every other stage behind it, and
// the run has no way to notice.
const (
	// stageTimeout bounds one agent's turn inside a run.
	stageTimeout = 10 * time.Minute
	// runTimeout bounds the whole plan.
	runTimeout = 30 * time.Minute
	// maxParallel is how many stage agents run at once inside a fanout.
	maxParallel = 4
)

// roleTools is the allowlist each role pins on its children.
//
// A stage names a role; it cannot enumerate tools. Otherwise a plan becomes
// a way to hand bash to a reviewer, and the whole argument for a read-only
// specialist is that its answer can be trusted not to have changed anything
// on the way.
var roleTools = map[string][]string{
	"readonly": {smart.ToolRead, smart.ToolGlob, smart.ToolGrep, "check"},
	"builder":  {smart.ToolRead, smart.ToolWrite, smart.ToolEdit, smart.ToolGlob, smart.ToolGrep, smart.ToolBash, "check"},
	"runner":   {smart.ToolRead, smart.ToolGlob, smart.ToolGrep, smart.ToolBash, "check"},
}

// toolsForRole is the allowlist a stage's child runs under: the role's set,
// intersected with the agent's own restriction if it has one.
//
// Intersected rather than replaced, for the reason a debate reviewer's list
// is: a plan must not be able to widen what an agent may do. A user who
// restricted their own agent to reading gets that, whatever role a plan
// names it under.
func (l *Loop) toolsForRole(ctx context.Context, agentName, role string) []string {
	if role == "" {
		role = "readonly"
	}
	allowed := roleTools[role]
	cfg := l.agentConfig(ctx, agentName)
	if len(cfg.Tools) > 0 {
		own := map[string]bool{}
		for _, n := range cfg.Tools {
			own[n] = true
		}
		var narrowed []string
		for _, n := range allowed {
			if own[n] {
				narrowed = append(narrowed, n)
			}
		}
		allowed = narrowed
	}
	return append(append([]string(nil), allowed...), answerToolName)
}

// inOrchestrationKey marks every turn inside a run, so nothing in one can
// start another. A plan that can run plans turns a 32-agent ceiling into
// 32^n, and there is no honest way to price that at the permission prompt.
type inOrchestrationKey struct{}

func withInOrchestration(ctx context.Context) context.Context {
	return context.WithValue(ctx, inOrchestrationKey{}, true)
}

func inOrchestration(ctx context.Context) bool {
	on, _ := ctx.Value(inOrchestrationKey{}).(bool)
	return on
}

// unit is one agent launch: which stage, over which item, which copy.
type unit struct {
	stage string
	item  string
	copy  int
}

// outcome is what one unit produced.
type outcome struct {
	unit
	agent string
	text  string
	// data is the structured answer when the stage declared one and the
	// agent gave it. Nil otherwise, which is what "unanswered" means.
	data map[string]any
	err  error
	// kept records whether this outcome passed the stage's keep filter, so
	// the report can say how many were dropped rather than only how many
	// survived.
	kept bool
	// round is which run of a repeat_until stage produced this, starting
	// at 1. Zero on every other stage, so reports of ordinary stages read
	// exactly as before.
	round int
}

// runReport is what the tool hands back to the orchestrating model. Written
// by localcode, from what happened, rather than summarised by a model:
// the one thing a run must not do is report a success nobody observed.
type runReport struct {
	stages   []stageReport
	launched int
	stopped  string
}

type stageReport struct {
	name       string
	kind       string
	agent      string
	launched   int
	kept       int
	unanswered int
	failed     int
	// dropped is how many items of a reference fanout did not run because
	// the run's agent ceiling was in the way. Named rather than silent.
	dropped int
	// merged is how many repeats of an item an earlier stage returned.
	merged int
	// rounds is how many times a repeat_until stage ran, and settled says
	// whether its field came back true. Zero on every other stage.
	rounds  int
	settled bool
	// answers is what a stage produced, in launch order.
	answers []outcome
}

// runPlan executes a validated plan and returns the report.
//
// ctx is the tool call's, so Esc reaches every stage. The run's own
// deadline is layered on top of it.
func (l *Loop) runPlan(ctx context.Context, sessionID string, p Plan) runReport {
	ctx, cancel := context.WithTimeout(withInOrchestration(ctx), runTimeout)
	defer cancel()

	report := runReport{}
	results := map[string][]outcome{}

	for _, stage := range p.Stages {
		if err := ctx.Err(); err != nil {
			report.stopped = "the run was cancelled or ran out of time before " + stage.Name
			return report
		}

		items, merged := l.stageItems(stage, results)
		copies := max(stage.Copies, 1)

		// A fanout over an earlier stage's results is the one width nobody
		// could know in advance, so it is the one the validator could not
		// price. It is capped here instead, against what the run has left,
		// and the number dropped is in the report: a run that quietly did
		// two thirds of what it said is the failure this design is against.
		dropped := 0
		if room := (maxRunAgents - report.launched) / copies; len(items) > room {
			if _, isRef := planRef(stage.Over); isRef {
				dropped = len(items) - max(room, 0)
				items = items[:max(room, 0)]
			}
		}

		var units []unit
		for _, item := range items {
			for c := range copies {
				units = append(units, unit{stage: stage.Name, item: item, copy: c + 1})
			}
		}
		if len(units) == 0 {
			report.stages = append(report.stages, stageReport{
				name: stage.Name, kind: stage.Kind, agent: stage.Agent, dropped: dropped, merged: merged,
			})
			if dropped > 0 {
				report.stopped = fmt.Sprintf("stopped at %s: the run had already launched %d of its %d agents, so none of its %d items could run",
					stage.Name, report.launched, maxRunAgents, dropped)
				return report
			}
			continue
		}
		if report.launched+len(units) > maxRunAgents {
			report.stopped = fmt.Sprintf("stopped before %s: the run had launched %d agents and this stage needs %d more, past the limit of %d",
				stage.Name, report.launched, len(units), maxRunAgents)
			return report
		}

		l.Store.Append(sessionID, events.TypeTaskStatus, map[string]any{
			"task_id": "orchestrate:" + stage.Name,
			"status":  "running",
			"stage":   stage.Name,
			"agents":  len(units),
		})

		brief := l.carriedInput(p, stage, results)
		got := l.runStage(ctx, sessionID, p, stage, units, brief)
		report.launched += len(units)

		sr := stageReport{name: stage.Name, kind: stage.Kind, agent: stage.Agent, launched: len(units), dropped: dropped, merged: merged}
		// Round 1 is stamped only on a repeat_until stage: on an ordinary
		// stage every outcome stays at round 0 and the report reads as
		// before, while a repeated stage labels every round including the
		// first, so identical answers do not print as identical lines.
		first := 0
		if stage.RepeatUntil != "" {
			first = 1
		}
		kept, stopped := applyStageOutcome(stage, first, got, &sr)
		if stopped != "" {
			report.stages = append(report.stages, sr)
			report.stopped = stopped
			return report
		}
		sr.kept = len(kept)

		// A repeat_until stage runs again while its field is false or
		// empty. Each round reruns the same units with the rounds so far
		// carried in {{input}}, inside the same agent ceiling and the same
		// run deadline. The report says which round settled it, because a
		// loop that does not name its stopping round cannot be debugged.
		round := 1
		for stage.RepeatUntil != "" && round < stage.MaxRounds && !repeatSettled(stage, kept) {
			if err := ctx.Err(); err != nil {
				report.stages = append(report.stages, sr)
				report.stopped = "the run was cancelled or ran out of time before " + stage.Name + " finished its repeats"
				return report
			}
			if report.launched+len(units) > maxRunAgents {
				sr.rounds = round
				report.stages = append(report.stages, sr)
				report.stopped = fmt.Sprintf("stopped in %s: round %d settled nothing and the run had launched %d agents, past the limit of %d",
					stage.Name, round+1, report.launched, maxRunAgents)
				return report
			}
			round++
			l.Store.Append(sessionID, events.TypeTaskStatus, map[string]any{
				"task_id": "orchestrate:" + stage.Name,
				"status":  "running",
				"stage":   stage.Name,
				"agents":  len(units),
				"round":   round,
			})
			brief = l.carriedInput(p, stage, resultsWith(results, stage.Name, kept))
			got = l.runStage(ctx, sessionID, p, stage, units, brief)
			report.launched += len(units)
			sr.launched += len(units)
			kept, stopped = applyStageOutcome(stage, round, got, &sr)
			if stopped != "" {
				sr.rounds = round
				report.stages = append(report.stages, sr)
				report.stopped = stopped
				return report
			}
			sr.kept = len(kept)
			l.Store.Append(sessionID, events.TypeTaskStatus, map[string]any{
				"task_id": "orchestrate:" + stage.Name,
				"status":  "completed",
				"stage":   stage.Name,
				"kept":    len(kept),
				"round":   round,
			})
		}
		if stage.RepeatUntil != "" {
			sr.rounds = round
			sr.settled = repeatSettled(stage, kept)
		}
		report.stages = append(report.stages, sr)
		results[stage.Name] = kept

		l.Store.Append(sessionID, events.TypeTaskStatus, map[string]any{
			"task_id": "orchestrate:" + stage.Name,
			"status":  "completed",
			"stage":   stage.Name,
			"kept":    len(kept),
		})
	}
	return report
}
