package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"localcode/internal/tools"
)

// Starting a debate from an ordinary sentence.
//
// "1부터 10까지 더하는 파이선 프로그램을 만들어라. 완료되면 @girl은 그 결과물을
// 검토해라. @boy는 그 검토 결과를 확인하고 필요 수정을 해라. 이렇게 10번 반복해라."
// is what somebody actually types, and until this existed all of it went
// to one model, which read the protocol as part of the task and tried to
// run its own loop.
//
// So the model gets a tool, and what it does with it is narrow. The
// division of labour is the same one scheduling ended up with:
//
//   - The model reads the sentence and separates the three things in it:
//     who reviews, how many times, and the work. That is language, and it
//     is what a model is for.
//   - localcode runs the loop. Not the model. The count, the alternation,
//     the stopping condition and the reviewer's tools are all decided
//     here, because a model asked to run its own loop stops early and
//     reports that it is finished.
//   - The person confirms, and the question names what localcode read
//     rather than the words that were passed to it.
//
// The one thing the tool must get right, and the reason its description
// says so twice: `task` is the work alone. A task that still contains
// "review it ten times" gives the author a loop inside the loop that is
// already running it.

const debateToolName = "Debate"

// DebateTool books a debate to start when this turn ends.
type DebateTool struct {
	loop *Loop
}

func NewDebateTool(loop *Loop) DebateTool { return DebateTool{loop: loop} }

func (DebateTool) Name() string { return debateToolName }

func (t DebateTool) Description() string { return t.DescriptionFor(context.Background()) }

func (t DebateTool) DescriptionFor(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("Have another agent review your work, round after round, when the user asks for that " +
		"(\"@girl이 검토해라\", \"have the review agent check it, then fix it, 5 times\", \"get a second " +
		"opinion and iterate\"). You do the work; the reviewer reads it and says what is wrong; you answer " +
		"it; localcode counts the rounds and stops when the reviewer approves.\n" +
		"Put ONLY the work in `task`. Leave out who reviews, how many times, and when to stop — those are " +
		"this tool's arguments, and repeating them in `task` makes you run the loop a second time.\n" +
		"Call this instead of doing the work, then end your turn: the first round is where the work happens.\n" +
		"Available reviewers:\n")
	writeAgentList(&b, t.loop.DelegatableAgents(ctx))
	return b.String()
}

func (t DebateTool) InputSchema() json.RawMessage { return t.InputSchemaFor(context.Background()) }

func (t DebateTool) InputSchemaFor(ctx context.Context) json.RawMessage {
	names, _ := json.Marshal(agentNamesOf(t.loop.DelegatableAgents(ctx)))
	return json.RawMessage(fmt.Sprintf(
		`{"type":"object","properties":{`+
			`"reviewers":{"type":"array","items":{"type":"string","enum":%s},"description":"the agent or agents that review, at most %d; they review independently and all must approve"},`+
			`"rounds":{"type":"integer","minimum":1,"maximum":%d,"description":"how many times to go round, if the user said; %d if they did not"},`+
			`"task":{"type":"string","description":"the work only, in the user's own terms — not who reviews it, not how many rounds, not when to stop"}},`+
			`"required":["reviewers","task"]}`,
		names, debateMaxReviewers, debateMaxRounds, debateDefaultRounds))
}

// RequiresPermission is true, and this is the confirmation step.
//
// A debate is a commitment of a size nothing else the model can ask for
// comes close to: rounds x (1 + reviewers) model turns, each with tools
// of its own. The prompt names the count and the reading of the task, so
// a protocol sentence that leaked into the task is visible before the
// turns are spent rather than after.
func (DebateTool) RequiresPermission(json.RawMessage) bool { return true }

func (t DebateTool) Subject(input json.RawMessage) string {
	args := t.parse(input)
	return strings.Join(args.Reviewers, ",") + ": " + promptSummary(args.Task)
}

func (t DebateTool) Describe(input json.RawMessage) string {
	args := t.parse(input)
	return fmt.Sprintf("debate: %s writes, %s reviews, %s (~%d model turns)\n  task: %s",
		"this conversation's agent", strings.Join(args.Reviewers, " and "),
		roundCount(args.Rounds), args.Rounds*(1+len(args.Reviewers)), promptSummary(args.Task))
}

type debateArgs struct {
	Reviewers []string `json:"reviewers"`
	Rounds    int      `json:"rounds"`
	Task      string   `json:"task"`
}

func (t DebateTool) parse(input json.RawMessage) debateArgs {
	var args debateArgs
	_ = json.Unmarshal(input, &args)
	args.Task = strings.TrimSpace(args.Task)
	var clean []string
	for _, name := range args.Reviewers {
		if name = strings.TrimSpace(name); name != "" {
			clean = append(clean, name)
		}
	}
	args.Reviewers = clean
	if args.Rounds <= 0 {
		args.Rounds = debateDefaultRounds
	}
	if args.Rounds > debateMaxRounds {
		args.Rounds = debateMaxRounds
	}
	return args
}

// Execute records the debate; it does not run it.
//
// It cannot run it. This call is inside a turn of the very session the
// debate has to drive, and a debate runs turns of its own there — a
// second one, nested inside the first, would be two writers appending to
// one conversation's history at once. So the run is left for SendMessage
// to pick up once this turn has finished, and the tool's answer tells the
// model to stop rather than carry on doing the work it has just asked to
// have reviewed.
func (t DebateTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	sessionID, ok := SessionIDFromContext(ctx)
	if !ok {
		return tools.Result{Content: "Debate has no session context", IsError: true}
	}
	args := t.parse(input)
	if args.Task == "" {
		return tools.Result{Content: "say what the work is, in `task`", IsError: true}
	}
	if len(args.Reviewers) == 0 {
		return tools.Result{Content: "name at least one reviewer", IsError: true}
	}
	if len(args.Reviewers) > debateMaxReviewers {
		return tools.Result{Content: fmt.Sprintf(
			"%d reviewers is more than the %d this allows", len(args.Reviewers), debateMaxReviewers), IsError: true}
	}

	author := t.loop.sessionAgent(sessionID)
	if reason, ok := t.loop.debateRefusal(ctx, sessionID, author, t.loop.DelegatableAgents(ctx), args.Reviewers); !ok {
		return tools.Result{Content: reason, IsError: true, Refused: true}
	}

	t.loop.setPendingDebate(debateRun{
		sessionID: sessionID,
		author:    author,
		reviewers: args.Reviewers,
		rounds:    args.Rounds,
		// The command line the person did not type. It stands in the
		// transcript where their own words would be for a "/debate", so
		// the record says what was started and on whose reading.
		command: fmt.Sprintf("/debate %s %d %s", strings.Join(args.Reviewers, ","), args.Rounds, args.Task),
		task:    args.Task,
	})

	return tools.Result{Content: fmt.Sprintf(
		"Booked: %s will review, over %s. It starts the moment this turn ends.\n"+
			"End your turn now, without doing the work and without saying you have done it — "+
			"round one is where the work happens, and you are the one who will do it there.",
		strings.Join(args.Reviewers, " and "), roundCount(args.Rounds))}
}
