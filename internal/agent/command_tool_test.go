package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"localcode/internal/commands"
	"localcode/internal/skills"
)

// The model running a command.
//
// Two switches and a list decide whether anything is reachable at all,
// and every test here is about one of the ways that could be wrong: a
// name reachable that nobody wrote down, a slash that was repaired
// instead of refused, a shell splice promoted to a privileged call, or a
// command that could book another.

func invocableLoop(t *testing.T, on bool) *Loop {
	t.Helper()
	loop, _ := clearLoop(t)
	loop.Config.SetModelInvocableRuntime(on)
	return loop
}

// Nothing is reachable that somebody did not write down, and the enum the
// model is shown is the same list a call is checked against.
func TestOnlyWhatWasOptedInIsReachable(t *testing.T) {
	loop := invocableLoop(t, true)
	loop.Config.ModelCommands = []string{"/compact", "usage"} // one with a slash, one without
	loop.Commands = []commands.Command{
		{Name: "tidy-context", ModelInvocable: true},
		{Name: "deploy"}, // not opted in
	}
	loop.Skills = []skills.Skill{
		{Name: "review", ModelInvocable: true},
		{Name: "other"},
	}

	got := loop.modelCommands()
	want := []string{"/compact", "/review", "/tidy-context", "/usage"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("reachable = %v, want %v", got, want)
	}

	// The enum and the check are the same list, or the schema advertises
	// something the tool refuses.
	schema := string(NewCommandTool(loop).InputSchemaFor(context.Background()))
	for _, name := range want {
		if !strings.Contains(schema, name) {
			t.Errorf("the schema does not offer %q: %s", name, schema)
		}
	}
	for _, name := range []string{"/deploy", "/other"} {
		if strings.Contains(schema, name) {
			t.Errorf("the schema offers %q, which nothing opted in", name)
		}
	}
}

// A name nobody opted in is refused, and the refusal says what there is.
func TestACommandNobodyOptedInIsRefused(t *testing.T) {
	loop := invocableLoop(t, true)
	loop.Config.ModelCommands = []string{"/compact"}
	ctx := WithSessionID(context.Background(), "s1")

	res := NewCommandTool(loop).Execute(ctx, json.RawMessage(`{"name":"/permission-skip-all","arguments":"on"}`))
	if !res.IsError || !res.Refused {
		t.Fatalf("a command nobody opted in returned %+v, want a refusal", res)
	}
	if _, booked := loop.takePendingCommand("s1"); booked {
		t.Error("a refused command was booked anyway")
	}
}

// The slash is required rather than repaired. A model that wrote
// "compact" may have meant the word.
func TestANameWithoutItsSlashIsRefusedRatherThanRepaired(t *testing.T) {
	loop := invocableLoop(t, true)
	loop.Config.ModelCommands = []string{"/compact"}
	ctx := WithSessionID(context.Background(), "s1")

	res := NewCommandTool(loop).Execute(ctx, json.RawMessage(`{"name":"compact"}`))
	if !res.IsError {
		t.Fatalf("%q was accepted without its slash: %+v", "compact", res)
	}
	if !strings.Contains(res.Content, "/compact") {
		t.Errorf("the refusal does not show the spelling it wanted: %q", res.Content)
	}
	if _, booked := loop.takePendingCommand("s1"); booked {
		t.Error("a name with no slash was booked")
	}
}

// A booked command becomes the next turn, with its arguments.
func TestAnAcceptedCommandIsBookedAsTypedText(t *testing.T) {
	loop := invocableLoop(t, true)
	loop.Commands = []commands.Command{{Name: "tidy-context", ModelInvocable: true}}
	ctx := WithSessionID(context.Background(), "s1")

	res := NewCommandTool(loop).Execute(ctx, json.RawMessage(`{"name":"/tidy-context","arguments":"the parser"}`))
	if res.IsError {
		t.Fatalf("Execute: %s", res.Content)
	}
	line, booked := loop.takePendingCommand("s1")
	if !booked || line != "/tidy-context the parser" {
		t.Errorf("booked %q (%v), want the command line a person would type", line, booked)
	}
	// Taken, not read: a booking left behind fires on a later message.
	if _, again := loop.takePendingCommand("s1"); again {
		t.Error("the booking survived being taken")
	}
}

// One command booking the next is a loop with nothing bounding it.
func TestACommandRunCannotBookAnother(t *testing.T) {
	loop := invocableLoop(t, true)
	loop.Config.ModelCommands = []string{"/compact"}
	ctx := withCommandRun(WithSessionID(context.Background(), "s1"))

	res := NewCommandTool(loop).Execute(ctx, json.RawMessage(`{"name":"/compact"}`))
	if !res.IsError || !res.Refused {
		t.Fatalf("a command booked from inside a command run returned %+v, want a refusal", res)
	}
}

