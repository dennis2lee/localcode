package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// Who composed a booked prompt.
//
// A booking is a prompt and a moment, and when the moment comes the prompt
// is handed to SendMessage, which treats what it is handed as something a
// person typed: a leading "/" is a command and a leading "!" is a shell
// command that no permission rule is asked about. That is right for the
// person who booked it and wrong for the model, which writes the prompt
// after reading files and tool output and was never given either.
//
// Task and TaskBackground close the same door with withDelegatedTask. The
// Schedule tool and a "/schedule" line the Command tool runs did not, so
// with model_invocable off a model could book "!touch x" and have it run.
// Every test below goes through a real door, the tool or SendMessage, and
// not through a flag, so a booking that sets a field nothing reads fails
// here.

// authorRig is one conversation with a scheduler, and a model that keeps
// every request it was sent, so a test can say what the model was told and
// not only what a command did.
type authorRig struct {
	loop    *Loop
	sched   *Scheduler
	model   *scriptedProvider
	sid     string
	project string
}

// newAuthorRig builds the rig. turns are the model's answers in order: the
// ones a booking turn needs first, then what a fired run would say. A run
// that handled its prompt as a command asks the model nothing, so the
// count of requests is itself evidence.
func newAuthorRig(t *testing.T, turns ...[]provider.StreamEvent) *authorRig {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	model := &scriptedProvider{turns: turns}
	// A person who says yes to every question, which is what the model's
	// Schedule call meets under skip_tools or an allow rule. The bug does not
	// depend on the answer, and a rig that said no would pass for the wrong
	// reason.
	reg := tools.NewRegistry(func(context.Context, tools.Ask) (bool, error) { return true, nil })
	loop := New(store, reg, map[string]provider.Provider{"local": model}, cfg)
	reg.Register(NewScheduleTool(loop))
	reg.Register(NewCommandTool(loop))

	const sid = "s1"
	project := t.TempDir()
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sched := NewScheduler(context.Background(), loop)
	// A booking is an hour out and nothing here waits for it, so every one
	// is cancelled when the test ends rather than left armed. Registered
	// after the store's own cleanup, so it runs before the store closes.
	t.Cleanup(func() {
		for _, e := range sched.List("") {
			sched.Cancel(e.SessionID, e.ID)
		}
	})
	return &authorRig{loop: loop, sched: sched, model: model, sid: sid, project: project}
}

// answer is a model turn that says one thing and stops.
func answer(text string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: text},
		{Type: provider.EventMessageStop, StopReason: "end_turn"},
	}
}

// callTool is a model turn that asks for one tool.
func callTool(name string, args map[string]string) []provider.StreamEvent {
	input, _ := json.Marshal(args)
	return []provider.StreamEvent{
		{Type: provider.EventToolUseStart, ToolUseID: "call_1", ToolName: name},
		{Type: provider.EventToolUseEnd, ToolUseID: "call_1", ToolInput: input},
		{Type: provider.EventMessageStop, StopReason: "tool_use"},
	}
}

// fire runs a booking now and returns it as it ended.
//
// By hand rather than by waiting on the timer, so the run is over when
// this returns and nothing here depends on a clock.
func (r *authorRig) fire(t *testing.T, id string) Scheduled {
	t.Helper()
	r.sched.fire(r.sid, id)
	e, ok := r.sched.Get(r.sid, id)
	if !ok {
		t.Fatalf("booking %s is gone after it fired", id)
	}
	if e.Status != ScheduleDone {
		t.Fatalf("booking %s is %q (%s), want %q", id, e.Status, e.Error, ScheduleDone)
	}
	return e
}

// booked is the one booking a test made, failing if there is not exactly
// one.
func (r *authorRig) booked(t *testing.T) Scheduled {
	t.Helper()
	list := r.sched.List(r.sid)
	if len(list) != 1 {
		t.Fatalf("booked %d tasks, want 1: %+v", len(list), list)
	}
	return list[0]
}

