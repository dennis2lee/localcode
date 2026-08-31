package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"localcode/internal/config"
	"localcode/internal/tools"
)

// The Orchestrate tool: the plan is the input, and the schema is the
// language.
//
// Everything about the shape a plan may take is in this tool's input
// schema, which means the model is told the grammar by the same mechanism
// that enforces it, and a plan can be refused in full before anything runs.
// The agent enum is rendered per turn from the roster the turn was admitted
// with, the way the Task tool's already is: rendering it from the live
// setting would let a turn advertise one roster and accept another.

const orchestrateToolName = "Orchestrate"

// OrchestrateTool runs a validated plan of delegated stages.
type OrchestrateTool struct {
	loop *Loop
}

func NewOrchestrateTool(loop *Loop) *OrchestrateTool { return &OrchestrateTool{loop: loop} }

func (*OrchestrateTool) Name() string { return orchestrateToolName }

func (t *OrchestrateTool) Description() string { return t.DescriptionFor(context.Background()) }

func (t *OrchestrateTool) DescriptionFor(ctx context.Context) string {
	return fmt.Sprintf(`Run a fixed plan of delegated work: several agents at once, in stages, with what each stage keeps decided by code rather than by a model remembering to.

Use it when the shape of the work is known in advance and the value is in actually doing all of it: reviewing one change along several independent dimensions, checking each finding with agents that cannot see each other, surveying a subsystem several ways at once. For one question, use Task; a plan is not cheaper than a single delegation, it is more thorough than a model choosing step by step.

A stage is one of three kinds. "step" runs its agent once. "fanout" runs it once per item in over, times copies, all at once, where over is either a list you write or one reference of the form $earlierstage.field. "barrier" runs once and is handed everything the stages before it kept.

A stage that declares returns gets an Answer tool in that exact shape, and only what it declares can be referred to or filtered on later. keep names one returned field and drops every result where it is false or empty: a stage of skeptics declaring {"survives":"bool"} and keeping "survives" is an adversarial filter with no expression language involved. unanswered says what to do when an agent does not answer in the declared shape: skip (the default), keep, or fail.

Prompts take three substitutions and no others: {{task}}, {{item}} inside a fanout, and {{input}} for what earlier stages kept.

Ceilings, all refusals at validation rather than truncations while running: %d stages, %d items per fanout, %d copies, %d agents in a whole run, %d declared fields per stage. A stage gets %s and the run gets %s. Everything runs while you wait, and Esc stops all of it.`,
		maxStages, maxFanout, maxCopies, maxRunAgents, maxReturnFields,
		stageTimeout, runTimeout)
}

func (t *OrchestrateTool) InputSchema() json.RawMessage {
	return t.InputSchemaFor(context.Background())
}

func (t *OrchestrateTool) InputSchemaFor(ctx context.Context) json.RawMessage {
	agents, _ := json.Marshal(t.agentNames(ctx))
	kinds, _ := json.Marshal([]string{"step", "fanout", "barrier"})
	roles, _ := json.Marshal([]string{"readonly", "builder", "runner"})
	types, _ := json.Marshal([]string{"string", "bool", "number", "strings"})
	unans, _ := json.Marshal([]string{"skip", "keep", "fail"})

	stage := fmt.Sprintf(`{
"type":"object",
"properties":{
 "name":{"type":"string","description":"lowercase, no spaces; how later stages refer to this one"},
 "kind":{"type":"string","enum":%s},
 "agent":{"type":"string","enum":%s},
 "role":{"type":"string","enum":%s,"description":"tool allowlist; defaults to readonly"},
 "prompt":{"type":"string","description":"stands on its own: the agent cannot see this conversation. {{task}}, {{item}}, {{input}}"},
 "over":{"type":"array","items":{"type":"string"},"description":"fanout only: the items, or one entry of the form $stage.field"},
 "copies":{"type":"integer","description":"fanout only: independent agents per item, default 1"},
 "returns":{"type":"object","additionalProperties":{"type":"string","enum":%s},"description":"field name to type; what this stage must answer with"},
 "keep":{"type":"string","description":"one returned field; results where it is false or empty are dropped"},
 "unanswered":{"type":"string","enum":%s}
},
"required":["name","kind","agent","prompt"]
}`, kinds, agents, roles, types, unans)

	return json.RawMessage(fmt.Sprintf(`{
"type":"object",
"properties":{
 "goal":{"type":"string","description":"what this run is for, in your own words; it reaches every agent"},
 "stages":{"type":"array","items":%s}
},
"required":["goal","stages"]
}`, stage))
}

