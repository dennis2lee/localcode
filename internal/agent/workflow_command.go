package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/tools"
	"localcode/internal/userdirs"
)

// /workflow: list, show, save, delete and run saved workflows.
//
// "/workflow run <name> [args]" runs a saved plan without a model turn. It
// is recorded the way an auto-delegated prompt is: the person's line, then
// localcode's reply, both in the history the model sees next, and the
// reply written on every way out, a refusal or a stop included, so the
// rebuilt history never holds a question with no answer.
//
// A workflow in the home directory is the person's own file and runs when
// they type its name. A project's arrives with a clone and can be edited by
// any agent that can write the project, so it asks first, with the
// question bound to the file's content: an "always" for it answers only
// that exact file, and an edited file asks again.

const workflowUsage = "usage: /workflow [list] | show <name> | run <name> [args] | resume [run] | save <name> [--user] [--force] [description] | delete <name> [--user]"

func (l *Loop) routeWorkflow(ctx context.Context, sessionID, text string) (bool, error) {
	// Cut at any whitespace and matched in any case, as the daemon's
	// heldUntilIdle reads it: a form one accepts and the other does not
	// is one that runs at the wrong time.
	first, rest := cutWord(strings.TrimSpace(text))
	if !strings.EqualFold(first, "/workflow") {
		return false, nil
	}
	sub, rest := cutWord(strings.TrimSpace(rest))

	switch strings.ToLower(sub) {
	case "", "list":
		return true, l.replyLocal(sessionID, text, l.workflowList(ctx, sessionID))
	case "show":
		return true, l.replyLocal(sessionID, text, l.workflowShow(ctx, sessionID, strings.TrimSpace(rest)))
	case "save":
		return true, l.replyLocal(sessionID, text, l.workflowSave(sessionID, rest))
	case "delete":
		return true, l.replyLocal(sessionID, text, l.workflowDelete(sessionID, rest))
	case "run":
		return true, l.runWorkflowCommand(ctx, sessionID, text, rest)
	case "resume":
		return true, l.resumeWorkflowCommand(ctx, sessionID, text, strings.TrimSpace(rest))
	default:
		return true, l.replyLocal(sessionID, text, workflowUsage)
	}
}

// cutWord splits off the first whitespace-separated word.
func cutWord(s string) (string, string) {
	s = strings.TrimLeft(s, " \t\r\n")
	i := strings.IndexAny(s, " \t\r\n")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}

