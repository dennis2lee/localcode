package agent

import (
	"context"
	"fmt"

	"localcode/internal/events"
)

// Pipelined stages: one scheduler for a fanout and the pipelined fanouts
// after it.
//
// Three rules carry the whole design, and each one is what the per-stage
// loop gave for free and a pipeline has to give on purpose.
//
//   - Ordered release. A unit's outcome is applied (kept, dropped, failed)
//     only once every unit launched before it in the same stage has
//     finished, and only then are its items handed to the next stage. So
//     the next stage's items, their order, the first sixteen that fit and
//     the report are the same on every run, and a resumed run can match a
//     unit by what it was asked.
//
//   - One budget. Every launch is counted when it is queued, by the one
//     goroutine that queues, against the same 32-turn ceiling; items that
//     would pass it are dropped and counted, as a reference fanout's are.
//
//   - Producers first. A free slot goes to the earliest stage with work
//     waiting, so the stages that find things are not starved by the
//     stages checking them.
//
// A stage that says unanswered: fail stops the whole group, cancelling the
// units already in flight, rather than letting later stages carry on with
// work the plan said must not happen.

// pipelineGroup is the stages from i that run together: a fanout followed
// by the pipelined fanouts after it. A single stage is no group.
func pipelineGroup(stages []Stage, i int) []Stage {
	if stages[i].Kind != "fanout" {
		return nil
	}
	j := i + 1
	for j < len(stages) && stages[j].Pipeline {
		j++
	}
	return stages[i:j]
}

// pipeStage is one stage's state inside a pipelined group.
type pipeStage struct {
	stage   Stage
	sr      stageReport
	units   []unit
	out     []outcome
	done    []bool
	queued  int // units handed to a worker
	next    int // first unit whose outcome has not been applied
	kept    []outcome
	seen    map[string]bool
	items   int
	brief   string
	allowed []string
	field   string // the field of the stage before that this one spreads over
	started bool
	ended   int // units whose outcome has come back, in any order
}

// runPipeline runs group, a fanout and the pipelined stages after it, and
// returns why the run must stop, or "".
func (l *Loop) runPipeline(ctx context.Context, sessionID, runID string, p Plan, group []Stage, results map[string][]outcome, report *runReport) string {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ps := make([]*pipeStage, len(group))
	for j, s := range group {
		st := &pipeStage{
			stage:   s,
			sr:      stageReport{name: s.Name, kind: s.Kind, agent: s.Agent, pipelined: j > 0},
			seen:    map[string]bool{},
			allowed: l.toolsForRole(ctx, s.Agent, s.Role),
		}
		if j > 0 {
			ref, _ := planRef(s.Over)
			_, st.field, _ = splitRef(ref)
		}
		ps[j] = st
	}

	progress := func(st *pipeStage, status string, extra map[string]any) {
		data := map[string]any{"task_id": "orchestrate:" + st.stage.Name, "status": status, "stage": st.stage.Name}
		for k, v := range extra {
			data[k] = v
		}
		l.Store.Append(l.progressSession(sessionID, runID), events.TypeTaskStatus, data)
	}

	// The first stage is an ordinary fanout: its items are known now, and
	// it is priced and capped exactly as runPlan prices and caps one.
	head := ps[0]
	items, merged := l.stageItems(head.stage, results)
	head.sr.merged = merged
	copies := max(head.stage.Copies, 1)
	if room := (maxRunAgents - report.launched) / copies; len(items) > room {
		if _, isRef := planRef(head.stage.Over); isRef {
			head.sr.dropped = len(items) - max(room, 0)
			items = items[:max(room, 0)]
		}
	}
	for _, item := range items {
		for c := range copies {
			head.units = append(head.units, unit{stage: head.stage.Name, item: item, copy: c + 1})
		}
	}
	if report.launched+len(head.units) > maxRunAgents {
		return fmt.Sprintf("stopped before %s: the run had launched %d agents and this stage needs %d more, past the limit of %d",
			head.stage.Name, report.launched, len(head.units), maxRunAgents)
	}
	head.out = make([]outcome, len(head.units))
	head.done = make([]bool, len(head.units))
	report.launched += len(head.units)
	head.sr.launched = len(head.units)
	head.brief = l.carriedInput(p, head.stage, results)
	head.started = true
	progress(head, "running", map[string]any{"agents": len(head.units)})
	l.runProgress(sessionID, runID, head.stage.Name, 0, 0, len(head.units))

	// feed hands one applied outcome's items to the stage after it.
	feed := func(j int, o outcome) {
		st := ps[j]
		copies := max(st.stage.Copies, 1)
		for _, v := range asStrings(o.data[st.field]) {
			key := itemKey(v)
			if st.seen[key] {
				st.sr.merged++
				continue
			}
			st.seen[key] = true
			if st.items >= maxFanout {
				continue
			}
			if report.launched+copies > maxRunAgents {
				st.sr.dropped++
				continue
			}
			st.items++
			for c := range copies {
				st.units = append(st.units, unit{stage: st.stage.Name, item: v, copy: c + 1})
				st.out = append(st.out, outcome{})
				st.done = append(st.done, false)
			}
			report.launched += copies
			st.sr.launched += copies
			if !st.started {
				st.started = true
				progress(st, "pipelined", nil)
			}
		}
	}

	type finished struct {
		j, k int
		o    outcome
	}
	doneCh := make(chan finished)
	running := 0
	stop := ""

	launchable := func() (int, int, bool) {
		for j, st := range ps {
			if st.queued < len(st.units) {
				return j, st.queued, true
			}
		}
		return 0, 0, false
	}

	for {
		for running < maxParallel && stop == "" && ctx.Err() == nil {
			j, k, ok := launchable()
			if !ok {
				break
			}
			ps[j].queued++
			running++
			st, u := ps[j], ps[j].units[k]
			go func() {
				doneCh <- finished{j: j, k: k, o: l.runUnit(ctx, sessionID, runID, p, st.stage, u, st.brief, st.allowed)}
			}()
		}
		if running == 0 {
			break
		}
		f := <-doneCh
		running--
		st := ps[f.j]
		st.out[f.k] = f.o
		st.done[f.k] = true
		st.ended++
		// Of the units this stage has so far: a pipelined stage's total
		// grows as the stage before it answers.
		l.runProgress(sessionID, runID, st.stage.Name, 0, st.ended, len(st.units))

		// Apply outcomes in launch order, and only then pass items on.
		for st.next < len(st.units) && st.done[st.next] {
			kept, why := applyStageOutcome(st.stage, 0, []outcome{st.out[st.next]}, &st.sr)
			st.next++
			if why != "" && stop == "" {
				stop = why
				cancel()
			}
			st.kept = append(st.kept, kept...)
			if stop == "" && f.j+1 < len(ps) {
				for _, o := range kept {
					feed(f.j+1, o)
				}
			}
		}
	}

	if stop == "" && ctx.Err() != nil {
		stop = "the run was cancelled or ran out of time in " + group[0].Name
	}
	for _, st := range ps {
		st.sr.kept = len(st.kept)
		results[st.stage.Name] = st.kept
		report.stages = append(report.stages, st.sr)
		if st.started {
			progress(st, "completed", map[string]any{"kept": len(st.kept)})
		}
	}
	return stop
}
