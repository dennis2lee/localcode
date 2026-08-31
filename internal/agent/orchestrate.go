package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Orchestration: a plan the model fills in, and a Go loop that runs it.
//
// The Task tool already lets a model delegate. What it cannot do is commit
// to a shape in advance. Every fan-out is the model deciding, one step at a
// time, whether to delegate again, and the two failure modes are both
// common: a model that says it will check each finding with three
// independent reviewers checks the first two and reports; a model told to
// repeat until nothing new turns up stops at the third round. Neither is
// dishonesty. A stated procedure is a request, and the machinery this
// repository already leans on for the same problem is the other one: a tool
// allowlist rather than a sentence, a Go loop rather than an instruction.
// runDebate is that argument already made once, for one fixed protocol.
//
// So the orchestration program is data, not prose and not a script. The
// model authors it by filling in this tool's input schema, which means the
// whole of it can be checked before a single token is spent: every agent
// name against the roster this turn was admitted with, every reference
// against a stage that really precedes it, every count against a ceiling.
// A plan that would not work is refused with the reason, and nothing runs.
//
// The alternative considered and rejected was embedding a scripting
// language. It buys expressiveness this needs none of: the control forms
// these patterns want are a closed set, and a closed set can be validated,
// previewed and priced, which a program cannot. This is also a repository
// that has three times reimplemented something small rather than take a
// dependency, and "models write Python well" is not a strong enough case
// against that.

// Ceilings. Every one of these is in the tool description, so a plan is
// written against the same numbers it is judged by, and every one of them
// is a refusal at validation rather than a truncation at run time.
const (
	// maxStages bounds a plan's length. Past this it is a program, and a
	// program should be several turns with a person in between.
	maxStages = 8
	// maxFanout bounds one stage's item list.
	maxFanout = 16
	// maxCopies bounds independent agents per item: three skeptics on one
	// finding is the pattern this exists for, eight is nobody's.
	maxCopies = 8
	// maxRunAgents bounds the whole run. The product of the two above
	// would be 128, which is not a number anybody meant to spend.
	maxRunAgents = 32
	// maxReturnFields bounds a stage's declared answer. Flat and small is
	// what a model reliably fills in; anything richer is filled correctly
	// only by the models that did not need the schema.
	maxReturnFields = 5
)

// Plan is one orchestration: what it is for, and the stages in order.
type Plan struct {
	// Goal is what the run is trying to achieve, in the author's own
	// words. It is not interpreted, it is carried into every stage's
	// brief, because a sub-agent cannot see this conversation.
	Goal string `json:"goal"`

	// Stages run in order. Every stage is a barrier for the one after it:
	// pipelining an item through the remaining stages on its own buys wall
	// clock and costs a per-item state machine, and it is deliberately not
	// in this version. See docs/USAGE.md.
	Stages []Stage `json:"stages"`
}

// Stage is one step of a plan.
type Stage struct {
	// Name identifies the stage and is how a later stage refers to its
	// results. Unique within a plan, lowercase, no spaces.
	Name string `json:"name"`

	// Kind is "step", "fanout" or "barrier".
	//
	//   step    one agent, once.
	//   fanout  one agent per item of Over, times Copies, all at once.
	//   barrier one agent, once, given every kept result so far.
	//
	// The difference between step and barrier is only what the prompt is
	// given, and they are separate words because the plan reads as what it
	// does: a barrier is where the run comes back together.
	Kind string `json:"kind"`

	// Agent is which delegatable agent runs this stage. Validated against
	// the roster the turn was admitted with, not the live one.
	Agent string `json:"agent"`

	// Role is the tool allowlist: "readonly", "builder" or "runner".
	// Empty means readonly, because a stage that did not say is one whose
	// author did not think about it.
	Role string `json:"role,omitempty"`

	// Prompt is what the agent is asked. Three substitutions and no
	// others: {{task}} the run's goal, {{item}} this item of a fanout,
	// {{input}} the results kept by earlier stages.
	//
	// Deliberately not the custom-command expansions. A plan is written by
	// a model, and "!`shell`" in a model-authored document is arbitrary
	// execution in a daemon that gates bash carefully.
	Prompt string `json:"prompt"`

	// Over is a fanout's item list: either literal strings, or one
	// reference of the form "$stage.field" naming an earlier stage and one
	// of its declared return fields.
	Over []string `json:"over,omitempty"`

	// Copies is how many independent agents run per item. 1 by default.
	// More than one is the adversarial pattern: several agents that cannot
	// see each other answering the same question.
	Copies int `json:"copies,omitempty"`

	// Returns declares the answer's shape: field name to type, one of
	// "string", "bool", "number", "strings". A stage with no Returns is
	// answered in prose, which is fine for a final report and useless to a
	// later stage that wants to fan out over what it found.
	Returns map[string]string `json:"returns,omitempty"`

	// Keep decides which of this stage's results are carried forward: a
	// field name that must be true (for a bool) or non-empty. Empty keeps
	// everything. This is the quorum mechanism: a skeptic stage declaring
	// {"survives":"bool"} and keeping "survives" is exactly the
	// adversarial filter, with no expression language anywhere.
	Keep string `json:"keep,omitempty"`

	// Unanswered decides what to do with a result whose declared fields
	// did not come back: "skip" drops it, "keep" carries it, "fail" ends
	// the run. Empty means skip.
	//
	// A first-class outcome rather than a parse failure, because a skeptic
	// that did not answer must neither kill a finding nor save one, and
	// whichever is chosen has to be the plan author's choice and has to be
	// named in the report.
	Unanswered string `json:"unanswered,omitempty"`
}

