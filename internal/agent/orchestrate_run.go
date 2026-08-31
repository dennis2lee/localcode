package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"localcode/internal/events"
	"localcode/internal/smart"
	"localcode/internal/trace"
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

		items := l.stageItems(stage, results)
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
				name: stage.Name, kind: stage.Kind, agent: stage.Agent, dropped: dropped,
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

		sr := stageReport{name: stage.Name, kind: stage.Kind, agent: stage.Agent, launched: len(units), dropped: dropped}
		var kept []outcome
		for _, o := range got {
			switch {
			case o.err != nil:
				sr.failed++
			case len(stage.Returns) > 0 && o.data == nil:
				sr.unanswered++
				if stage.Unanswered == "fail" {
					report.stages = append(report.stages, sr)
					report.stopped = fmt.Sprintf("stopped in %s: an agent did not answer in the shape the stage declared, and the stage says unanswered: fail", stage.Name)
					return report
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
		sr.kept = len(kept)
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

// runStage launches one stage's units, at most maxParallel at a time, and
// returns their outcomes in launch order however they finished.
func (l *Loop) runStage(ctx context.Context, sessionID string, p Plan, stage Stage, units []unit, carried string) []outcome {
	out := make([]outcome, len(units))
	allowed := l.toolsForRole(ctx, stage.Agent, stage.Role)
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup

	for i, u := range units {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				out[i] = outcome{unit: u, agent: stage.Agent, err: ctx.Err()}
				return
			}
			out[i] = l.runUnit(ctx, sessionID, p, stage, u, carried, allowed)
		}()
	}
	wg.Wait()
	return out
}

// runUnit is one agent turn: its own deadline, its own child session, and
// its answer read back out of that child's log.
func (l *Loop) runUnit(ctx context.Context, sessionID string, p Plan, stage Stage, u unit, carried string, allowed []string) outcome {
	o := outcome{unit: u, agent: stage.Agent}
	if l.Tasks == nil {
		o.err = fmt.Errorf("this build has no task manager, so nothing can be delegated to")
		return o
	}

	ctx, cancel := context.WithTimeout(ctx, stageTimeout)
	defer cancel()

	prompt := stagePrompt(p, stage, u.item, carried)
	ctx = withStageAnswer(withReviewerTools(ctx, allowed), stage)

	started := time.Now()
	childID, text, err := l.Tasks.SpawnSyncInto(ctx, sessionID, "", stage.Agent, prompt)
	l.traceSpan(ctx, trace.ID(ctx), sessionID, trace.SpanDelegate, trace.Record{
		Agent:  stage.Agent,
		Detail: fmt.Sprintf("orchestrate %s/%s -> %s in %s", stage.Name, u.item, childID, time.Since(started).Round(time.Millisecond)),
	})
	o.text = text
	if err != nil {
		o.err = err
		return o
	}
	if len(stage.Returns) > 0 && childID != "" {
		o.data = l.readAnswer(childID, stage)
	}
	return o
}

// stagePrompt is the three substitutions and nothing else.
func stagePrompt(p Plan, stage Stage, item, carried string) string {
	r := strings.NewReplacer(
		"{{task}}", p.Goal,
		"{{item}}", item,
		"{{input}}", carried,
	)
	body := r.Replace(stage.Prompt)
	// The goal travels even when the prompt did not ask for it: a
	// sub-agent cannot see this conversation, and a stage prompt written
	// without {{task}} is one whose author forgot that rather than one who
	// meant the agent to work blind.
	if !strings.Contains(stage.Prompt, "{{task}}") {
		body = "The run's goal: " + p.Goal + "\n\n" + body
	}
	return body
}

// stageItems is what a fanout spreads over: the literal list, or the values
// of an earlier stage's kept results.
func (l *Loop) stageItems(stage Stage, results map[string][]outcome) []string {
	if stage.Kind != "fanout" {
		return []string{""}
	}
	ref, isRef := planRef(stage.Over)
	if !isRef {
		return stage.Over
	}
	from, field, ok := splitRef(ref)
	if !ok {
		return nil
	}
	var items []string
	for _, o := range results[from] {
		for _, v := range asStrings(o.data[field]) {
			items = append(items, v)
			if len(items) >= maxFanout {
				return items
			}
		}
	}
	return items
}

// carriedInput is what {{input}} expands to: every kept result so far,
// labelled by the stage that produced it.
//
// Composed by localcode rather than by a model, and capped, because it is
// the one part of a stage's prompt whose size nobody chose.
func (l *Loop) carriedInput(p Plan, stage Stage, results map[string][]outcome) string {
	if !strings.Contains(stage.Prompt, "{{input}}") && stage.Kind != "barrier" {
		return ""
	}
	var b strings.Builder
	for _, s := range p.Stages {
		if s.Name == stage.Name {
			break
		}
		kept := results[s.Name]
		if len(kept) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s (%s, %d kept)\n", s.Name, s.Agent, len(kept))
		for _, o := range kept {
			if o.data != nil {
				enc, _ := json.Marshal(o.data)
				b.Write(enc)
				b.WriteByte('\n')
				continue
			}
			b.WriteString(strings.TrimSpace(o.text))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return truncateMiddle(b.String(), carriedInputLimit,
		"earlier stages produced more than fits in one prompt")
}

// carriedInputLimit bounds what one stage is handed from the stages before
// it. Thirty-two thousand characters is roughly eight thousand tokens: a
// large brief, and a long way short of a window.
const carriedInputLimit = 32000

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return strings.TrimSpace(t) != ""
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	default:
		return true
	}
}

func asStrings(v any) []string {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
