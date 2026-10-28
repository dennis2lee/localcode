package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"localcode/internal/events"
	"localcode/internal/tools"
)

// Saved workflows and /workflow.

func TestWorkflowArgumentsSplitLikeAShellLine(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
		err  bool
	}{
		{"a b  c", []string{"a", "b", "c"}, false},
		{"one\r\ntwo\nthree", []string{"one", "two", "three"}, false},
		{`"docs/my file.md" 'it''s' x`, []string{"docs/my file.md", "its", "x"}, false},
		{"A a  B", []string{"A", "B"}, false},
		{`"" x`, []string{"x"}, false},
		{`"unclosed x`, nil, true},
	} {
		got, err := splitArgs(tc.in)
		if (err != nil) != tc.err {
			t.Errorf("splitArgs(%q) error = %v, want error %v", tc.in, err, tc.err)
			continue
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("splitArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func argsWorkflow(t *testing.T) Workflow {
	t.Helper()
	var wf Workflow
	if err := json.Unmarshal([]byte(`{"name":"look","args":"what to look at","plan":{
	  "goal":"look at {{args}}",
	  "stages":[
	    {"name":"each","kind":"fanout","agent":"oracle","over":["$args"],"prompt":"look at {{item}} for {{args}}"},
	    {"name":"sum","kind":"step","agent":"plan","prompt":"sum up {{args}}"}]}}`), &wf); err != nil {
		t.Fatalf("workflow: %v", err)
	}
	return wf
}

// The arguments are text: whatever they contain is never read as a
// template, and they can never become a reference to a stage.
func TestWorkflowArgumentsAreSubstitutedOnceAndOnlyOnce(t *testing.T) {
	wf := argsWorkflow(t)
	if err := wf.check([]string{"oracle", "plan"}); err != nil {
		t.Fatalf("a workflow using its arguments was refused: %v", err)
	}

	p, err := wf.expand(`x "{{input}} {{item}}"`)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if err := p.Validate(Limits{Agents: []string{"oracle", "plan"}, Args: true}); err != nil {
		t.Fatalf("the expanded plan was refused: %v", err)
	}
	if got := p.Stages[0].Over; len(got) != 2 || got[1] != "{{input}} {{item}}" {
		t.Fatalf("the fanout spreads over %q, want the two arguments", got)
	}
	withInput := p.Stages[1]
	withInput.Prompt = "sum up {{args}} given {{input}}"
	prompt := stagePrompt(p, withInput, "", "CARRIED")
	if !strings.Contains(prompt, `sum up x "{{input}} {{item}}" given CARRIED`) {
		t.Errorf("the arguments were not inserted verbatim:\n%s", prompt)
	}
	if n := strings.Count(prompt, "CARRIED"); n != 1 {
		t.Errorf("{{input}} inside the arguments was expanded as a template (%d copies of the carried input):\n%s", n, prompt)
	}
	if !strings.Contains(stagePrompt(p, p.Stages[0], "x", ""), "The run's goal: look at x") {
		t.Error("the goal did not carry the arguments")
	}

	for _, tc := range []struct{ args, want string }{
		{"", "takes arguments: what to look at"},
		{"$HOME", "cannot start with $"},
		{strings.Repeat("a ", 0) + "1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17", "17 given, the limit is 16"},
	} {
		if _, err := wf.expand(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("expand(%q) = %v, want an error saying %q", tc.args, err, tc.want)
		}
	}
	none := wf
	none.Args, none.Plan = "", Plan{Goal: "g", Stages: []Stage{{Name: "a", Kind: "step", Agent: "oracle", Prompt: "x"}}}
	if _, err := none.expand("extra"); err == nil || !strings.Contains(err.Error(), "takes no arguments") {
		t.Errorf("arguments to a workflow that takes none: %v", err)
	}
}

// {{args}} and $args belong to saved workflows; a plan the model writes
// has no arguments to put in them.
func TestOnlyAWorkflowMayUseItsArguments(t *testing.T) {
	inline := planOf(t, `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"x {{args}}"}]}`)
	if err := inline.Validate(roster("oracle")); err == nil || !strings.Contains(err.Error(), "saved workflow") {
		t.Errorf("an inline plan using {{args}}: %v", err)
	}
	over := planOf(t, `{"goal":"g","stages":[{"name":"a","kind":"fanout","agent":"oracle","over":["$args"],"prompt":"{{item}}"}]}`)
	if err := over.Validate(roster("oracle")); err == nil {
		t.Error("an inline plan spreading over $args was accepted")
	}
	wf := argsWorkflow(t)
	wf.Args = ""
	if err := wf.check([]string{"oracle", "plan"}); err == nil || !strings.Contains(err.Error(), `"args"`) {
		t.Errorf("a workflow using its arguments without saying what they are: %v", err)
	}
}

// workflowLoop is orchestrateLoop with a project and a home directory, and
// a permission handler that records what it was asked.
type askLog struct {
	mu    sync.Mutex
	asks  []tools.Ask
	allow bool
}

func (a *askLog) handle(_ context.Context, ask tools.Ask) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asks = append(a.asks, ask)
	return a.allow, nil
}

func workflowLoop(t *testing.T, reply func(string) (string, map[string]any)) (*Loop, string, string, *askLog) {
	t.Helper()
	m := &scriptedModel{reply: reply}
	loop := orchestrateLoop(t, m.server(t).URL)
	asks := &askLog{allow: true}
	reg := tools.NewRegistry(asks.handle)
	reg.Register(tools.ReadFile{})
	reg.Register(NewAnswerTool())
	reg.Register(NewOrchestrateTool(loop))
	loop.Tools = reg

	project, home := t.TempDir(), t.TempDir()
	loop.Memory = &MemoryConfig{Home: home}
	if _, err := loop.Store.CreateSessionIn("conv", "", "oracle", project, true); err != nil {
		t.Fatalf("session: %v", err)
	}
	return loop, project, home, asks
}

func writeWorkflow(t *testing.T, dir, name, body string) string {
	t.Helper()
	wdir := filepath.Join(dir, ".localcode", "workflows")
	if err := os.MkdirAll(wdir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wdir, name+".json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const simpleWorkflow = `{"name":"%s","description":"%s","plan":{"goal":"check things",
  "stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"check"}]}}`

func workflowBody(name, desc string) string {
	return strings.NewReplacer("%s", name).Replace(strings.Replace(simpleWorkflow, `"description":"%s"`, `"description":"`+desc+`"`, 1))
}

// A project file beats a home file of the same name, and a broken file is
// listed with why rather than missing.
func TestWorkflowsAreFoundProjectFirstAndBrokenOnesAreShown(t *testing.T) {
	loop, project, home, _ := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	writeWorkflow(t, home, "same", workflowBody("same", "from home"))
	writeWorkflow(t, project, "same", workflowBody("same", "from project"))
	writeWorkflow(t, home, "wrong", workflowBody("other", "x"))
	writeWorkflow(t, project, "extra", `{"name":"extra","plan":{"goal":"g","stages":[]},"surprise":1}`)

	if err := loop.SendMessage(context.Background(), "conv", "oracle", "/workflow"); err != nil {
		t.Fatal(err)
	}
	out := lastAssistantText(loop.Store, "conv")
	for _, want := range []string{"same (project): from project", "wrong (user)", "is called \"other\" inside", "extra (project)", "does not parse"} {
		if !strings.Contains(out, want) {
			t.Errorf("/workflow does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "from home") {
		t.Errorf("the home workflow shadowed by the project's is listed:\n%s", out)
	}
}

// A workflow in the home directory runs when its owner types it: the line
// and localcode's report are a turn of the conversation the model sees
// next, and the run is one task under it.
func TestRunningAUserWorkflowRecordsTheTurn(t *testing.T) {
	loop, _, home, asks := workflowLoop(t, func(p string) (string, map[string]any) { return "checked " + p[strings.LastIndex(p, " ")+1:], nil })
	writeWorkflow(t, home, "look", `{"name":"look","args":"what to look at","plan":{"goal":"look at {{args}}",
	  "stages":[{"name":"each","kind":"fanout","agent":"oracle","over":["$args"],"prompt":"look at {{item}}"}]}}`)

	if err := loop.SendMessage(context.Background(), "conv", "oracle", "/workflow run look alpha beta"); err != nil {
		t.Fatal(err)
	}
	if len(asks.asks) != 0 {
		t.Errorf("a home workflow asked for permission: %+v", asks.asks)
	}
	out := lastAssistantText(loop.Store, "conv")
	if !strings.Contains(out, "Workflow look (run task-") || !strings.Contains(out, "2 agent turns") {
		t.Errorf("the reply is not the run's report:\n%s", out)
	}

	evs, _ := loop.Store.Events("conv", 0)
	var line map[string]any
	for _, e := range evs {
		if e.Type == events.TypeUserMessage {
			line = e.Data
		}
	}
	if line == nil || line["text"] != "/workflow run look alpha beta" || line["local"] == true {
		t.Errorf("the typed line is not an ordinary turn the model will see: %v", line)
	}
	hist := loop.messages["conv"]
	if len(hist) < 2 || !strings.Contains(hist[len(hist)-1].Content[0].Text, "Workflow look") {
		t.Error("the report is not in the history the model sees next")
	}
	if kids := loop.Store.Children("conv"); len(kids) != 1 || kids[0].Agent != runSessionAgent {
		t.Errorf("the run is not one task of the conversation: %v", kids)
	}
}

// A project's workflow asks first, and the question names the exact file:
// an "always" for it does not answer for an edited copy.
func TestAProjectWorkflowAsksAndTheQuestionNamesItsContent(t *testing.T) {
	loop, project, _, asks := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	writeWorkflow(t, project, "check", workflowBody("check", "first"))

	asks.allow = false
	loop.SendMessage(context.Background(), "conv", "oracle", "/workflow run check")
	if len(asks.asks) != 1 {
		t.Fatalf("asked %d times, want once", len(asks.asks))
	}
	first := asks.asks[0]
	if !strings.HasPrefix(first.Subject, "workflow:check@") || first.Tool != orchestrateToolName {
		t.Errorf("the question is %+v, want Orchestrate about workflow:check@<hash>", first)
	}
	if !strings.Contains(first.Description, "project workflow") || !strings.Contains(first.Description, "readonly") {
		t.Errorf("the question does not say what will run:\n%s", first.Description)
	}
	if out := lastAssistantText(loop.Store, "conv"); !strings.HasPrefix(out, "Not run") {
		t.Errorf("a refused workflow ran or said nothing:\n%s", out)
	}
	if kids := loop.Store.Children("conv"); len(kids) != 0 {
		t.Error("a refused workflow started a run")
	}

	writeWorkflow(t, project, "check", workflowBody("check", "edited"))
	loop.SendMessage(context.Background(), "conv", "oracle", "/workflow run check")
	if len(asks.asks) != 2 || asks.asks[1].Subject == first.Subject {
		t.Errorf("an edited workflow asked with the same subject %q", first.Subject)
	}
}

// Only a person typing it runs one.
func TestAWorkflowIsNotRunWhereNobodyTypedIt(t *testing.T) {
	loop, _, home, _ := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	writeWorkflow(t, home, "w", workflowBody("w", "x"))
	for name, ctx := range map[string]context.Context{
		"unattended":     WithUnattended(context.Background()),
		"booked command": withCommandRun(context.Background()),
	} {
		t.Run(name, func(t *testing.T) {
			loop.SendMessage(ctx, "conv", "oracle", "/workflow run w")
			if out := lastAssistantText(loop.Store, "conv"); !strings.HasPrefix(out, "Not run") {
				t.Errorf("the workflow was not refused:\n%s", out)
			}
		})
	}
	if kids := loop.Store.Children("conv"); len(kids) != 0 {
		t.Error("a refused workflow started a run")
	}

	off := false
	loop.Config.Orchestrate = &off
	loop.SendMessage(context.Background(), "conv", "oracle", "/workflow run w")
	if out := lastAssistantText(loop.Store, "conv"); !strings.Contains(out, "/orchestrate on") {
		t.Errorf("with orchestration off the refusal does not say how to turn it on:\n%s", out)
	}
}

// /workflow save keeps the last plan that ran; delete removes it.
func TestSaveKeepsTheLastPlanAndDeleteRemovesIt(t *testing.T) {
	loop, _, home, _ := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	if res := NewOrchestrateTool(loop).Execute(WithSessionID(loop.pinSmart(context.Background()), "conv"),
		json.RawMessage(`{"goal":"review it","stages":[{"name":"r","kind":"step","agent":"oracle","prompt":"review"}]}`)); res.IsError {
		t.Fatalf("the run failed: %s", res.Content)
	}

	loop.SendMessage(context.Background(), "conv", "oracle", "/workflow save my-review --user reviews a change")
	path := filepath.Join(home, ".localcode", "workflows", "my-review.json")
	wf := readWorkflow(path, scopeUser)
	if wf.err != nil {
		t.Fatalf("the saved file does not read back: %v\n%s", wf.err, lastAssistantText(loop.Store, "conv"))
	}
	if wf.Description != "reviews a change" || wf.Plan.Goal != "review it" {
		t.Errorf("saved %+v", wf)
	}
	if out := lastAssistantText(loop.Store, "conv"); !strings.Contains(out, `"goal": "review it"`) {
		t.Errorf("saving did not show what it saved:\n%s", out)
	}

	loop.SendMessage(context.Background(), "conv", "oracle", "/workflow save my-review --user")
	if out := lastAssistantText(loop.Store, "conv"); !strings.Contains(out, "--force") {
		t.Errorf("saving over a file did not refuse:\n%s", out)
	}

	// The last run being a saved workflow, there is nothing new to save:
	// its prompts say {{args}} and the arguments are not part of it.
	loop.SendMessage(context.Background(), "conv", "oracle", "/workflow run my-review")
	loop.SendMessage(context.Background(), "conv", "oracle", "/workflow save again --user")
	if out := lastAssistantText(loop.Store, "conv"); !strings.Contains(out, `saved workflow "my-review"`) {
		t.Errorf("saving a run of a saved workflow did not say it is one:\n%s", out)
	}

	loop.SendMessage(context.Background(), "conv", "oracle", "/workflow delete my-review --user")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the workflow is still there after delete: %v", err)
	}
}