// The switch and the list are separate. Off with everything opted in must
// offer nothing, or the switch is decoration.
func TestTheSwitchOffHidesTheToolWhateverIsOptedIn(t *testing.T) {
	loop := invocableLoop(t, false)
	loop.Config.ModelCommands = []string{"/compact"}
	loop.Commands = []commands.Command{{Name: "tidy-context", ModelInvocable: true}}

	ctx := WithSessionID(context.Background(), "s1")
	if hidden := loop.hiddenTools(ctx); !hidden[commandToolName] {
		t.Error("the tool is offered with the switch off")
	}

	// And on with nothing opted in is a tool whose enum has no members,
	// which is a call that can only fail.
	loop.Config.SetModelInvocableRuntime(true)
	loop.Config.ModelCommands = nil
	loop.Commands = nil
	if hidden := loop.hiddenTools(ctx); !hidden[commandToolName] {
		t.Error("the tool is offered with nothing opted in")
	}
}

// The shell splice, refused at load. It runs through neither the bash
// tool nor the permission gate, so a model calling a command that carries
// one would be running a shell command nobody was asked about.
func TestAShellSpliceRefusesModelInvocation(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := writeFile(dir, name+".md", body); err != nil {
			t.Fatal(err)
		}
	}
	write("safe", "---\ndescription: fine\nmodel_invocable: true\n---\nSummarise @README.md\n")
	write("spliced", "---\ndescription: risky\nmodel_invocable: true\n---\nDeploy now: !`kubectl apply -f prod.yaml`\n")

	list, err := commands.LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	byName := map[string]commands.Command{}
	for _, c := range list {
		byName[c.Name] = c
	}

	if !byName["safe"].ModelInvocable {
		t.Error("a command with no splice was refused model invocation")
	}
	spliced := byName["spliced"]
	if spliced.ModelInvocable {
		t.Fatal("a command containing a shell splice is model-invocable; a model could run it without the permission gate")
	}
	if spliced.Refused == "" {
		t.Error("the refusal is silent, so the frontmatter line looks like it worked")
	}
	// And the command still works when a person types it. Only the
	// automatic invocation was refused.
	if spliced.Body == "" {
		t.Error("the command itself was dropped; only its model invocation should be")
	}

	loop := invocableLoop(t, true)
	loop.Commands = list
	if got := loop.modelCommands(); len(got) != 1 || got[0] != "/safe" {
		t.Errorf("reachable = %v, want only /safe", got)
	}
	if refused := loop.refusedInvocable(); len(refused) != 1 || !strings.Contains(refused[0], "/spliced") {
		t.Errorf("refusedInvocable = %v, want it to name /spliced", refused)
	}
}

func writeFile(dir, name, body string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
}

// booksThenAnswers calls the Command tool once and then answers, which is
// what the tool's own description tells the model to do.
type booksThenAnswers struct {
	mu    sync.Mutex
	name  string
	turns int
	seen  []string
}

func (m *booksThenAnswers) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		user := ""
		for _, msg := range body.Messages {
			if msg.Role == "user" {
				var s string
				if json.Unmarshal(msg.Content, &s) == nil {
					user = s
				}
			}
		}

		m.mu.Lock()
		m.turns++
		first := m.turns == 1
		m.seen = append(m.seen, user)
		m.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		defer w.(http.Flusher).Flush()
		if first {
			args, _ := json.Marshal(map[string]string{"name": m.name})
			esc, _ := json.Marshal(string(args))
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"Command\",\"arguments\":%s}}]}}]}\n\n", esc)
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"done"}}]}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The half a test of the tool cannot see: the booking becomes a turn.
//
// The tool sets a field; SendMessage is what runs it. Proving the first
// without the second is how a feature ships booked and never run.
func TestABookedCommandRunsAsTheNextTurn(t *testing.T) {
	model := &booksThenAnswers{name: "/tidy-context"}
	loop := newSmartLoop(t, model.server(t).URL)
	loop.Config.SetModelInvocableRuntime(true)
	loop.Commands = []commands.Command{{
		Name: "tidy-context", Description: "tidy", ModelInvocable: true,
		Body: "SUMMARISE WHAT IS OPEN",
	}}
	loop.Tools.Register(NewCommandTool(loop))
	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "tidy up"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	model.mu.Lock()
	defer model.mu.Unlock()
	if model.turns < 2 {
		t.Fatalf("the model was asked %d times; the booked command never became a turn", model.turns)
	}
	// The command's body is what the second turn carried — which is the
	// command actually running, not a mention of its name.
	ran := false
	for _, u := range model.seen[1:] {
		if strings.Contains(u, "SUMMARISE WHAT IS OPEN") {
			ran = true
		}
	}
	if !ran {
		t.Errorf("no later turn carried the command's body; the turns were %q", model.seen)
	}

	// And the booking is gone, so the next ordinary message is ordinary.
	if line, booked := loop.takePendingCommand("s1"); booked {
		t.Errorf("the booking survived the run: %q", line)
	}
}