func (l *Loop) delegatableNames(ctx context.Context) []string {
	var names []string
	for name := range l.DelegatableAgents(ctx) {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (l *Loop) workflowList(ctx context.Context, sessionID string) string {
	all := l.workflows(sessionID)
	if len(all) == 0 {
		dirs := l.workflowDirs(sessionID)
		var where []string
		for _, d := range dirs {
			where = append(where, d.dir)
		}
		return "No saved workflows. They are read from " + strings.Join(where, " and ") +
			". \"/workflow save <name>\" saves the last plan Orchestrate ran in this conversation."
	}
	agents := l.delegatableNames(ctx)
	var b strings.Builder
	b.WriteString("Saved workflows:\n")
	for _, wf := range all {
		fmt.Fprintf(&b, "* %s (%s)", wf.Name, wf.scope)
		if wf.Description != "" {
			fmt.Fprintf(&b, ": %s", oneLine(wf.Description))
		}
		if wf.Args != "" {
			fmt.Fprintf(&b, " [args: %s]", oneLine(wf.Args))
		}
		if err := wf.check(agents); err != nil {
			fmt.Fprintf(&b, " (cannot run here: %v)", err)
		}
		b.WriteString("\n")
	}
	b.WriteString("Run one with \"/workflow run <name> [args]\".")
	return b.String()
}

func (l *Loop) workflowShow(ctx context.Context, sessionID, name string) string {
	if name == "" {
		return workflowUsage
	}
	wf, err := l.findWorkflow(sessionID, name)
	if err != nil {
		return err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s), %s\n", wf.Name, wf.scope, wf.path)
	if wf.Description != "" {
		fmt.Fprintf(&b, "%s\n", wf.Description)
	}
	if wf.Args != "" {
		fmt.Fprintf(&b, "args: %s\n", wf.Args)
	} else {
		b.WriteString("takes no arguments\n")
	}
	if err := wf.check(l.delegatableNames(ctx)); err != nil {
		fmt.Fprintf(&b, "cannot run here: %v\n", err)
	}
	plan, _ := json.MarshalIndent(wf.Plan, "", "  ")
	fmt.Fprintf(&b, "```json\n%s\n```", plan)
	return b.String()
}

// workflowFlags pulls --user and --force out of the words after a name.
func workflowFlags(rest string) (name string, user, force bool, words []string) {
	for _, w := range strings.Fields(rest) {
		switch {
		case w == "--user":
			user = true
		case w == "--force":
			force = true
		case name == "":
			name = w
		default:
			words = append(words, w)
		}
	}
	return name, user, force, words
}

// workflowDirFor is the directory save and delete write in: the
// project's, or with --user the home's. When the workspace is the home
// directory the two are one directory, listed once as the project's, and
// --user means that one. "" when there is no home to write in.
func (l *Loop) workflowDirFor(sessionID string, user bool) string {
	dirs := l.workflowDirs(sessionID)
	switch {
	case !user:
		return dirs[0].dir
	case len(dirs) > 1:
		return dirs[1].dir
	case l.homeDir() != "" && userdirs.SameDirectory(l.SessionDir(sessionID), l.homeDir()):
		return dirs[0].dir
	}
	return ""
}

func (l *Loop) workflowSave(sessionID, rest string) string {
	name, user, force, words := workflowFlags(rest)
	if !workflowNamePattern.MatchString(name) {
		return "say what to call it: /workflow save <name> [--user] [--force] [description], a name of lowercase letters, digits and hyphens"
	}
	plan, from, ok := l.lastRunPlan(sessionID)
	if !ok {
		return "nothing to save: no orchestration plan has run in this conversation yet"
	}
	if from != "" {
		// Its prompts still say {{args}}, and the arguments it was run
		// with are not part of it: saved as it stands it would be a
		// workflow that cannot run.
		return fmt.Sprintf("the last plan that ran was the saved workflow %q, which is a file already: \"/workflow show %s\" says where", from, from)
	}
	dir := l.workflowDirFor(sessionID, user)
	if dir == "" {
		return "there is no home directory to save a --user workflow in"
	}
	if !user {
		if why := linkedWorkflowDir(dir); why != "" {
			return "not saved: " + why
		}
	}
	path := filepath.Join(dir, name+".json")
	if _, err := os.Lstat(path); err == nil && !force {
		return fmt.Sprintf("%s already exists; add --force to replace it", path)
	}
	wf := Workflow{Name: name, Description: strings.Join(words, " "), Plan: plan}
	data, _ := json.MarshalIndent(wf, "", "  ")
	data = append(data, '\n')
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Sprintf("could not save: %v", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+name+"-*")
	if err != nil {
		return fmt.Sprintf("could not save: %v", err)
	}
	if _, err := tmp.Write(data); err == nil {
		err = tmp.Close()
	} else {
		tmp.Close()
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Sprintf("could not save: %v", err)
	}
	return fmt.Sprintf("Saved %s. It runs with \"/workflow run %s\". To give it arguments, write {{args}} where they go (or over: [\"$args\"] on a fanout) and say what to pass in \"args\".\n```json\n%s```",
		path, name, data)
}

// linkedWorkflowDir is why a project's workflow directory is not one save
// or delete may write in, or "". A repository that ships .localcode or
// .localcode/workflows as a symbolic link puts those writes wherever its
// author pointed it, the home's own workflows included, so they are
// refused there. Reading through it is left alone: what runs from it
// still asks, as any project workflow does.
func linkedWorkflowDir(dir string) string {
	for _, p := range []string{filepath.Dir(dir), dir} {
		if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Sprintf("%s is a symbolic link, and save and delete write only in the project's own directory (--user writes in the home's)", p)
		}
	}
	return ""
}

// lastRunPlan is the plan of the last orchestration run this conversation
// started, read from the run's own task record, and the saved workflow it
// came from, if it did.
func (l *Loop) lastRunPlan(sessionID string) (Plan, string, bool) {
	evs, err := l.Store.Events(sessionID, 0)
	if err != nil {
		return Plan{}, "", false
	}
	// A run a /rewind took back is not this conversation's any more.
	evs = applyRewinds(evs)
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		if e.Type != events.TypeTaskSpawned || e.Data["agent"] != runSessionAgent {
			continue
		}
		raw, err := json.Marshal(e.Data["plan"])
		if err != nil {
			continue
		}
		var p Plan
		if json.Unmarshal(raw, &p) == nil && len(p.Stages) > 0 {
			from, _ := e.Data["workflow"].(string)
			return p, from, true
		}
	}
	return Plan{}, "", false
}