// lastSentToTheModel is the text of the newest user message in the newest
// request the model received, or "" when it was never asked.
func (r *authorRig) lastSentToTheModel() string {
	reqs := r.model.sentRequests()
	if len(reqs) == 0 {
		return ""
	}
	msgs := reqs[len(reqs)-1].Messages
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != provider.RoleUser {
			continue
		}
		var b strings.Builder
		for _, blk := range msgs[i].Content {
			if blk.Type == provider.BlockText {
				b.WriteString(blk.Text)
			}
		}
		return b.String()
	}
	return ""
}

// ranAShellCommand reports whether a session holds output of a "!" turn.
func ranAShellCommand(t *testing.T, loop *Loop, sessionID string) bool {
	t.Helper()
	for _, ev := range mustEvents(t, loop.Store, sessionID) {
		if _, ok := ev.Data["shell_command"]; ok {
			return true
		}
	}
	return false
}

// marker is the file shellPrompt leaves in the project. Relative, so the
// shell makes it in the session's workspace on every platform and the test
// never hands a Windows path to a POSIX shell, and made with a redirect
// rather than touch, which cmd.exe, the fallback shell on Windows, has no
// idea of.
const marker = "ran-marker"

const shellPrompt = "!echo ran > " + marker

// What a prompt is when it is a command: the two shapes the report named.
// The shell escape is the one that matters, since it runs with no
// permission rule asked, and "/orchestrate on" is the one that is visible,
// since it moves a daemon-wide switch.
var commandShapedPrompts = []struct {
	name, prompt string
	shell        bool
}{
	{"a shell escape", shellPrompt, true},
	{"a slash command", "/orchestrate on", false},
}

// thePromptReachedTheModelAsText fails unless the run asked the model
// exactly once, with the prompt in its message, and nothing ran: no shell,
// no flipped switch.
func (r *authorRig) thePromptReachedTheModelAsText(t *testing.T, prompt, run string, requestsBefore int) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(r.project, marker)); !os.IsNotExist(err) {
		t.Errorf("the prompt ran a shell command: %s exists in the project", marker)
	}
	if ranAShellCommand(t, r.loop, run) {
		t.Error("the run session holds shell output for a prompt the model wrote")
	}
	if r.loop.OrchestrateEnabled() {
		t.Error("the prompt switched orchestration on")
	}
	if got := len(r.model.sentRequests()); got != requestsBefore+1 {
		t.Errorf("the run made %d model request(s), want 1: a prompt handled as a command never reaches the model",
			got-requestsBefore)
	}
	if got := r.lastSentToTheModel(); !strings.Contains(got, prompt) {
		t.Errorf("the model was sent %q, want the booked prompt %q as ordinary text", got, prompt)
	}
}

// The bug, through the door the report used: the model calls Schedule with
// model_invocable off, the permission step is answered yes, and the run
// that fires it must not be a command.
func TestAPromptTheModelSchedulesIsSentToTheRunAsText(t *testing.T) {
	for _, tc := range commandShapedPrompts {
		t.Run(tc.name, func(t *testing.T) {
			rig := newAuthorRig(t,
				callTool("Schedule", map[string]string{"when": "in 1 hour", "prompt": tc.prompt}),
				answer("booked"),
				answer("did the thing"),
			)
			if rig.loop.ModelInvocableEnabled() {
				t.Fatal("model_invocable starts on, so the test would not show the switch is irrelevant")
			}
			if rig.loop.OrchestrateEnabled() {
				t.Fatal("orchestrate starts on, so the test would prove nothing")
			}

			if err := rig.loop.SendMessage(context.Background(), rig.sid, "general-purpose", "do that later"); err != nil {
				t.Fatalf("SendMessage: %v", err)
			}
			e := rig.booked(t)
			if e.Prompt != tc.prompt {
				t.Fatalf("booked %q, want %q", e.Prompt, tc.prompt)
			}
			asked := len(rig.model.sentRequests())

			run := rig.fire(t, e.ID)
			rig.thePromptReachedTheModelAsText(t, tc.prompt, run.RunSession, asked)
		})
	}
}

