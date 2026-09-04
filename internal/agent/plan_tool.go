package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"localcode/internal/events"
	"localcode/internal/tools"
)

// A checklist the model keeps, and the person can watch.
//
// The habit this answers is the one keep_going already compensates for
// from the other side. A model given a task with five steps in it does
// two, writes a paragraph about the other three, and ends its turn.
// keep_going tells it to carry on, which works and is blind: neither the
// model nor the person can see how many steps there were or which one is
// current, so "carry on" is a nudge into fog.
//
// A plan makes the fog into a list. The model writes the steps once,
// marks one in progress, and marks it done before the next. That is worth
// something on its own — a model that has written "3. wire the loader"
// has a next action to return to after a tool result knocks it off course
// — and it is worth more to the person, who can see the shape of the work
// before it happens rather than after.
//
// Deliberately not the Orchestrate plan. That one is stages of delegated
// sub-agents, run by a Go loop, and it is a different question: who does
// the work. This is one model's own notes about work it is doing itself,
// and the descriptions say so, because a model holding both tools and one
// vague word for them will reach for the expensive one.
//
// Behind Smart Agent, like the rest of the bundle: a checklist is a way
// of working, and a way of working is something to opt into.
const updatePlanToolName = "update_plan"

// planStatus is the vocabulary, fixed. Three words with an order to them:
// nothing goes from pending to completed without being in_progress in
// between, which is what makes the list a record of what happened rather
// than a form filled in at the end.
const (
	planPending    = "pending"
	planInProgress = "in_progress"
	planCompleted  = "completed"
)

type planStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

// UpdatePlanTool records this turn's checklist.
type UpdatePlanTool struct {
	loop *Loop
}

func NewUpdatePlanTool(loop *Loop) UpdatePlanTool { return UpdatePlanTool{loop: loop} }

func (UpdatePlanTool) Name() string { return updatePlanToolName }

func (t UpdatePlanTool) Description() string { return t.DescriptionFor(context.Background()) }

func (UpdatePlanTool) DescriptionFor(context.Context) string {
	return "Write or update the checklist for the work you are doing yourself, so the user can see " +
		"the shape of it and you have a next action to return to. Steps are short (under about ten " +
		"words) and each carries a status: pending, in_progress, or completed.\n" +
		"Rules that make the list mean something:\n" +
		"* Exactly one step is in_progress at a time.\n" +
		"* Mark a step completed before starting the next one. Never move a step straight from " +
		"pending to completed, and never mark several completed at the end.\n" +
		"* Send the whole list every time, not just what changed.\n" +
		"* If the work turns out differently, call this again with the new list and say why in " +
		"`explanation`.\n" +
		"Skip it for straightforward work: anything you can finish in a step or two does not need a " +
		"plan, and a plan padded with obvious steps is worse than none.\n" +
		"This is your own checklist. To hand work to other agents instead, use Task or Orchestrate."
}

func (UpdatePlanTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"explanation":{"type":"string","description":"Why the plan changed. Only needed when replacing an existing plan."},` +
		`"plan":{"type":"array","description":"The whole checklist, in order.","items":{"type":"object","properties":{` +
		`"step":{"type":"string","description":"What is being done, in a few words."},` +
		`"status":{"type":"string","enum":["pending","in_progress","completed"],"description":"Where this step stands."}` +
		`},"required":["step","status"],"additionalProperties":false}}` +
		`},"required":["plan"],"additionalProperties":false}`)
}

func (t UpdatePlanTool) InputSchemaFor(context.Context) json.RawMessage { return t.InputSchema() }

// RequiresPermission is false: the tool writes a note into the transcript
// and touches nothing else. Asking to approve a checklist would train the
// person to approve without reading, which is the cost that matters.
func (UpdatePlanTool) RequiresPermission(json.RawMessage) bool { return false }

func (t UpdatePlanTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	var args struct {
		Explanation string     `json:"explanation"`
		Plan        []planStep `json:"plan"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tools.Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}
	if why := validatePlan(args.Plan); why != "" {
		return tools.Result{Content: why, IsError: true}
	}

	sessionID, ok := SessionIDFromContext(ctx)
	if !ok || t.loop == nil || t.loop.Store == nil {
		return tools.Result{Content: "no conversation to record a plan in", IsError: true}
	}

	steps := make([]any, 0, len(args.Plan))
	for _, s := range args.Plan {
		steps = append(steps, map[string]any{"step": s.Step, "status": s.Status})
	}
	data := map[string]any{"plan": steps}
	if args.Explanation != "" {
		data["explanation"] = args.Explanation
	}
	t.loop.Store.Append(sessionID, events.TypePlanUpdated, data)

	return tools.Result{Content: planSummary(args.Plan)}
}

// validatePlan refuses the shapes that make a plan misleading, and says
// which one it is.
//
// Refused rather than silently corrected. A plan with two steps in
// progress is a model that has lost track of which one it is on, and
// quietly picking one for it hides exactly the thing worth knowing.
func validatePlan(plan []planStep) string {
	if len(plan) == 0 {
		return "a plan needs at least one step; call this with the whole list, or do not call it at all"
	}
	if len(plan) == 1 {
		return "a one-step plan is not a plan: either the work has more steps than that, or it does " +
			"not need this tool. Do the work instead."
	}
	inProgress := 0
	for i, s := range plan {
		if strings.TrimSpace(s.Step) == "" {
			return fmt.Sprintf("step %d has no text", i+1)
		}
		switch s.Status {
		case planPending, planCompleted:
		case planInProgress:
			inProgress++
		default:
			return fmt.Sprintf("step %d has status %q; it must be one of pending, in_progress, completed",
				i+1, s.Status)
		}
	}
	if inProgress > 1 {
		return fmt.Sprintf("%d steps are in_progress; exactly one step is in progress at a time, so "+
			"mark the one you finished completed before starting the next", inProgress)
	}
	return ""
}

// planSummary is what the model gets back: the list as it now stands, so
// the next turn reads its own plan out of the tool result rather than
// reconstructing it from what it sent.
func planSummary(plan []planStep) string {
	var b strings.Builder
	done := 0
	for _, s := range plan {
		mark := " "
		switch s.Status {
		case planCompleted:
			mark = "x"
			done++
		case planInProgress:
			mark = ">"
		}
		fmt.Fprintf(&b, "[%s] %s\n", mark, s.Step)
	}
	fmt.Fprintf(&b, "%d of %d done", done, len(plan))
	return b.String()
}