func (l *Loop) workflowDelete(sessionID, rest string) string {
	name, user, _, _ := workflowFlags(rest)
	if !workflowNamePattern.MatchString(name) {
		return "say which: /workflow delete <name> [--user]"
	}
	dir := l.workflowDirFor(sessionID, user)
	if dir == "" {
		return "there is no home directory to delete a --user workflow from"
	}
	if !user {
		if why := linkedWorkflowDir(dir); why != "" {
			return "not deleted: " + why
		}
	}
	path := filepath.Join(dir, name+".json")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			where := "this project"
			if user {
				where = "your home directory"
			}
			return fmt.Sprintf("there is no workflow %q in %s", name, where)
		}
		return fmt.Sprintf("could not delete %s: %v", path, err)
	}
	return "Deleted " + path + "."
}

// runWorkflowRefusal is why a person's /workflow run cannot start here, or "".
func (l *Loop) runWorkflowRefusal(ctx context.Context) string {
	switch {
	case Unattended(ctx):
		return "a saved workflow is not run in a turn nobody is watching; run it yourself"
	case inCommandRun(ctx):
		return "a saved workflow is run by a person typing it, not by a command the model booked"
	case inOrchestration(ctx):
		return "a plan cannot run a plan"
	case inDebate(ctx):
		return "not inside a debate"
	case !config.OrchestrateFor(ctx, l.Config):
		return "orchestration is off. Turn it on with \"/orchestrate on\", or in the settings window."
	case l.Tasks == nil:
		return "this build cannot delegate, so there is nothing to orchestrate"
	case len(l.DelegatableAgents(ctx)) < 2:
		return "there is nobody to delegate a stage to: the built-in specialists need a profile in config.json to run on"
	}
	if l.Tools != nil && l.Tools.Decide(ctx, orchestrateToolName) == tools.DecisionDeny {
		return "Orchestrate is denied by a permission rule"
	}
	return ""
}