// The same promise one level down, at the tool, with no model turn to set it
// up. This is the shape the bug report reproduced with a probe.
func TestTheScheduleToolBooksAModelsPromptAsText(t *testing.T) {
	for _, tc := range commandShapedPrompts {
		t.Run(tc.name, func(t *testing.T) {
			rig := newAuthorRig(t, answer("did the thing"))
			input, _ := json.Marshal(map[string]string{"when": "in 1 hour", "prompt": tc.prompt})

			res := NewScheduleTool(rig.loop).Execute(WithSessionID(context.Background(), rig.sid), input)
			if res.IsError {
				t.Fatalf("Execute: %s", res.Content)
			}
			run := rig.fire(t, rig.booked(t).ID)
			rig.thePromptReachedTheModelAsText(t, tc.prompt, run.RunSession, 0)
		})
	}
}

// The other half, and the reason the fix is keyed on who wrote the prompt
// and not on the scheduler: a person who books a command typed it, so it
// runs as what it says. Without this, closing the hole by sending every
// scheduled prompt to the model as text would pass the tests above.
func TestAPromptAPersonSchedulesStillRunsAsWritten(t *testing.T) {
	doors := []struct {
		name string
		book func(t *testing.T, r *authorRig, prompt string)
	}{
		{"Scheduler.Add, which the window's button uses", func(t *testing.T, r *authorRig, prompt string) {
			if _, err := r.sched.Add(r.sid, "general-purpose", prompt, time.Now().Add(time.Hour), RepeatOptions{}); err != nil {
				t.Fatalf("Add: %v", err)
			}
		}},
		{"a /schedule line the person typed", func(t *testing.T, r *authorRig, prompt string) {
			if err := r.loop.SendMessage(context.Background(), r.sid, "general-purpose", "/schedule in 1 hour "+prompt); err != nil {
				t.Fatalf("SendMessage: %v", err)
			}
		}},
	}
	for _, door := range doors {
		for _, tc := range commandShapedPrompts {
			t.Run(door.name+"/"+tc.name, func(t *testing.T) {
				rig := newAuthorRig(t, answer("did the thing"))
				door.book(t, rig, tc.prompt)
				e := rig.booked(t)
				run := rig.fire(t, e.ID)

				if tc.shell {
					if _, err := os.Stat(filepath.Join(rig.project, marker)); err != nil {
						t.Errorf("a shell escape a person scheduled did not run: %v", err)
					}
					if !ranAShellCommand(t, rig.loop, run.RunSession) {
						t.Error("the run session holds no shell output for a person's shell escape")
					}
				} else if !rig.loop.OrchestrateEnabled() {
					t.Error("a slash command a person scheduled did not run")
				}
				if n := len(rig.model.sentRequests()); n != 0 {
					t.Errorf("a command a person scheduled asked the model %d time(s), want 0", n)
				}
			})
		}
	}
}

// The second door. With a built-in named in model_commands the model may
// run it, and "/schedule" is one a person can name. The prompt in the line
// is the model's words just as it is through the Schedule tool.
func TestAScheduleLineTheCommandToolRunsIsSentToTheRunAsText(t *testing.T) {
	for _, tc := range commandShapedPrompts {
		t.Run(tc.name, func(t *testing.T) {
			rig := newAuthorRig(t,
				callTool("Command", map[string]string{"name": "/schedule", "arguments": "in 1 hour " + tc.prompt}),
				answer("done"),
				answer("did the thing"),
			)
			rig.loop.Config.SetModelInvocableRuntime(true)
			rig.loop.Config.ModelCommands = []string{"/schedule"}

			if err := rig.loop.SendMessage(context.Background(), rig.sid, "general-purpose", "do that later"); err != nil {
				t.Fatalf("SendMessage: %v", err)
			}
			e := rig.booked(t)
			if e.Prompt != tc.prompt {
				t.Fatalf("booked %q, want %q", e.Prompt, tc.prompt)
			}
			asked := len(rig.model.sentRequests())

			run := rig.fire(t, e.ID)
			rig.thePromptReachedTheModelAsText(t, tc.prompt, run.RunSession, asked)
		})
	}
}

