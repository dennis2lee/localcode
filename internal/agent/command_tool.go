package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"localcode/internal/tools"
)

// The model running a command.
//
// Everything the model could reach until now was a tool. A command is a
// different thing: it is what a person types, and it can rewrite the
// conversation, spend money, or change what this session is allowed to
// do. So none of it was reachable, and one route in was closed on
// purpose — a delegated task's text used to walk the command table, and a
// task whose first line read "/permission-skip-all on" flipped the
// child's permission switch.
//
// What opens it is not a general capability but a list. A custom command
// or a skill says model_invocable in its own frontmatter; a built-in has
// no file, so it is named in config.json's model_commands. Nothing is
// reachable that somebody did not write down, and there is no wildcard to
// sweep a family in.
//
// The name carries its slash — "/compact", not "compact" — so a call is
// visibly a command rather than a word that happened to match one. That
// is also what the person types, which keeps one spelling for the thing.

const commandToolName = "Command"

// CommandTool runs one of the commands this session has been told the
// model may run.
type CommandTool struct{ loop *Loop }

func NewCommandTool(loop *Loop) CommandTool { return CommandTool{loop: loop} }

func (CommandTool) Name() string { return commandToolName }

func (t CommandTool) Description() string { return t.DescriptionFor(context.Background()) }

func (t CommandTool) DescriptionFor(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("Run one of this session's commands, the way the person would by typing it. " +
		"It runs as a turn of its own, immediately after the current one ends, in this same conversation — " +
		"so end your turn after calling this rather than carrying on. Available:\n")
	names := t.loop.modelCommands()
	for _, name := range names {
		if d := t.loop.commandDescription(name); d != "" {
			fmt.Fprintf(&b, "- %s: %s\n", name, d)
			continue
		}
		fmt.Fprintf(&b, "- %s\n", name)
	}
	if len(names) == 0 {
		b.WriteString("(none — nothing has been opted in)\n")
	}
	return b.String()
}

func (t CommandTool) InputSchema() json.RawMessage { return t.InputSchemaFor(context.Background()) }

func (t CommandTool) InputSchemaFor(ctx context.Context) json.RawMessage {
	enum, _ := json.Marshal(t.loop.modelCommands())
	return json.RawMessage(fmt.Sprintf(
		`{"type":"object","properties":{`+
			`"name":{"type":"string","enum":%s,"description":"the command, with its leading slash"},`+
			`"arguments":{"type":"string","description":"what to put after the command name; omit when it takes none"}`+
			`},"required":["name"]}`, enum))
}

// RequiresPermission is false, and the allowlist is why: what a command
// may do was decided when somebody wrote its name down, not per call. A
// prompt on every call would be one people learn to click through, which
// is worse than no prompt and a list they chose.
func (CommandTool) RequiresPermission(json.RawMessage) bool { return false }

func (t CommandTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	sessionID, ok := SessionIDFromContext(ctx)
	if !ok {
		return tools.Result{Content: "Command has no session context", IsError: true}
	}
	// Not from inside a command this same mechanism started. One command
	// booking the next is a loop with nothing bounding it, and the two
	// guards above it — the allowlist and the person who wrote it — do
	// not bound depth.
	if inCommandRun(ctx) {
		return tools.Result{
			Content: "a command run this way cannot run another. Say what should happen next instead.",
			IsError: true, Refused: true,
		}
	}

	var args struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tools.Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return tools.Result{Content: "say which command, with its leading slash", IsError: true}
	}
	// The slash is required rather than repaired. A model that wrote
	// "compact" may have meant the word; one that wrote "/compact" meant
	// the command, and that difference is the whole reason the name
	// carries it.
	if !strings.HasPrefix(name, "/") {
		return tools.Result{
			Content: fmt.Sprintf("%q is not a command. A command starts with a slash, as %q does.", name, "/"+name),
			IsError: true,
		}
	}
	allowed := t.loop.modelCommands()
	if !hasName(allowed, name) {
		return tools.Result{
			Content: fmt.Sprintf("%s is not a command this session may run. Available: %s",
				name, strings.Join(allowed, ", ")),
			IsError: true, Refused: true,
		}
	}

	line := name
	if a := strings.TrimSpace(args.Arguments); a != "" {
		line += " " + a
	}
	t.loop.setPendingCommand(sessionID, line)
	return tools.Result{Content: fmt.Sprintf(
		"Booked: %s runs as the next turn in this conversation, the moment this one ends. "+
			"End your turn now without doing its work yourself.", line)}
}

// modelCommands is every command this session may run, with slashes,
// sorted — the enum the model is shown and the list a call is checked
// against, which have to be the same list or the schema is advertising
// something the tool refuses.
func (l *Loop) modelCommands() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range l.Config.ModelCommandNames() {
		add(name)
	}
	for _, c := range l.Commands {
		if c.ModelInvocable {
			add("/" + c.Name)
		}
	}
	for _, s := range l.Skills {
		if s.ModelInvocable {
			add("/" + s.Name)
		}
	}
	sort.Strings(out)
	return out
}

// commandDescription is the one line a command carries, for the tool's
// own listing. Empty for a built-in, whose name is its description.
func (l *Loop) commandDescription(slashed string) string {
	name := strings.TrimPrefix(slashed, "/")
	for _, c := range l.Commands {
		if c.Name == name {
			return c.Description
		}
	}
	for _, s := range l.Skills {
		if s.Name == name {
			return s.Description
		}
	}
	for _, sc := range SlashCommands() {
		if sc.Name == name {
			return sc.Description
		}
	}
	return ""
}

// hasName reports whether the allowlist holds this exact name. Exact
// rather than fuzzy: the enum the model was shown is this same list, so a
// name that is not in it is not a near miss, it is a name the model
// invented.
func hasName(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}