func (l *Loop) runWorkflowCommand(ctx context.Context, sessionID, line, rest string) error {
	name, args := cutWord(rest)
	if name == "" {
		return l.replyLocal(sessionID, line, workflowUsage)
	}
	if why := l.runWorkflowRefusal(ctx); why != "" {
		return l.replyLocal(sessionID, line, "Not run: "+why+".")
	}
	wf, err := l.findWorkflow(sessionID, name)
	if err != nil {
		return l.replyLocal(sessionID, line, "Not run: "+err.Error())
	}
	agents := l.delegatableNames(ctx)
	if err := wf.check(agents); err != nil {
		return l.replyLocal(sessionID, line, "Not run: "+err.Error())
	}
	plan, err := wf.expand(args)
	if err != nil {
		return l.replyLocal(sessionID, line, "Not run: "+err.Error())
	}
	if err := plan.Validate(Limits{Agents: agents, Args: true}); err != nil {
		return l.replyLocal(sessionID, line, "Not run: the plan was refused before anything ran: "+err.Error())
	}

	if wf.scope == scopeProject {
		ok, why := l.Tools.Permit(WithSessionID(ctx, sessionID), orchestrateToolName,
			"workflow:"+wf.Name+"@"+wf.hash, describeWorkflowRun(wf, plan))
		if !ok {
			return l.replyLocal(sessionID, line, "Not run: "+why+".")
		}
	}

	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": line})
	runID, out, err := l.Tasks.SpawnRun(ctx, sessionID, plan.renderedGoal(),
		map[string]any{"plan": plan, "workflow": wf.Name, "args": plan.args}, l.planWork(sessionID, plan))
	var reply string
	switch {
	case errors.Is(err, errLetGo):
		reply = letGoReply(fmt.Sprintf("Workflow %s (run %s)", wf.Name, runID), runID)
	case err != nil && runID == "":
		reply = fmt.Sprintf("Workflow %s did not start: %v", wf.Name, err)
	case err != nil:
		reply = fmt.Sprintf("Workflow %s stopped (run %s): %v\n\n%s", wf.Name, runID, err, out)
	default:
		reply = fmt.Sprintf("Workflow %s (run %s). localcode wrote this report from what the stages' agents answered:\n\n%s", wf.Name, runID, out)
	}
	reply = capToolResult(reply, 0)
	l.appendDelegatedTurn(sessionID, line, reply)
	return l.replyText(sessionID, reply)
}

// letGoReply is what a person's run says when it was let go: where the
// report will be, since there is none yet to show.
func letGoReply(what, runID string) string {
	return fmt.Sprintf("%s was let go and goes on in the background. Its report will be the last entry of the run's task: \"/tasks %s\". Do not start it again.", what, runID)
}

// describeWorkflowRun is the permission question for a project workflow:
// what it will run, under which roles, at what ceiling.
func describeWorkflowRun(wf Workflow, p Plan) string {
	agents := map[string]bool{}
	roles := map[string]bool{}
	for _, s := range p.Stages {
		agents[s.Agent] = true
		role := s.Role
		if role == "" {
			role = "readonly"
		}
		roles[role] = true
	}
	// What localcode knows first, and the repository's own words last: a
	// long goal must not push the roles off the end of the question.
	return fmt.Sprintf("run the project workflow %q (sha256 %s...) from %s: %d stages by %s, as %s, at most %d agent turns, %s per agent turn and %s for the run. Its goal: %s",
		wf.Name, wf.hash[:12], wf.path, len(p.Stages),
		strings.Join(sortedKeys(agents, len(agents)), ", "), strings.Join(sortedKeys(roles, len(roles)), ", "),
		p.Launches(), stageTimeout, runTimeout, firstLine(p.renderedGoal()))
}

// runRecord is one orchestration run this conversation started, as its
// task record holds it.
type runRecord struct {
	id       string
	plan     Plan
	workflow string
}

// findRun is the run to resume: the one named, or the last this
// conversation started. Taken from the conversation's own record, so the
// plan that is resumed is the plan that ran, not whatever a file says now.
func (l *Loop) findRun(sessionID, id string) (runRecord, error) {
	evs, err := l.Store.Events(sessionID, 0)
	if err != nil {
		return runRecord{}, err
	}
	// Not one a /rewind took back: rewinding a run that edited files puts
	// the files back, and resuming it would make those edits again.
	evs = applyRewinds(evs)
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		if e.Type != events.TypeTaskSpawned || e.Data["agent"] != runSessionAgent {
			continue
		}
		taskID, _ := e.Data["task_id"].(string)
		if id != "" && taskID != id {
			continue
		}
		raw, err := json.Marshal(e.Data["plan"])
		if err != nil {
			continue
		}
		var p Plan
		if json.Unmarshal(raw, &p) != nil || len(p.Stages) == 0 {
			continue
		}
		rec := runRecord{id: taskID, plan: p}
		rec.workflow, _ = e.Data["workflow"].(string)
		if rec.workflow != "" {
			rec.plan.workflow = true
			rec.plan.args, _ = e.Data["args"].(string)
		}
		return rec, nil
	}
	if id != "" {
		return runRecord{}, fmt.Errorf("there is no run %s in this conversation", id)
	}
	return runRecord{}, errors.New("no orchestration has run in this conversation")
}