// What makes the Command door distinguishable from a person's, and the
// shape the daemon really has: it runs a message the person queued behind
// a turn with the same context as the turn before it. If "this /schedule
// came from the Command tool" were a fact about the context, the session or
// the turn that was running, that message would be booked as the model's.
func TestAScheduleLineAPersonTypesAfterACommandRunIsTheirs(t *testing.T) {
	rig := newAuthorRig(t,
		callTool("Command", map[string]string{"name": "/schedule", "arguments": "in 1 hour the model's own errand"}),
		answer("done"),
	)
	rig.loop.Config.SetModelInvocableRuntime(true)
	rig.loop.Config.ModelCommands = []string{"/schedule"}

	// The daemon's turn context, reused for every message of a turn.
	turn := context.Background()
	if err := rig.loop.SendMessage(turn, rig.sid, "general-purpose", "do that later"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if err := rig.loop.SendMessage(turn, rig.sid, "general-purpose", "/schedule in 2 hours "+shellPrompt); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	list := rig.sched.List(rig.sid)
	if len(list) != 2 {
		t.Fatalf("booked %d tasks, want the model's and the person's: %+v", len(list), list)
	}
	var theirs Scheduled
	for _, e := range list {
		if e.Prompt == shellPrompt {
			theirs = e
		}
	}
	if theirs.ID == "" {
		t.Fatalf("the person's booking is missing: %+v", list)
	}
	run := rig.fire(t, theirs.ID)
	if _, err := os.Stat(filepath.Join(rig.project, marker)); err != nil {
		t.Errorf("a shell escape the person typed after a command run did not run: %v", err)
	}
	if !ranAShellCommand(t, rig.loop, run.RunSession) {
		t.Error("the run session holds no shell output for the person's shell escape")
	}
}

// Who wrote a prompt is written on the booking, because a restart rebuilds
// the books from the conversation's log and a field that is not there does
// not come back. A booking from before the field existed has no author, and
// it runs as the person wrote it: nothing in the log says otherwise, and a
// booking somebody made on purpose, repeating, would stop working
// silently if it were guessed to be the model's.
func TestWhoWroteABookedPromptSurvivesARestart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		by     string
		asText bool
	}{
		{"the model", "model", true},
		{"a person", "person", false},
		{"nobody recorded, from before the field", "", false},
		// Some later version's author, read by this one. Not a person's,
		// so not typed: an unknown author fails toward text.
		{"an author this version does not know", "a-later-kind", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newAuthorRig(t, answer("did the thing"))
			created := map[string]any{
				"id": "s7", "at": time.Now().Add(time.Hour).Format(time.RFC3339),
				"prompt": "/orchestrate on", "agent": "general-purpose",
			}
			if tc.by != "" {
				created["by"] = tc.by
			}
			if _, err := rig.loop.Store.Append(rig.sid, events.TypeScheduleCreated, created); err != nil {
				t.Fatalf("append: %v", err)
			}
			rig.sched.Restore([]string{rig.sid}, time.Now())

			rig.fire(t, "s7")
			if got := rig.loop.OrchestrateEnabled(); got == tc.asText {
				t.Errorf("after the restart the prompt ran as a command = %v, want %v", got, !tc.asText)
			}
			if asked := len(rig.model.sentRequests()) == 1; asked != tc.asText {
				t.Errorf("after the restart the model was asked = %v, want %v", asked, tc.asText)
			}
		})
	}
}

// A restart in the middle: the model books, the daemon stops, and a daemon
// that remembers nothing rebuilds the books from the log. The prompt is
// still the model's, because the log is the only place it can be told.
func TestAModelsBookingStillRunsAsTextAfterARestart(t *testing.T) {
	rig := newAuthorRig(t, answer("did the thing"))
	prompt := shellPrompt
	input, _ := json.Marshal(map[string]string{"when": "in 1 hour", "prompt": prompt})
	if res := NewScheduleTool(rig.loop).Execute(WithSessionID(context.Background(), rig.sid), input); res.IsError {
		t.Fatalf("Execute: %s", res.Content)
	}

	restarted := NewScheduler(context.Background(), rig.loop)
	t.Cleanup(func() {
		for _, e := range restarted.List("") {
			restarted.Cancel(e.SessionID, e.ID)
		}
	})
	restarted.Restore([]string{rig.sid}, time.Now())
	rig.sched = restarted

	run := rig.fire(t, rig.booked(t).ID)
	rig.thePromptReachedTheModelAsText(t, prompt, run.RunSession, 0)
}

