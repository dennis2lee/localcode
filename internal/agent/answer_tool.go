package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"localcode/internal/events"
	"localcode/internal/tools"
)

// Answering in a shape the plan can branch on.
//
// A sub-agent returns free text. That is the right default and it is why
// the delegation tools work at all: the parent reads a paragraph instead of
// a transcript. It is also why nothing can be built on top of a fan-out,
// because "did this finding survive" answered in prose is a question the
// next stage cannot act on without another model call to interpret it.
//
// So a stage that declares what it wants back is given a tool that takes
// exactly that. The shape is the plan author's, rendered per stage; the
// mechanism is the one the debate verdict already uses, generalised: the
// value is recovered by re-reading the child's own event log for the call,
// because a tool result travels to the model that called it and there is no
// channel from a child's tool call back to its parent.
//
// Not required. A stage whose agent never calls it is "unanswered", which
// is a first-class outcome the plan decides what to do with: a skeptic that
// did not answer must neither kill a finding nor save one.

const answerToolName = "Answer"

// stageAnswerKey carries the stage a turn is running for, so the Answer
// tool can render that stage's declared schema.
type stageAnswerKey struct{}

func withStageAnswer(ctx context.Context, s Stage) context.Context {
	if len(s.Returns) == 0 {
		return ctx
	}
	return context.WithValue(ctx, stageAnswerKey{}, s)
}

func stageAnswerFor(ctx context.Context) (Stage, bool) {
	s, ok := ctx.Value(stageAnswerKey{}).(Stage)
	return s, ok
}

// AnswerTool is offered only to a stage that declared a return shape.
type AnswerTool struct{}

func NewAnswerTool() AnswerTool { return AnswerTool{} }

func (AnswerTool) Name() string { return answerToolName }

func (AnswerTool) Description() string {
	return "Report this stage's result in the shape the plan asked for. Available only inside an orchestration stage that declared one."
}

func (a AnswerTool) DescriptionFor(ctx context.Context) string {
	s, ok := stageAnswerFor(ctx)
	if !ok {
		return a.Description()
	}
	return fmt.Sprintf("Report your result for stage %q in the shape it declared: %s. "+
		"Call this once, when you have the answer. Everything else you say is for the person reading "+
		"the transcript; this is what the rest of the plan acts on, so a field you leave out is a "+
		"question the next stage cannot answer.", s.Name, describeReturns(s.Returns))
}

func (AnswerTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (a AnswerTool) InputSchemaFor(ctx context.Context) json.RawMessage {
	if s, ok := stageAnswerFor(ctx); ok {
		if schema := s.answerSchema(); schema != nil {
			return schema
		}
	}
	return a.InputSchema()
}

// RequiresPermission is false. It records a value; it touches nothing.
func (AnswerTool) RequiresPermission(json.RawMessage) bool { return false }

func (AnswerTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	s, ok := stageAnswerFor(ctx)
	if !ok {
		return tools.Result{
			Content: "Answer is only callable inside an orchestration stage that declared what it returns",
			IsError: true,
		}
	}
	var got map[string]any
	if err := json.Unmarshal(input, &got); err != nil {
		return tools.Result{Content: fmt.Sprintf("invalid answer: %v", err), IsError: true}
	}
	var missing []string
	for f := range s.Returns {
		if _, ok := got[f]; !ok {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		// Said, not swallowed. The agent still has its turn, so a clear
		// message here is the difference between a repaired answer and an
		// unanswered stage.
		return tools.Result{
			Content: fmt.Sprintf("the answer is missing %s. The stage declared %s; call Answer again with every field.",
				strings.Join(sorted(missing), ", "), describeReturns(s.Returns)),
			IsError: true,
		}
	}
	return tools.Result{Content: "recorded"}
}

func describeReturns(returns map[string]string) string {
	if len(returns) == 0 {
		return "nothing"
	}
	var parts []string
	for _, f := range sorted(keysOf(returns)) {
		parts = append(parts, fmt.Sprintf("%s (%s)", f, returns[f]))
	}
	return strings.Join(parts, ", ")
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// readAnswer recovers a stage's structured answer from the child session's
// own log: the last accepted Answer call.
//
// Out of band, the way readVerdict is, and for the same reason. A tool
// result goes to the model that called the tool; nothing carries it back to
// the parent. The event log is where both of them can see it.
//
// The last one, because an agent that got the shape wrong and was told so
// calls again, and the corrected call is the answer. Only calls whose
// result was not an error, so a rejected attempt is not mistaken for one.
func (l *Loop) readAnswer(childID string, stage Stage) map[string]any {
	if l.Store == nil {
		return nil
	}
	evs, err := l.Store.Events(childID, 0)
	if err != nil {
		return nil
	}
	var found map[string]any
	for _, ev := range evs {
		if ev.Type != events.TypeToolEnd {
			continue
		}
		if isErr, _ := ev.Data["is_error"].(bool); isErr {
			continue
		}
		raw, _ := ev.Data["input"].(string)
		if raw == "" {
			continue
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			continue
		}
		// The tool_end event does not name the tool, so the answer is
		// identified by its shape: every declared field present. That is
		// the same test Execute applied before accepting it, so nothing
		// else in the log can match it by accident unless it is an equally
		// valid answer.
		complete := true
		for f := range stage.Returns {
			if _, ok := got[f]; !ok {
				complete = false
				break
			}
		}
		if complete {
			found = got
		}
	}
	return found
}