func (t *OrchestrateTool) agentNames(ctx context.Context) []string {
	if t.loop == nil {
		return nil
	}
	var out []string
	for name := range t.loop.DelegatableAgents(ctx) {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// RequiresPermission is always true, and does not depend on the plan.
//
// The alternative was to parse the input here and ask only for a plan that
// validates, which reads as tidy and is wrong twice: this method has no
// context, so it cannot see the roster the turn was admitted with, and a
// permission question that sometimes does not appear is one nobody can
// reason about. A run spends more than any other single tool call in
// localcode. It asks.
func (*OrchestrateTool) RequiresPermission(json.RawMessage) bool { return true }

// Describe is the ceilings, not an estimate.
//
// Debate can print rounds*(1+reviewers) because its shape is fixed. A plan
// with a $stage.field fanout cannot be counted in advance at all: its width
// is however many findings the earlier stage returns. So the prompt shows
// what the run cannot exceed, and says which agents it may spend it on.
func (t *OrchestrateTool) Describe(input json.RawMessage) string {
	var p Plan
	if err := json.Unmarshal(input, &p); err != nil {
		return "run an orchestration plan (the plan does not parse and will be refused)"
	}
	agents := map[string]bool{}
	for _, s := range p.Stages {
		agents[s.Agent] = true
	}
	var names []string
	for a := range agents {
		names = append(names, a)
	}
	sort.Strings(names)

	return fmt.Sprintf("run an orchestration: %q, %d stages, at most %d agent turns across %s, %s per stage and %s for the run",
		firstLine(p.Goal), len(p.Stages), p.Launches(), strings.Join(names, ", "), stageTimeout, runTimeout)
}

func (t *OrchestrateTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	if t.loop == nil || t.loop.Tasks == nil {
		return tools.Result{Content: "this build cannot delegate, so there is nothing to orchestrate", IsError: true}
	}
	if reason := t.refusal(ctx); reason != "" {
		return tools.Result{Content: reason, IsError: true, Refused: true}
	}

	var p Plan
	dec := json.NewDecoder(strings.NewReader(string(input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return tools.Result{
			Content: fmt.Sprintf("the plan does not parse: %v. Nothing ran.", err),
			IsError: true,
		}
	}
	if err := p.Validate(Limits{Agents: t.agentNames(ctx)}); err != nil {
		return tools.Result{
			Content: fmt.Sprintf("the plan was refused before anything ran: %v", err),
			IsError: true,
		}
	}

	sessionID, _ := SessionIDFromContext(ctx)
	report := t.loop.runPlan(ctx, sessionID, p)
	return tools.Result{Content: report.String()}
}

// refusal is why this call cannot proceed, or "".
func (t *OrchestrateTool) refusal(ctx context.Context) string {
	if !config.OrchestrateFor(ctx, t.loop.Config) {
		return "orchestration is off. Turn it on with \"/orchestrate on\", or in the settings window."
	}
	if inOrchestration(ctx) {
		return "a plan cannot run a plan: the ceiling on a run is a ceiling only if a stage cannot start another run."
	}
	if inDebate(ctx) {
		return "not inside a debate."
	}
	if len(t.loop.DelegatableAgents(ctx)) < 2 {
		return "there is only one agent configured, so there is nobody to delegate a stage to. Turn on Smart Agent for the built-in roster, or declare agents in config.json."
	}
	return ""
}

// String is the run report, composed here rather than summarised by a
// model. The one thing a run must not do is report a success nobody
// observed, and a summary of a summary is exactly how that happens.
func (r runReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Orchestration finished: %d agent turns across %d stages.\n\n", r.launched, len(r.stages))
	for _, s := range r.stages {
		fmt.Fprintf(&b, "## %s (%s, %s): %d launched, %d kept", s.name, s.kind, s.agent, s.launched, s.kept)
		if s.unanswered > 0 {
			fmt.Fprintf(&b, ", %d did not answer in the declared shape", s.unanswered)
		}
		if s.failed > 0 {
			fmt.Fprintf(&b, ", %d failed", s.failed)
		}
		if s.merged > 0 {
			fmt.Fprintf(&b, ", %d repeat(s) of an item merged", s.merged)
		}
		if s.dropped > 0 {
			fmt.Fprintf(&b, ", %d item(s) not run because the run's %d-agent ceiling was reached", s.dropped, maxRunAgents)
		}
		b.WriteString("\n")
		for _, o := range s.answers {
			label := o.agent
			if o.item != "" {
				label += " on " + firstLine(o.item)
			}
			if o.copy > 1 {
				label += fmt.Sprintf(" (copy %d)", o.copy)
			}
			switch {
			case o.err != nil:
				fmt.Fprintf(&b, "* %s failed: %v\n", label, o.err)
			case o.data != nil:
				enc, _ := json.Marshal(o.data)
				fmt.Fprintf(&b, "* %s: %s\n", label, enc)
			default:
				fmt.Fprintf(&b, "* %s: %s\n", label, strings.TrimSpace(o.text))
			}
		}
		b.WriteString("\n")
	}
	if r.stopped != "" {
		fmt.Fprintf(&b, "The run did not finish: %s\n", r.stopped)
	}
	return b.String()
}
