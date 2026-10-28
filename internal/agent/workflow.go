package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"localcode/internal/userdirs"
)

// Saved workflows: an orchestration plan kept in a file and run by name.
//
// A person who reviews every branch the same way should not have to ask a
// model to write the same plan every time, and a plan a model rewrites
// each time is not the same plan. So a plan can be saved, given a name and
// a description, and run again with "/workflow run <name> [args]", by the
// person, with no model turn in between: the plan that runs is the file.
//
// The model does not run saved workflows itself in this version. A tool
// that can either take a plan or look one up by name is the tool with
// modes background.go argues against, its permission prompt would be
// written before the file it describes is read, and a project's
// description would land in a tool description the model reads as the
// product's own words.

// workflowMaxBytes bounds a workflow file. A plan is eight stages of a few
// lines each; a file a hundred times that is not a plan.
const workflowMaxBytes = 256 << 10

// workflowNamePattern is what a workflow may be called: what it is invoked
// by, and the file it lives in.
var workflowNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Workflow is one saved plan, as the file holds it.
type Workflow struct {
	// Name is how it is run, and must be the file's name without .json.
	Name string `json:"name"`
	// Description says what it is for, in /workflow list.
	Description string `json:"description,omitempty"`
	// Args describes what to pass after the name. Empty means the workflow
	// takes no arguments, and passing some is then refused rather than
	// ignored.
	Args string `json:"args,omitempty"`
	// Plan is the plan. Its goal and prompts may use {{args}}, and a
	// fanout may spread over the arguments with over: ["$args"].
	Plan Plan `json:"plan"`

	path  string
	scope string
	hash  string
	err   error
}

const (
	scopeProject = "project"
	scopeUser    = "user"
)

// workflowDir is one directory workflows are read from, and whose they are.
type workflowDir struct {
	dir   string
	scope string
}

// workflowDirs is where this conversation's workflows are: its own
// workspace's .localcode/workflows, then the home's. Per conversation
// rather than per daemon, because a daemon serves several projects and a
// project's workflows are about that project.
func (l *Loop) workflowDirs(sessionID string) []workflowDir {
	dirs := userdirs.WorkflowDirs(l.SessionDir(sessionID), l.homeDir())
	out := []workflowDir{{dir: dirs[0], scope: scopeProject}}
	if len(dirs) > 1 {
		out = append(out, workflowDir{dir: dirs[1], scope: scopeUser})
	}
	return out
}

// homeDir is the person's home directory: the one memory uses when it was
// configured, the process's otherwise.
func (l *Loop) homeDir() string {
	if l.Memory != nil && l.Memory.Home != "" {
		return l.Memory.Home
	}
	home, _ := os.UserHomeDir()
	return home
}

// workflows lists every workflow this conversation can run, a project's
// ahead of the home's and the first of a name winning. One that cannot be
// read or parsed is listed with the reason, so a broken file is seen
// rather than silently missing.
func (l *Loop) workflows(sessionID string) []Workflow {
	var out []Workflow
	seen := map[string]bool{}
	for _, d := range l.workflowDirs(sessionID) {
		entries, err := os.ReadDir(d.dir)
		if err != nil {
			continue
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			stem := strings.TrimSuffix(n, ".json")
			if seen[stem] {
				continue
			}
			seen[stem] = true
			out = append(out, readWorkflow(filepath.Join(d.dir, n), d.scope))
		}
	}
	return out
}

// findWorkflow is the workflow of that name this conversation would run.
func (l *Loop) findWorkflow(sessionID, name string) (Workflow, error) {
	if !workflowNamePattern.MatchString(name) {
		return Workflow{}, fmt.Errorf("%q is not a workflow name: lowercase letters, digits and hyphens", name)
	}
	for _, d := range l.workflowDirs(sessionID) {
		path := filepath.Join(d.dir, name+".json")
		if _, err := os.Lstat(path); err == nil {
			wf := readWorkflow(path, d.scope)
			return wf, wf.err
		}
	}
	return Workflow{}, fmt.Errorf("no workflow called %q here; /workflow lists the ones there are", name)
}

