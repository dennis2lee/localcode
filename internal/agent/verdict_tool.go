package agent

import (
	"context"
	"encoding/json"
	"strings"

	"localcode/internal/tools"
)

// The reviewer's answer, as a boolean rather than a sentence.
//
// A debate ends early when the reviewer approves, so "did it approve" is
// a question localcode has to answer on every round, and answering it by
// reading prose is the thing this exists to avoid: the reply may be in
// Korean or English, may be three paragraphs, and may contain the word
// "approved" inside the sentence explaining what would have to change
// before it could be. A tool call is a field the model set on purpose.
//
// The prose fallback still exists (see approvedByLastLine) because a
// small local model may never call a tool it has not seen before. It is
// strict, and both paths default to "not approved", because a debate that
// ends a round early on a misread stops looking at work that nobody
// approved while the transcript says somebody did.

const verdictToolName = "Verdict"

type verdictArgs struct {
	Approved bool   `json:"approved"`
	Findings string `json:"findings"`
}

// VerdictTool is offered only to a reviewer inside a debate. It is hidden
// from every other turn (see hiddenTools) and refuses to run there, so
// there is no turn in which a model can declare its own work approved.
type VerdictTool struct{}

func (VerdictTool) Name() string { return verdictToolName }

func (VerdictTool) Description() string {
	return "Report the result of your review. Call this once, at the end. Set approved only if the work " +
		"is correct and complete as it stands and you would ship it; if anything should change, approved " +
		"is false and `findings` says what and why, specifically enough to act on. `findings` is the whole " +
		"of what the author will be shown — put your review in it, not in a message afterwards."
}

func (VerdictTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"approved":{"type":"boolean","description":"true only if the work is right as it stands"},` +
		`"findings":{"type":"string","description":"what is wrong and why, or what you checked if approving"}},` +
		`"required":["approved"]}`)
}

// RequiresPermission is false. Saying what you think of somebody else's
// code changes nothing on disk, and a prompt on every round of a debate
// the person started is a prompt they would learn to click through.
func (VerdictTool) RequiresPermission(json.RawMessage) bool { return false }

func (VerdictTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	if _, reviewing := reviewerTools(ctx); !reviewing {
		return tools.Result{
			Content: "Verdict can only be called by a reviewer inside a debate",
			IsError: true, Refused: true,
		}
	}
	var args verdictArgs
	if err := json.Unmarshal(input, &args); err != nil {
		return tools.Result{Content: "invalid input: " + err.Error(), IsError: true}
	}
	// The debate reads the findings out of this call and frames them
	// itself; what goes back here is only the confirmation, so a model
	// that would otherwise carry on reviewing after it has reported
	// stops. It says the findings are what travels, because that is true
	// and because a reviewer that saves its argument for a closing
	// message would have the argument dropped.
	if args.Approved {
		return tools.Result{Content: "Recorded: approved. The debate ends here; nothing further is needed from you."}
	}
	if strings.TrimSpace(args.Findings) == "" {
		return tools.Result{
			Content: "Recorded as not approved, but with no findings. Say what should change — " +
				"call Verdict again with `findings` filled in.",
		}
	}
	return tools.Result{Content: "Recorded: changes requested. The author will be given your findings."}
}