// stoppedAtCeiling reports whether a run's report says it stopped at the
// agent ceiling.
//
// The report's last event says so with "ceiling". One written before that
// key existed, by v0.169.0, says it only in words, so its last line is
// read as well: without that, a v0.169.0 run stopped at the ceiling is
// resumed once, to stop at the same place.
func (l *Loop) stoppedAtCeiling(runID string) bool {
	evs, err := l.Store.Events(runID, 0)
	if err != nil {
		return false
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == events.TypeMessagePartEnd {
			if evs[i].Data["ceiling"] == true {
				return true
			}
			text, _ := evs[i].Data["text"].(string)
			return endsAtCeiling(text)
		}
	}
	return false
}

// ceilingStops are the last lines of a report that stopped at the ceiling,
// as runReport.String writes them around the stop reasons in
// orchestrate_run.go and orchestrate_pipeline.go.
var ceilingStops = []*regexp.Regexp{
	regexp.MustCompile(`^The run did not finish: stopped at .+: the run had already launched \d+ of its \d+ agents, so none of its \d+ items could run$`),
	regexp.MustCompile(`^The run did not finish: stopped before .+: the run had launched \d+ agents and this stage needs \d+ more, past the limit of \d+$`),
	regexp.MustCompile(`^The run did not finish: stopped in .+: round \d+ settled nothing and the run had launched \d+ agents, past the limit of \d+$`),
}

// endsAtCeiling reports whether a report's last line is a stop at the
// ceiling. Only the last line: the answers above it are model text, and
// the stop line is always the last thing a report says.
func endsAtCeiling(report string) bool {
	trimmed := strings.TrimRight(report, "\n")
	last := trimmed[strings.LastIndex(trimmed, "\n")+1:]
	for _, re := range ceilingStops {
		if re.MatchString(last) {
			return true
		}
	}
	return false
}

// runLineage is the run a chain of resumes started from, and every run in
// that chain: the original, its resumes, their resumes. Every one of them
// reuses the same completed units and reruns the same unfinished ones, so
// a resume of any of them while another is running would run those units
// twice at once.
func (l *Loop) runLineage(sessionID, runID string) (string, []string) {
	from := map[string]string{}
	evs, _ := l.Store.Events(sessionID, 0)
	for _, e := range evs {
		if e.Type != events.TypeTaskSpawned || e.Data["agent"] != runSessionAgent {
			continue
		}
		id, _ := e.Data["task_id"].(string)
		parent, _ := e.Data["resumed_from"].(string)
		if id != "" {
			from[id] = parent
		}
	}
	rootOf := func(id string) string {
		// Bounded by the number of runs, so a malformed record that
		// names itself cannot loop.
		for range len(from) + 1 {
			p := from[id]
			if p == "" {
				return id
			}
			id = p
		}
		return id
	}
	root := rootOf(runID)
	var members []string
	for id := range from {
		if rootOf(id) == root {
			members = append(members, id)
		}
	}
	return root, members
}

// liveRunOf is runID itself if it is still running, or another run of its
// lineage that is, or "".
func (l *Loop) liveRunOf(sessionID, runID string) string {
	running := map[string]bool{}
	for _, id := range l.Tasks.Running() {
		running[id] = true
	}
	if running[runID] {
		return runID
	}
	_, members := l.runLineage(sessionID, runID)
	sort.Strings(members)
	for _, id := range members {
		if running[id] {
			return id
		}
	}
	return ""
}

// writingStages names the stages of p that can change the workspace.
func writingStages(p Plan) []string {
	var out []string
	for _, s := range p.Stages {
		if s.Role == "builder" || s.Role == "runner" {
			out = append(out, s.Name)
		}
	}
	return out
}