// readWorkflow reads and parses one file. The hash is of the bytes that
// were parsed, so a permission given for it is a permission for exactly
// the plan that runs.
func readWorkflow(path, scope string) Workflow {
	wf := Workflow{path: path, scope: scope, Name: strings.TrimSuffix(filepath.Base(path), ".json")}
	data, err := readBounded(path, workflowMaxBytes)
	if err != nil {
		wf.err = err
		return wf
	}
	sum := sha256.Sum256(data)
	wf.hash = hex.EncodeToString(sum[:])[:12]

	var parsed Workflow
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		wf.err = fmt.Errorf("%s does not parse: %v", path, err)
		return wf
	}
	if parsed.Name != wf.Name {
		wf.err = fmt.Errorf("%s is called %q inside; a workflow's name must be its file's name", path, parsed.Name)
		return wf
	}
	wf.Description, wf.Args, wf.Plan = parsed.Description, parsed.Args, parsed.Plan
	return wf
}

// readBounded reads a regular file of at most limit bytes.
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d KiB, which no plan is", path, limit>>10)
	}
	return data, nil
}

// usesArgs reports whether the plan refers to its arguments anywhere.
func (p Plan) usesArgs() bool {
	if strings.Contains(p.Goal, "{{args}}") {
		return true
	}
	for _, s := range p.Stages {
		if strings.Contains(s.Prompt, "{{args}}") || isArgsOver(s.Over) {
			return true
		}
	}
	return false
}

func isArgsOver(over []string) bool {
	return len(over) == 1 && over[0] == "$args"
}

// check is the static half of validation, the part that needs no
// arguments: the plan against the roster, with {{args}} and $args allowed,
// and the arguments it uses against the ones it says it takes. Run by
// /workflow list and show, and again before every run after the arguments
// are in.
func (wf Workflow) check(agents []string) error {
	if wf.err != nil {
		return wf.err
	}
	if wf.Args == "" && wf.Plan.usesArgs() {
		return errors.New(`the plan uses {{args}} or $args, so the file has to say what to pass in "args"`)
	}
	return wf.Plan.Validate(Limits{Agents: agents, Args: true})
}

// expand is the plan with these arguments in it: every fanout over $args
// becomes the list of arguments, and {{args}} is filled in when each
// prompt is rendered, in the same single pass as the other substitutions,
// so nothing in the arguments is ever read as a template.
func (wf Workflow) expand(args string) (Plan, error) {
	args = strings.TrimSpace(args)
	if wf.Args == "" && args != "" {
		return Plan{}, fmt.Errorf("workflow %q takes no arguments", wf.Name)
	}
	if wf.Args != "" && args == "" {
		return Plan{}, fmt.Errorf("workflow %q takes arguments: %s", wf.Name, wf.Args)
	}
	p := wf.Plan
	p.Stages = append([]Stage(nil), wf.Plan.Stages...)
	p.args, p.workflow = args, true
	for i, s := range p.Stages {
		if !isArgsOver(s.Over) {
			continue
		}
		items, err := splitArgs(args)
		if err != nil {
			return Plan{}, err
		}
		switch {
		case len(items) == 0:
			return Plan{}, fmt.Errorf("stage %q spreads over the arguments, and there are none", s.Name)
		case len(items) > maxFanout:
			return Plan{}, fmt.Errorf("stage %q spreads over the arguments: %d given, the limit is %d", s.Name, len(items), maxFanout)
		case len(items) == 1 && strings.HasPrefix(items[0], "$"):
			// One item starting with "$" is how a reference to an earlier
			// stage is written, so it would be read as one.
			return Plan{}, fmt.Errorf("stage %q spreads over the arguments, and a single argument cannot start with $ (it would read as a reference to a stage); quote it with another argument or drop the $", s.Name)
		}
		p.Stages[i].Over = items
	}
	return p, nil
}

// splitArgs is the arguments as fanout items: separated by spaces, tabs or
// line breaks, with single or double quotes keeping one item together, and
// a repeat (case and spacing ignored) dropped.
func splitArgs(args string) ([]string, error) {
	var items []string
	seen := map[string]bool{}
	var cur strings.Builder
	var quote rune
	have := false
	flush := func() {
		if have {
			v := cur.String()
			if k := itemKey(v); k != "" && !seen[k] {
				seen[k] = true
				items = append(items, v)
			}
		}
		cur.Reset()
		have = false
	}
	for _, r := range args {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote, have = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("the arguments have an unclosed %c", quote)
	}
	flush()
	return items, nil
}
