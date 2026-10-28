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
// It runs as a synchronous child of the tool call that asked for it: a run
// session (SpawnRun), with every unit a child of the run rather than of the
// conversation. Not booked the way a debate is: a debate has to defer
// because it appends to the conversation it was started from, and two
// writers on one history is the problem pendingDebate exists to avoid.
// Every stage here runs in a session of its own instead, so nothing else
// touches the parent's history and there is nothing to defer.
//
// Every launch is SYNCHRONOUS. That is what makes Esc work: the turn waits
// on the run and the run waits on its units, so cancelling the turn
// cancels the whole run, down to the stage in flight. The background path
// deliberately roots children in the daemon's context so they outlive their
// turn, which is right for a task somebody comes back for and wrong as a
// default here: it would leave a fan-out running, spending, and writing
// into the workspace after the person who started it had stopped watching.
// So the only way a run outlives its turn is the person letting it go, the
// same let-go every synchronous sub-agent has, and because the run is one
// session that let-go takes all of it.
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

// runSessionAgent is the agent name a run session carries. Not an agent:
// a run has no model turn of its own, and a message typed into one is
// refused rather than handed to whatever agent shares the name. The space
// keeps it out of the names a config or a slash command can use.
const runSessionAgent = "orchestration run"

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

// unit is one agent launch: which stage, over which item, which copy, and
// in which pass of a repeating stage (0 for a stage that does not repeat).
type unit struct {
	stage string
	item  string
	copy  int
	pass  int
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
	// reused marks an outcome a resumed run took from the run it resumed
	// rather than launching an agent for.
	reused bool
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
	// reused is how many of this stage's outcomes a resumed run took from
	// the run it resumed.
	reused int
	// pipelined marks a stage that started on items while the stage before
	// it was still running.
	pipelined bool
	// dry marks a repeat_until_dry stage, and ranDry says whether it
	// stopped because its rounds stopped finding anything new rather than
	// because it ran out of rounds.
	dry    bool
	ranDry bool
	// answers is what a stage produced, in launch order.
	answers []outcome
}

// progressSession is where a run's stage lines go: the conversation while
// its turn is waiting on the run, and the run's own session once the
// person has let it go. A run that carried on writing into the
// conversation would interleave its progress with turns that have nothing
// to do with it.
func (l *Loop) progressSession(sessionID, runID string) string {
	if runID != sessionID && l.Tasks != nil && l.Tasks.LetGo(runID) {
		return runID
	}
	return sessionID
}

// runPlan executes a validated plan and returns the report.
//
// ctx is the run session's, which the turn that started the run cancels
// for as long as it is waiting on it, so Esc reaches every stage. The
// run's own deadline is layered on top of it. sessionID is the
// conversation, where the stage progress lines go; runID is the run
// session every unit is spawned under.
func (l *Loop) runPlan(ctx context.Context, sessionID, runID string, p Plan) runReport {
	ctx, cancel := context.WithTimeout(withInOrchestration(ctx), runTimeout)
	defer cancel()

	report := runReport{}
	results := map[string][]outcome{}

	for i := 0; i < len(p.Stages); i++ {
		stage := p.Stages[i]
		if err := ctx.Err(); err != nil {
			report.stopped = "the run was cancelled or ran out of time before " + stage.Name
			return report
		}

		// A fanout followed by pipelined stages runs as one group: each
		// pipelined stage starts on an item as soon as the unit that found
		// it, and every unit launched before that one, has finished.
		if group := pipelineGroup(p.Stages, i); len(group) > 1 {
			if stopped := l.runPipeline(ctx, sessionID, runID, p, group, results, &report); stopped != "" {
				report.stopped = stopped
				return report
			}
			i += len(group) - 1
			continue
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

		l.Store.Append(l.progressSession(sessionID, runID), events.TypeTaskStatus, map[string]any{
			"task_id": "orchestrate:" + stage.Name,
			"status":  "running",
			"stage":   stage.Name,
			"agents":  len(units),
		})

		brief := l.carriedInput(p, stage, results)
		first := 0
		if stage.repeats() {
			first = 1
		}
		got := l.runStage(ctx, sessionID, runID, p, stage, units, brief, first)
		report.launched += len(units)

		sr := stageReport{name: stage.Name, kind: stage.Kind, agent: stage.Agent, launched: len(units), dropped: dropped, merged: merged}
		// Round 1 is stamped only on a repeat_until stage: on an ordinary
		// stage every outcome stays at round 0 and the report reads as
		// before, while a repeated stage labels every round including the
		// first, so identical answers do not print as identical lines.
		kept, stopped := applyStageOutcome(stage, first, got, &sr)
		if stopped != "" {
			report.stages = append(report.stages, sr)
			report.stopped = stopped
			return report
		}
		sr.kept = len(kept)

		// What the stage hands on. One round's kept results for every
		// stage but a dry loop, which hands on every round's: its later
		// rounds exist for what they add. seen and empty are the dry
		// loop's own count of what it has found and of how many rounds in
		// a row have added nothing; the first round counts too, since an
		// answered round that finds nothing is a search that ran dry.
		carried := kept
		seen := map[string]bool{}
		empty := 0
		if stage.RepeatUntilDry != "" {
			empty = dryCount(stage, got, kept, seen, empty)
		}
		finished := func() bool {
			if stage.RepeatUntilDry != "" {
				return empty >= stage.dryRounds()
			}
			return repeatSettled(stage, kept)
		}

		// A repeat_until stage runs again while its field is false or
		// empty. Each round reruns the same units with the rounds so far
		// carried in {{input}}, inside the same agent ceiling and the same
		// run deadline. The report says which round settled it, because a
		// loop that does not name its stopping round cannot be debugged.
		round := 1
		for stage.repeats() && round < stage.MaxRounds && !finished() {
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
			l.Store.Append(l.progressSession(sessionID, runID), events.TypeTaskStatus, map[string]any{
				"task_id": "orchestrate:" + stage.Name,
				"status":  "running",
				"stage":   stage.Name,
				"agents":  len(units),
				"round":   round,
			})
			brief = l.carriedInput(p, stage, resultsWith(results, stage.Name, carried))
			got = l.runStage(ctx, sessionID, runID, p, stage, units, brief, round)
			report.launched += len(units)
			sr.launched += len(units)
			kept, stopped = applyStageOutcome(stage, round, got, &sr)
			if stopped != "" {
				sr.rounds = round
				report.stages = append(report.stages, sr)
				report.stopped = stopped
				return report
			}
			if stage.RepeatUntilDry != "" {
				empty = dryCount(stage, got, kept, seen, empty)
				carried = append(carried, kept...)
			} else {
				carried = kept
			}
			sr.kept = len(carried)
			l.Store.Append(l.progressSession(sessionID, runID), events.TypeTaskStatus, map[string]any{
				"task_id": "orchestrate:" + stage.Name,
				"status":  "completed",
				"stage":   stage.Name,
				"kept":    len(carried),
				"round":   round,
			})
		}
		if stage.RepeatUntil != "" {
			sr.rounds = round
			sr.settled = repeatSettled(stage, kept)
		}
		if stage.RepeatUntilDry != "" {
			sr.rounds = round
			sr.dry = true
			sr.ranDry = empty >= stage.dryRounds()
		}
		report.stages = append(report.stages, sr)
		results[stage.Name] = carried

		l.Store.Append(l.progressSession(sessionID, runID), events.TypeTaskStatus, map[string]any{
			"task_id": "orchestrate:" + stage.Name,
			"status":  "completed",
			"stage":   stage.Name,
			"kept":    len(carried),
		})
	}
	return report
}