// What each door writes is what Restore reads back, so the doors and the
// table above agree on one spelling of the field and not by accident.
func TestEveryDoorRecordsWhoWroteThePrompt(t *testing.T) {
	rig := newAuthorRig(t, answer("done"))
	rig.loop.Config.SetModelInvocableRuntime(true)
	rig.loop.Config.ModelCommands = []string{"/schedule"}
	ctx := WithSessionID(context.Background(), rig.sid)

	by := func() map[string]any {
		out := map[string]any{}
		for _, ev := range mustEvents(t, rig.loop.Store, rig.sid) {
			if ev.Type != events.TypeScheduleCreated {
				continue
			}
			id, _ := ev.Data["id"].(string)
			prompt, _ := ev.Data["prompt"].(string)
			out[prompt+" ("+id+")"] = ev.Data["by"]
		}
		return out
	}

	toolInput, _ := json.Marshal(map[string]string{"when": "in 1 hour", "prompt": "from the tool"})
	if res := NewScheduleTool(rig.loop).Execute(ctx, toolInput); res.IsError {
		t.Fatalf("Schedule: %s", res.Content)
	}
	if _, err := rig.sched.Add(rig.sid, "general-purpose", "from Add", time.Now().Add(time.Hour), RepeatOptions{}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := rig.loop.SendMessage(context.Background(), rig.sid, "general-purpose", "/schedule in 1 hour typed"); err != nil {
		t.Fatalf("typed: %v", err)
	}
	if err := rig.loop.SendMessage(withCommandRun(context.Background()), rig.sid, "general-purpose",
		"/schedule in 1 hour from the Command tool"); err != nil {
		t.Fatalf("command run: %v", err)
	}

	got := by()
	want := map[string]string{
		"from the tool (s1)":         "model",
		"from Add (s2)":              "person",
		"typed (s3)":                 "person",
		"from the Command tool (s4)": "model",
	}
	if len(got) != len(want) {
		t.Fatalf("schedule.created events = %v, want %d of them", got, len(want))
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s records by=%v, want %q", k, got[k], w)
		}
	}
}

// The marker is for the prompt that was booked and for nothing else. A
// person can open the run's conversation and type in it, and what they
// type there is theirs even when the context they type through is the one
// the booked prompt travelled on.
func TestOnlyTheBookedTextIsTreatedAsModelWritten(t *testing.T) {
	ctx := withModelWrittenPrompt(context.Background(), "!touch a")
	if !modelWrittenPrompt(ctx, "!touch a") {
		t.Error("the booked prompt is not recognised")
	}
	if modelWrittenPrompt(ctx, "!touch b") {
		t.Error("other text on the same context is treated as the model's")
	}
	if modelWrittenPrompt(context.Background(), "!touch a") {
		t.Error("text on an unmarked context is treated as the model's")
	}
	if modelWrittenPrompt(withModelWrittenPrompt(context.Background(), ""), "") {
		t.Error("an empty booking marks every blank message as the model's")
	}
	if modelWrittenPrompt(withCommandRun(ctx), "!touch a") {
		t.Error("a command run, which is a different message, still carries the booked prompt's mark")
	}

	// And through the door, where it is a consequence rather than a
	// property of two functions: the same context, a different message.
	rig := newAuthorRig(t)
	if err := rig.loop.SendMessage(ctx, rig.sid, "general-purpose", shellPrompt); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rig.project, marker)); err != nil {
		t.Errorf("a shell escape typed through the booked prompt's context did not run: %v", err)
	}
}