var (
	stageKinds  = map[string]bool{"step": true, "fanout": true, "barrier": true}
	stageRoles  = map[string]bool{"readonly": true, "builder": true, "runner": true}
	returnTypes = map[string]bool{"string": true, "bool": true, "number": true, "strings": true}
	unanswered  = map[string]bool{"skip": true, "keep": true, "fail": true}
)

// Limits is what a plan is validated against: the roster this turn may
// delegate to, and nothing else. Passed in rather than read, because the
// roster depends on the Smart Agent setting the turn was admitted with and
// a validator that re-read it would accept a plan the runner then refuses.
type Limits struct {
	Agents []string
}

// Validate reports every reason this plan cannot run, or nil.
//
// Every error names the stage, because a plan is authored by a model that
// will be asked to fix it and "invalid plan" is the tool result that
// produces another invalid plan. This is the same argument as the edit
// tool's failure diagnosis, one layer up.
func (p Plan) Validate(l Limits) error {
	if strings.TrimSpace(p.Goal) == "" {
		return fmt.Errorf("the plan has no goal: say what the run is for, since no sub-agent can see this conversation")
	}
	if len(p.Stages) == 0 {
		return fmt.Errorf("the plan has no stages")
	}
	if len(p.Stages) > maxStages {
		return fmt.Errorf("the plan has %d stages; the limit is %d", len(p.Stages), maxStages)
	}

	known := map[string]Stage{}
	agents := map[string]bool{}
	for _, a := range l.Agents {
		agents[a] = true
	}

	launches := 0
	for i, s := range p.Stages {
		where := fmt.Sprintf("stage %d", i+1)
		if s.Name != "" {
			where = fmt.Sprintf("stage %q", s.Name)
		}

		if s.Name == "" {
			return fmt.Errorf("%s has no name", where)
		}
		if s.Name != strings.ToLower(s.Name) || strings.ContainsAny(s.Name, " \t.$") {
			return fmt.Errorf("%s: a stage name is lowercase with no spaces, dots or dollars", where)
		}
		if _, dup := known[s.Name]; dup {
			return fmt.Errorf("%s: two stages have that name, and a later stage refers to results by name", where)
		}
		if !stageKinds[s.Kind] {
			return fmt.Errorf("%s: kind is %q, and must be one of step, fanout, barrier", where, s.Kind)
		}
		if s.Role != "" && !stageRoles[s.Role] {
			return fmt.Errorf("%s: role is %q, and must be one of readonly, builder, runner", where, s.Role)
		}
		if !agents[s.Agent] {
			return fmt.Errorf("%s: there is no agent %q to delegate to. Available: %s",
				where, s.Agent, strings.Join(sorted(l.Agents), ", "))
		}
		if strings.TrimSpace(s.Prompt) == "" {
			return fmt.Errorf("%s: no prompt. The agent cannot see this conversation, so the prompt has to stand on its own", where)
		}
		if err := checkTemplate(where, s.Prompt, s.Kind == "fanout"); err != nil {
			return err
		}

		copies := s.Copies
		if copies == 0 {
			copies = 1
		}
		if copies < 1 || copies > maxCopies {
			return fmt.Errorf("%s: copies is %d, and must be between 1 and %d", where, s.Copies, maxCopies)
		}

		n := copies
		switch s.Kind {
		case "fanout":
			if len(s.Over) == 0 {
				return fmt.Errorf("%s: a fanout needs over: either a list of items, or one $stage.field naming an earlier stage's results", where)
			}
			if ref, isRef := planRef(s.Over); isRef {
				from, field, ok := splitRef(ref)
				if !ok {
					return fmt.Errorf("%s: over is %q; a reference is written $stage.field", where, ref)
				}
				prior, seen := known[from]
				if !seen {
					return fmt.Errorf("%s: over refers to stage %q, which does not run before this one", where, from)
				}
				if _, declared := prior.Returns[field]; !declared {
					return fmt.Errorf("%s: stage %q does not return a field called %q (it returns %s)",
						where, from, field, fieldList(prior.Returns))
				}
				if prior.Returns[field] != "strings" {
					return fmt.Errorf("%s: %s.%s is a %s; a fanout can only spread over a strings field",
						where, from, field, prior.Returns[field])
				}
				// A reference's width is not known until the run: it is
				// however many findings the stage before it returns. So
				// validation asks only whether the stage can run at all,
				// one item's worth, and the runner caps the real width to
				// what the run has left and says how many it dropped.
				//
				// Pricing it at maxFanout here instead would be tidier and
				// would refuse the plan this feature exists for: sixteen
				// findings times two skeptics is already past any ceiling
				// worth having, so no plan with a reference and copies
				// could ever validate.
			} else {
				if len(s.Over) > maxFanout {
					return fmt.Errorf("%s: over has %d items; the limit is %d", where, len(s.Over), maxFanout)
				}
				n *= len(s.Over)
			}
		default:
			if len(s.Over) > 0 {
				return fmt.Errorf("%s: over belongs to a fanout, and this stage is a %s", where, s.Kind)
			}
			if s.Copies > 1 {
				return fmt.Errorf("%s: copies belongs to a fanout, and this stage is a %s", where, s.Kind)
			}
		}
		launches += n

		if len(s.Returns) > maxReturnFields {
			return fmt.Errorf("%s: returns declares %d fields; the limit is %d, and flat and small is what a model reliably fills in",
				where, len(s.Returns), maxReturnFields)
		}
		for f, typ := range s.Returns {
			if f == "" || strings.ContainsAny(f, " \t.$") {
				return fmt.Errorf("%s: %q is not a usable field name", where, f)
			}
			if !returnTypes[typ] {
				return fmt.Errorf("%s: field %q is declared %q; the types are string, bool, number, strings", where, f, typ)
			}
		}
		if s.Keep != "" {
			if _, ok := s.Returns[s.Keep]; !ok {
				return fmt.Errorf("%s: keep names %q, which this stage does not return (it returns %s)",
					where, s.Keep, fieldList(s.Returns))
			}
		}
		if s.Unanswered != "" && !unanswered[s.Unanswered] {
			return fmt.Errorf("%s: unanswered is %q, and must be one of skip, keep, fail", where, s.Unanswered)
		}

		known[s.Name] = s
	}

	if launches > maxRunAgents {
		return fmt.Errorf("this plan would launch up to %d agents; the limit is %d. Narrow a fanout, or lower copies",
			launches, maxRunAgents)
	}
	return nil
}

