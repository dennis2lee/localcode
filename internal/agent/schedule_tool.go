package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"localcode/internal/tools"
	"localcode/internal/when"
)

// Booking work from an ordinary sentence.
//
// "/schedule 내일 아침 테스트 돌려줘" works and nobody types it. What
// people say is "내일 아침에 테스트 돌려줘", and until this existed that
// went straight to the model, which tried to run the tests immediately —
// the one reading of the sentence that is certainly wrong.
//
// So the model gets a tool. What it does with it is narrow and worth
// stating, because the division of labour here is the whole design:
//
//   - The model decides this is a request for later, and separates the
//     time from the work. That is language, and it is what a model is
//     for. Even a small local one can do it.
//   - localcode reads the time. Not the model. A local model asked for a
//     timestamp gets the year wrong occasionally, and a scheduled task
//     is exactly where an occasional wrong answer stays invisible until
//     the day it matters. The tool takes the words and parses them here,
//     with the same parser the command and the window use.
//   - The person confirms. Booking unattended work is a side effect, so
//     it asks like every other side effect, and the question names the
//     moment localcode read rather than the words the model passed.

// ScheduleTool books a prompt for later.
type ScheduleTool struct {
	loop *Loop
}

func NewScheduleTool(loop *Loop) ScheduleTool { return ScheduleTool{loop: loop} }

func (ScheduleTool) Name() string { return "Schedule" }

func (ScheduleTool) Description() string {
	return "Book a prompt to run later, in this same project, when the user asks for something " +
		"to happen at a later time (\"tomorrow morning\", \"in 30 minutes\", \"내일 아침에\", " +
		"\"금요일 저녁에\"). Pass the user's own words for the time in `when` — do not convert them " +
		"to a date or a timestamp yourself — and the work to do in `prompt`, written so it stands " +
		"on its own, since the scheduled run will not see this conversation. " +
		"It runs only while localcode is running. Use this instead of doing the work now."
}

func (ScheduleTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"when":{"type":"string","description":"the time in the user's own words, e.g. \"tomorrow 9am\", \"30분 뒤\", \"금요일 저녁\""},` +
		`"prompt":{"type":"string","description":"self-contained instructions for the scheduled run; it cannot see this conversation"}},` +
		`"required":["when","prompt"]}`)
}

// RequiresPermission is true, and this is the confirmation step.
//
// Booking work is a side effect with a delay on it: the model is asking
// to run a turn later, unattended, that will use tools of its own. That
// is at least as much of a commitment as a write, and unlike a write
// nobody sees it happen. The prompt names the moment localcode read out
// of the words, which is also the echo that catches a misread time before
// the work is booked rather than after it fails to happen.
func (ScheduleTool) RequiresPermission(json.RawMessage) bool { return true }

// Subject is the resolved time and the work, so a permission rule can
// match on it and the prompt has something to show.
func (t ScheduleTool) Subject(input json.RawMessage) string {
	args := t.parse(input)
	return args.When + ": " + promptSummary(args.Prompt)
}

// Describe is what the permission prompt says. It states the moment
// rather than the words, because "schedule for 내일 아침" is the model's
// reading and "schedule for 09:00 tomorrow" is localcode's, and the one
// worth confirming is the one that will actually happen.
func (t ScheduleTool) Describe(input json.RawMessage) string {
	args := t.parse(input)
	now := time.Now()
	at, err := when.ParseTime(args.When, now)
	if err != nil {
		return fmt.Sprintf("schedule %q (unreadable time: %s)", promptSummary(args.Prompt), args.When)
	}
	return fmt.Sprintf("schedule for %s: %s", when.Format(at, now), promptSummary(args.Prompt))
}

type scheduleArgs struct {
	When   string `json:"when"`
	Prompt string `json:"prompt"`
}

func (ScheduleTool) parse(input json.RawMessage) scheduleArgs {
	var args scheduleArgs
	_ = json.Unmarshal(input, &args)
	args.When = strings.TrimSpace(args.When)
	args.Prompt = strings.TrimSpace(args.Prompt)
	return args
}

func (t ScheduleTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	if t.loop.Schedules == nil {
		return tools.Result{Content: "this build has no scheduler", IsError: true}
	}
	sessionID, ok := SessionIDFromContext(ctx)
	if !ok {
		return tools.Result{Content: "Schedule has no session context", IsError: true}
	}
	// A scheduled turn may not book more scheduled turns. Nobody is
	// watching it, so a task that books its own successor is a loop with
	// no one in the room to notice — and the ceiling on outstanding tasks
	// is per conversation, which a chain of them would walk straight
	// past by starting a new conversation each time.
	if Unattended(ctx) {
		return tools.Result{
			Content: "this turn is itself a scheduled task, and a scheduled task cannot book another one",
			IsError: true, Refused: true,
		}
	}

	args := t.parse(input)
	if args.Prompt == "" {
		return tools.Result{Content: "say what to do at that time, in `prompt`", IsError: true}
	}
	now := time.Now()
	at, err := when.ParseTime(args.When, now)
	if err != nil {
		// The parser's own sentence, which names which kind of no this
		// is — a vague time, a repeat, or one it cannot read. Handed back
		// so the model can ask the user for the missing half rather than
		// inventing one.
		return tools.Result{Content: err.Error(), IsError: true}
	}

	agentName := t.loop.sessionAgent(sessionID)
	if agentName == "" {
		agentName = "general-purpose"
	}
	entry, err := t.loop.Schedules.Add(sessionID, agentName, args.Prompt, at)
	if err != nil {
		return tools.Result{Content: err.Error(), IsError: true}
	}
	return tools.Result{Content: fmt.Sprintf(
		"Scheduled as %s for %s, working in %s.\n"+
			"It runs only while localcode is running; if localcode is closed at that moment "+
			"the task is reported as missed rather than run late.\n"+
			"Tell the user when it will run, and that it needs localcode to be running.",
		entry.ID, when.Format(at, now), t.loop.SessionDir(sessionID))}
}