// A run the scheduler fired carries the booked prompt, and the model in it
// may call the Command tool. The line that books can read exactly like the
// prompt: it is a different message, and the one the allowlist decided.
func TestACommandRunThatReadsLikeTheBookedPromptStillRuns(t *testing.T) {
	rig := newAuthorRig(t, answer("took it as text"))
	ctx := withModelWrittenPrompt(WithUnattended(context.Background()), "/orchestrate on")

	if err := rig.loop.SendMessage(ctx, rig.sid, "general-purpose", "/orchestrate on"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if rig.loop.OrchestrateEnabled() {
		t.Fatal("the booked prompt ran as a command, so the setup does not show the difference")
	}

	if err := rig.loop.SendMessage(withCommandRun(ctx), rig.sid, "general-purpose", "/orchestrate on"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !rig.loop.OrchestrateEnabled() {
		t.Error("a command the allowlist let through was sent to the model as text because it matched the booked prompt")
	}
}

// The Schedule tool refuses to book from a scheduled turn, and a "/schedule"
// line the Command tool runs is the other way to book. Without the same
// refusal there, a scheduled run whose model may run /schedule books its
// successor in the run's own conversation, which the per-conversation
// ceiling never sees.
func TestAScheduledTurnCannotBookAnotherWithACommandEither(t *testing.T) {
	rig := newAuthorRig(t)
	ctx := withCommandRun(WithUnattended(context.Background()))

	if err := rig.loop.SendMessage(ctx, rig.sid, "general-purpose", "/schedule in 1 hour again"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if n := len(rig.sched.List(rig.sid)); n != 0 {
		t.Fatalf("a scheduled turn booked %d task(s) through the Command tool", n)
	}
	if got := lastAssistantText(rig.loop.Store, rig.sid); !strings.Contains(got, "cannot book another") {
		t.Errorf("the refusal does not say why: %q", got)
	}

	// The same line from a turn somebody is watching books normally, so
	// the refusal is about the turn and not about the command.
	if err := rig.loop.SendMessage(withCommandRun(context.Background()), rig.sid, "general-purpose",
		"/schedule in 1 hour again"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if n := len(rig.sched.List(rig.sid)); n != 1 {
		t.Errorf("an attended command run booked %d task(s), want 1", n)
	}
}

// A model that books a command-shaped prompt is told it will be text, so
// it can say so to the user rather than report a command as scheduled. A
// prompt that does not look like a command is not told anything.
func TestTheModelIsToldACommandShapedPromptWillBeText(t *testing.T) {
	for _, tc := range []struct {
		prompt string
		told   bool
	}{
		{"!make test", true},
		{"/review", true},
		{"  /compact", true},
		{"run the tests", false},
		{"say hello! then stop", false},
	} {
		rig := newAuthorRig(t)
		input, _ := json.Marshal(map[string]string{"when": "in 1 hour", "prompt": tc.prompt})
		res := NewScheduleTool(rig.loop).Execute(WithSessionID(context.Background(), rig.sid), input)
		if res.IsError {
			t.Fatalf("Execute(%q): %s", tc.prompt, res.Content)
		}
		if got := strings.Contains(res.Content, "plain text"); got != tc.told {
			t.Errorf("the tool's answer for %q says it is plain text = %v, want %v: %s", tc.prompt, got, tc.told, res.Content)
		}

		// The same through a line the Command tool ran, whose answer is
		// the reply in the conversation.
		line := "/schedule in 1 hour " + tc.prompt
		if err := rig.loop.SendMessage(withCommandRun(context.Background()), rig.sid, "general-purpose", line); err != nil {
			t.Fatalf("SendMessage(%q): %v", line, err)
		}
		if got := strings.Contains(lastAssistantText(rig.loop.Store, rig.sid), "plain text"); got != tc.told {
			t.Errorf("the reply to %q says it is plain text = %v, want %v", line, got, tc.told)
		}
		// And a person's own line is never told it: what they book runs
		// as they wrote it.
		if err := rig.loop.SendMessage(context.Background(), rig.sid, "general-purpose", line); err != nil {
			t.Fatalf("SendMessage(%q): %v", line, err)
		}
		if strings.Contains(lastAssistantText(rig.loop.Store, rig.sid), "plain text") {
			t.Errorf("a person's own /schedule line for %q was told it would be plain text", tc.prompt)
		}
	}
}