// Launches is the worst case this plan can cost, in agent turns, and never
// more than the run ceiling.
//
// Used for the permission prompt, where a ceiling is the honest number:
// the real count depends on how many findings a stage returns, which
// nobody knows yet. The clamp matters as much as the estimate. Without it
// a plan with one reference fanout priced at 16 items times 2 copies asked
// the person to approve "up to 35 agent turns" in a runner that stops at
// 32, which is a number that cannot happen being used to get a yes.
func (p Plan) Launches() int {
	total := 0
	for _, s := range p.Stages {
		n := s.Copies
		if n == 0 {
			n = 1
		}
		if s.Kind == "fanout" {
			if _, isRef := planRef(s.Over); isRef {
				n *= maxFanout
			} else {
				n *= len(s.Over)
			}
		}
		total += n
	}
	return min(total, maxRunAgents)
}

// planRef reports whether a fanout's Over is a reference rather than a
// literal list. One entry beginning with "$" is a reference; anything else
// is items.
func planRef(over []string) (string, bool) {
	if len(over) == 1 && strings.HasPrefix(over[0], "$") {
		return over[0], true
	}
	return "", false
}

func splitRef(ref string) (stage, field string, ok bool) {
	rest := strings.TrimPrefix(ref, "$")
	i := strings.IndexByte(rest, '.')
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// checkTemplate refuses a prompt carrying anything but the three
// substitutions, so a plan cannot smuggle in an expansion this does not
// implement and cannot quietly interpolate nothing.
func checkTemplate(where, prompt string, fanout bool) error {
	rest := prompt
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			return nil
		}
		j := strings.Index(rest[i:], "}}")
		if j < 0 {
			return fmt.Errorf("%s: the prompt has an unclosed {{", where)
		}
		name := strings.TrimSpace(rest[i+2 : i+j])
		switch name {
		case "task", "input":
		case "item":
			if !fanout {
				return fmt.Errorf("%s: {{item}} is only meaningful in a fanout", where)
			}
		default:
			return fmt.Errorf("%s: {{%s}} is not a substitution. The three are {{task}}, {{item}} and {{input}}", where, name)
		}
		rest = rest[i+j+2:]
	}
}

func fieldList(returns map[string]string) string {
	if len(returns) == 0 {
		return "nothing"
	}
	names := make([]string, 0, len(returns))
	for f := range returns {
		names = append(names, f)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// answerSchema is the JSON schema a stage's Answer tool advertises, built
// from the stage's declared Returns. Nil when the stage returns prose.
func (s Stage) answerSchema() json.RawMessage {
	if len(s.Returns) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.Returns))
	for f := range s.Returns {
		names = append(names, f)
	}
	sort.Strings(names)

	var props []string
	for _, f := range names {
		body := `"type":"string"`
		switch s.Returns[f] {
		case "bool":
			body = `"type":"boolean"`
		case "number":
			body = `"type":"number"`
		case "strings":
			body = `"type":"array","items":{"type":"string"}`
		}
		props = append(props, fmt.Sprintf(`%q:{%s}`, f, body))
	}
	req, _ := json.Marshal(names)
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s},"required":%s}`,
		strings.Join(props, ","), req))
}