func (l *Loop) resumeWorkflowCommand(ctx context.Context, sessionID, line, id string) error {
	notResumed := func(why string) error {
		return l.replyLocal(sessionID, line, "Not resumed: "+why)
	}
	if why := l.runWorkflowRefusal(ctx); why != "" {
		return notResumed(why + ".")
	}
	rec, err := l.findRun(sessionID, id)
	if err != nil {
		return notResumed(err.Error())
	}
	if sess, err := l.Store.Get(rec.id); err == nil && sess.ParentID != sessionID {
		// A fork copies the record and not the run: the run and its
		// units belong to the conversation it was forked from.
		return notResumed(fmt.Sprintf("run %s belongs to another conversation", rec.id))
	}
	if l.stoppedAtCeiling(rec.id) {
		return notResumed(fmt.Sprintf("run %s stopped at the %d agent turn ceiling, and the answers a resume reuses count toward that ceiling, so a resume would stop at the same place. Run the plan over fewer items, or split it", rec.id, maxRunAgents))
	}
	if live := l.liveRunOf(sessionID, rec.id); live == rec.id {
		return notResumed(fmt.Sprintf("run %s is still running", rec.id))
	} else if live != "" {
		// A resume that was let go is still running units of this run;
		// a second would run the ones it has not finished again.
		return notResumed(fmt.Sprintf("run %s is being resumed already, as run %s", rec.id, live))
	}
	// Held by lineage, not by run: resuming a run and resuming its resume
	// at the same moment would run the same units twice.
	lineage, _ := l.runLineage(sessionID, rec.id)
	l.mu.Lock()
	if l.resuming[lineage] {
		l.mu.Unlock()
		return notResumed(fmt.Sprintf("run %s is being resumed already", rec.id))
	}
	if l.resuming == nil {
		l.resuming = map[string]bool{}
	}
	l.resuming[lineage] = true
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.resuming, lineage)
		l.mu.Unlock()
	}()

	if err := rec.plan.Validate(Limits{Agents: l.delegatableNames(ctx), Args: rec.plan.workflow}); err != nil {
		return notResumed("the plan no longer validates here: " + err.Error())
	}
	cache := l.runJournal(rec.id)
	if writes := writingStages(rec.plan); len(writes) > 0 {
		// A unit that writes may have been cut off half way, and one that
		// finished may have had its changes reverted since: reusing or
		// rerunning either is a decision about this workspace now, so it
		// is asked every time.
		ok, why := l.Tools.PermitEachTime(WithSessionID(ctx, sessionID), orchestrateToolName, "resume:"+rec.id,
			fmt.Sprintf("resume run %s (%s), whose stages %s can change the workspace: %d finished answers that could be reused (those whose units are asked the same again) and everything else run again, at most %d agent turns",
				rec.id, firstLine(rec.plan.renderedGoal()), strings.Join(writes, ", "), len(cache), rec.plan.Launches()))
		if !ok {
			return notResumed(why + ".")
		}
	}

	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": line})
	spawned := map[string]any{"plan": rec.plan, "resumed_from": rec.id}
	if rec.workflow != "" {
		spawned["workflow"], spawned["args"] = rec.workflow, rec.plan.args
	}
	runID, out, err := l.Tasks.SpawnRun(withResume(ctx, cache), sessionID, rec.plan.renderedGoal(), spawned, l.planWork(sessionID, rec.plan))
	var reply string
	switch {
	case errors.Is(err, errLetGo):
		reply = letGoReply(fmt.Sprintf("The resume of run %s (run %s)", rec.id, runID), runID)
	case err != nil && runID == "":
		reply = fmt.Sprintf("Resuming run %s did not start: %v", rec.id, err)
	case err != nil:
		reply = fmt.Sprintf("Resumed run %s as run %s, which stopped: %v\n\n%s", rec.id, runID, err, out)
	default:
		reply = fmt.Sprintf("Resumed run %s as run %s. localcode wrote this report from what the stages' agents answered, reused or new:\n\n%s", rec.id, runID, out)
	}
	reply = capToolResult(reply, 0)
	l.appendDelegatedTurn(sessionID, line, reply)
	return l.replyText(sessionID, reply)
}
