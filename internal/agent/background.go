package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"localcode/internal/config"
	"localcode/internal/tools"
	"localcode/internal/trace"
)

// Background delegation: launching sub-agents that run while the
// orchestrator keeps working, and picking their answers up afterwards.
//
// The Task tool already delegates, but synchronously — the calling turn
// stops at the tool boundary until the sub-agent has finished. That is the
// right shape for one question. It is the wrong shape for three, because
// three independent questions asked one after another take as long as all
// three added together, and the whole reason to send investigation to
// another context is that it can happen somewhere else.
//
// So: TaskBackground launches and returns a handle, TaskCollect waits and
// returns the answers. Two tools rather than one with a mode flag, because
// the model has to be able to call the first three times and the second
// once, and a single tool that sometimes blocks and sometimes does not is
// harder to describe than two that each do one thing.
//
// Both are Smart Agent tools: they are hidden unless it is on. Work
// running unattended in a session nobody is looking at is the part of this
// a user should be opting into rather than discovering.

// maxBackgroundTasks bounds how many launched-and-uncollected tasks one
// session may have.
//
// A ceiling rather than a queue, and it is deliberately low. Every
// outstanding task is a model burning tokens in a session the user is not
// reading, and the failure this guards against is not a burst — the
// semaphore in TaskManager already handles those — it is a model that
// launches work it never comes back for. Eight is more parallel
// investigation than any real request needs, so hitting it is a signal
// rather than a limit to design around, and the error says so.
const maxBackgroundTasks = 8

// taskOutcome is what a finished task left behind: its final answer, or
// why there isn't one.
type taskOutcome struct {
	text string
	err  error
}

// errTaskDetached is the outcome of a task whose parent session was
// deleted out from under it.
//
// It exists because a waiter can be woken by two different things and the
// caller has to be able to tell them apart. Cleanup used to close the
// channel without recording anything, and Wait read the missing map entry
// as a zero value: empty text, no error, collected. A collection could
// therefore report "finished without an answer" for a task that was never
// asked and never ran, which reads as a task that had nothing to say.
var errTaskDetached = errors.New("the session that launched this task was deleted")

// errSessionClosing is what an admission gets when the tree it is trying
// to join is being deleted. A refusal rather than a wait: the caller asked
// to start work in a conversation that is going away, and there is nothing
// for it to wait for.
func errSessionClosing(sessionID string) error {
	return fmt.Errorf("session %s is being deleted", sessionID)
}

// finish records a task's result and wakes anything waiting on it. Safe
// to call for a task nobody is waiting on, which is the common case: most
// tasks are spawned by a client, not by the model.
func (tm *TaskManager) finish(taskID, text string, runErr, ctxErr error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	out := taskOutcome{text: text}
	switch {
	case ctxErr != nil:
		out.err = fmt.Errorf("stopped before it finished")
	case runErr != nil:
		out.err = runErr
	}
	// A waiter is registered at spawn time for exactly the tasks that can
	// be collected. Recording an outcome for the others would keep a
	// second copy of every client-spawned answer, in memory, with nothing
	// that ever reads or deletes it.
	ch, collectable := tm.waiters[taskID]
	if !collectable {
		return
	}
	tm.results[taskID] = out
	close(ch)
	delete(tm.waiters, taskID)
}

// Wait blocks until taskID finishes and returns its final answer. The
// third value reports whether a terminal outcome was actually obtained:
// false means the caller gave up, not that the task failed.
//
// That distinction is what keeps a cancelled collection recoverable. The
// task keeps running — it belongs to the session, not to the turn that
// went away — so its answer is still coming, and the caller has to be able
// to tell "this task failed" from "I stopped waiting" in order to leave
// the id outstanding rather than consuming it. See TaskCollectTool.
//
// A task that has already finished returns immediately, which is what
// makes "launch three, collect three" work regardless of the order they
// happen to complete in.
func (tm *TaskManager) Wait(ctx context.Context, taskID string) (string, error, bool) {
	tm.mu.Lock()
	if out, done := tm.results[taskID]; done {
		delete(tm.results, taskID)
		tm.mu.Unlock()
		return out.text, out.err, true
	}
	ch, running := tm.waiters[taskID]
	tm.mu.Unlock()
	if !running {
		return "", fmt.Errorf("no such background task %q", taskID), true
	}

	select {
	case <-ch:
	case <-ctx.Done():
		return "", ctx.Err(), false
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()
	out, recorded := tm.results[taskID]
	delete(tm.results, taskID)
	if !recorded {
		// Woken with nothing recorded means the wake was cleanup, not
		// completion. Reported as terminal, because it is — the task is
		// gone and nothing more is coming — but never as an answer.
		return "", errTaskDetached, true
	}
	return out.text, out.err, true
}

// SpawnBackground launches a task and remembers it as one this session
// still owes itself a collection for.
// One admission window covers the spawn and the pending entry both. They
// used to be separate, so a deletion could complete in between and the
// late append would put an entry back into a session's books after they
// had been closed.
func (tm *TaskManager) SpawnBackground(launchCtx context.Context, parentSessionID, agentName, prompt, traceID string) (string, error) {
	tm.mu.Lock()
	outstanding := len(tm.pending[parentSessionID])
	tm.mu.Unlock()
	if outstanding >= maxBackgroundTasks {
		return "", fmt.Errorf("this session already has %d background tasks running and uncollected; collect them before launching more", outstanding)
	}

	if !tm.loop.lifecycle.admit(parentSessionID) {
		return "", errSessionClosing(parentSessionID)
	}
	defer tm.loop.lifecycle.admitted(parentSessionID)

	taskID, err := tm.spawn(launchCtx, parentSessionID, agentName, prompt, traceID, true)
	if err != nil {
		return "", err
	}
	tm.mu.Lock()
	tm.pending[parentSessionID] = append(tm.pending[parentSessionID], taskID)
	tm.mu.Unlock()
	return taskID, nil
}

// peekPending returns the background tasks a session is waiting on: all of
// them, or just the one named. Nothing is removed.
//
// Removal is dropPending's job, and it happens per task, after that task
// has actually been collected. Taking the ids out up front lost them: a
// cancelled collection left the tasks running and their answers recorded,
// with no id outstanding to ask for them by, so the work was done and
// permanently unreachable.
func (tm *TaskManager) peekPending(parentSessionID, taskID string) []string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	all := tm.pending[parentSessionID]
	if taskID == "" {
		return append([]string(nil), all...)
	}
	for _, id := range all {
		if id == taskID {
			return []string{id}
		}
	}
	return nil
}

// dropPending forgets one collected task.
func (tm *TaskManager) dropPending(parentSessionID, taskID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	all := tm.pending[parentSessionID]
	for i, id := range all {
		if id == taskID {
			rest := append(append([]string(nil), all[:i]...), all[i+1:]...)
			if len(rest) == 0 {
				delete(tm.pending, parentSessionID)
			} else {
				tm.pending[parentSessionID] = rest
			}
			return
		}
	}
}

// StopSession ends every background task in ids and waits for the work to
// unwind. ids is a claimed tree: nothing can be admitted into it for as
// long as the caller holds the claim, which is what makes stopping the
// set it names the same as stopping the set that exists. See
// Loop.claimSessionTree.
//
// This is the half of deleting a conversation that is not about records.
// A background task runs in a session of its own, rooted in the manager's
// context rather than the launching turn's, precisely so it survives the
// turn that started it; the cost of that is that it also survived the
// user deleting the conversation. An `implement` task could go on editing
// files, for an instruction given in a conversation that no longer
// exists, with its final status appended to a session that is not there
// to receive it.
//
// Recursive, because a task can delegate: stopping the children a session
// launched is not enough if one of those launched more. The recursion is
// in the claim rather than here.
//
// Blocking, because the caller is about to remove the session logs these
// goroutines are writing to. Returning while a turn is mid tool call
// would trade an orphan process for a torn file.
func (tm *TaskManager) StopSession(ids []string) {
	tm.mu.Lock()
	var cancels []context.CancelFunc
	var waits []chan struct{}
	for _, id := range ids {
		if c, ok := tm.cancels[id]; ok {
			cancels = append(cancels, c)
		}
		if ch, ok := tm.done[id]; ok {
			waits = append(waits, ch)
		}
	}
	tm.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, ch := range waits {
		<-ch
	}

	// Only once nothing is running: a task that was mid flight until a
	// moment ago may have recorded an outcome on its way out, and the
	// point of this ordering is that there is nothing left to record one
	// after the books are closed.
	for _, id := range ids {
		tm.forgetSession(id)
		tm.forgetTask(id)
	}
}

// forgetTask drops what is remembered about one task by its own id, as
// opposed to by the session that launched it. A descendant being removed
// with its ancestor is not in anybody's pending list any more, but it can
// still hold a waiter and a result of its own.
func (tm *TaskManager) forgetTask(taskID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	delete(tm.results, taskID)
	if ch, ok := tm.waiters[taskID]; ok {
		close(ch)
		delete(tm.waiters, taskID)
	}
}

// forgetSession drops everything remembered about a parent session's
// background tasks — called when the session is deleted, so a task
// launched and never collected does not keep its answer forever.
//
// The waiter goes with the result, and that is the part that is easy to
// miss. Deleting only the recorded results left a waiter behind for any
// child still running, and finish stores an outcome whenever it finds a
// waiter: the task finished after its parent was deleted, wrote its answer
// into the map, and left it there with no pending id to collect it by and
// no later cleanup that would look for it. Taking the waiter first is what
// makes cleanup final, because finish under the same lock then sees no
// waiter and records nothing.
//
// Closing rather than dropping the channel matters for the same reason:
// anything still blocked in Wait is woken instead of held until its own
// context expires.
func (tm *TaskManager) forgetSession(parentSessionID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, id := range tm.pending[parentSessionID] {
		delete(tm.results, id)
		if ch, ok := tm.waiters[id]; ok {
			close(ch)
			delete(tm.waiters, id)
		}
	}
	delete(tm.pending, parentSessionID)
}

// TaskBackgroundTool launches a sub-agent and returns immediately.
type TaskBackgroundTool struct {
	manager *TaskManager
	agents  func(context.Context) map[string]config.AgentConfig
}

func NewTaskBackgroundTool(manager *TaskManager, agents func(context.Context) map[string]config.AgentConfig) TaskBackgroundTool {
	return TaskBackgroundTool{manager: manager, agents: agents}
}

func (t TaskBackgroundTool) Name() string { return "TaskBackground" }

func (t TaskBackgroundTool) Description() string { return t.DescriptionFor(context.Background()) }

func (t TaskBackgroundTool) DescriptionFor(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("Start a sub-agent working in the background and return straight away with its task id. " +
		"Use this to run two or more independent pieces of investigation at once, then call TaskCollect to " +
		"get the answers. For a single question, use Task instead: it is the same thing without the " +
		"bookkeeping. Available agents:\n")
	writeAgentList(&b, t.agents(ctx))
	return b.String()
}

func (t TaskBackgroundTool) InputSchema() json.RawMessage {
	return t.InputSchemaFor(context.Background())
}

func (t TaskBackgroundTool) InputSchemaFor(ctx context.Context) json.RawMessage {
	return delegationSchema(agentNamesOf(t.agents(ctx)))
}

// RequiresPermission is false for the same reason Task's is: starting a
// sub-agent has no effect of its own, and every tool it goes on to call
// is gated by that sub-agent's own permission checks.
func (t TaskBackgroundTool) RequiresPermission(json.RawMessage) bool { return false }

func (t TaskBackgroundTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	var args struct {
		Agent  string `json:"agent"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tools.Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}
	agents := t.agents(ctx)
	if _, ok := agents[args.Agent]; !ok {
		return tools.Result{
			Content: fmt.Sprintf("unknown agent %q. Available: %s", args.Agent, strings.Join(agentNamesOf(agents), ", ")),
			IsError: true,
		}
	}
	// The same depth guard the synchronous Task tool applies. Without it,
	// background delegation would be the way around it: a sub-agent that
	// cannot call Task could still launch one and never collect it, and
	// nothing would be waiting to notice.
	if depth := taskDepthFromContext(ctx); depth >= maxTaskDepth {
		return tools.Result{
			Content: fmt.Sprintf("max sub-agent delegation depth (%d) reached; refusing to delegate further", maxTaskDepth),
			IsError: true,
		}
	}
	parentSessionID, ok := SessionIDFromContext(ctx)
	if !ok {
		return tools.Result{Content: "TaskBackground has no session context", IsError: true}
	}

	// The trace id travels with the launch so the background work shows
	// up under the turn that started it rather than as an orphan.
	taskID, err := t.manager.SpawnBackground(ctx, parentSessionID, args.Agent, args.Prompt, trace.ID(ctx))
	if err != nil {
		return tools.Result{Content: fmt.Sprintf("could not start %q in the background: %v", args.Agent, err), IsError: true}
	}
	return tools.Result{Content: fmt.Sprintf(
		"started %s in the background as task %s. Keep working; call TaskCollect when you need its answer.",
		args.Agent, taskID)}
}

// TaskCollectTool waits for background tasks and returns their answers.
type TaskCollectTool struct {
	manager *TaskManager
}

func NewTaskCollectTool(manager *TaskManager) TaskCollectTool {
	return TaskCollectTool{manager: manager}
}

func (t TaskCollectTool) Name() string { return "TaskCollect" }

func (t TaskCollectTool) Description() string {
	return "Wait for background sub-agents started with TaskBackground and return what they found. " +
		"With no task_id it collects every outstanding one, which is the usual call: launch the " +
		"independent pieces, then collect once."
}

func (t TaskCollectTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string","description":"collect only this task; omit to collect every outstanding background task"}}}`)
}

func (t TaskCollectTool) RequiresPermission(json.RawMessage) bool { return false }

func (t TaskCollectTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	var args struct {
		TaskID string `json:"task_id"`
	}
	// An absent body is the common call ("collect everything"), so a
	// missing or empty input is not an error here.
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return tools.Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
		}
	}
	parentSessionID, ok := SessionIDFromContext(ctx)
	if !ok {
		return tools.Result{Content: "TaskCollect has no session context", IsError: true}
	}

	ids := t.manager.peekPending(parentSessionID, args.TaskID)
	if len(ids) == 0 {
		if args.TaskID != "" {
			return tools.Result{Content: fmt.Sprintf("no outstanding background task %q in this session", args.TaskID), IsError: true}
		}
		return tools.Result{Content: "no background tasks are outstanding."}
	}

	// Collected in launch order rather than completion order, so the same
	// three launches read the same way every time and the model can match
	// answers to what it asked for.
	var b strings.Builder
	uncollected := 0
	// Which children's answers this one result carries, and where each
	// one is in it. A tool result is one block and this one aggregates
	// several, so the identities travel beside it; the spans are what
	// make a per-child record cover that child's words rather than the
	// whole collection over again.
	var sources []tools.ResultSource
	for _, id := range ids {
		text, err, collected := t.manager.Wait(ctx, id)
		if !collected {
			// The caller stopped waiting. The task did not stop, so its
			// id stays outstanding and the next TaskCollect can pick the
			// answer up.
			uncollected++
			continue
		}
		t.manager.dropPending(parentSessionID, id)
		if err != nil {
			// localcode's own sentence about a child that failed, not
			// the child's words. No source: nobody else wrote this.
			fmt.Fprintf(&b, "## %s\nfailed: %v\n\n", id, err)
			continue
		}
		if strings.TrimSpace(text) == "" {
			fmt.Fprintf(&b, "## %s\nfinished without an answer.\n\n", id)
			continue
		}
		fmt.Fprintf(&b, "## %s\n", id)
		from := b.Len()
		b.WriteString(text)
		sources = append(sources, tools.ResultSource{
			ID: t.manager.childAgent(id) + "#" + id, From: from, To: b.Len(),
		})
		b.WriteString("\n\n")
	}
	if uncollected > 0 {
		fmt.Fprintf(&b, "## still running\n%d task(s) had not finished when this collection stopped; they are still going and can be collected again.\n", uncollected)
	}
	// TrimSpace would move every span, so the leading trim is done by
	// construction (nothing is written before the first header) and the
	// trailing one by cutting only the blank lines this loop added.
	content := strings.TrimRight(b.String(), "\n")
	return tools.Result{Content: content, Sources: sources}
}

// delegationSchema is the input schema both delegation tools share: which
// agent, and what to tell it. One function because the two schemas were
// identical and drifting apart would mean the model learning two shapes
// for the same argument.
func delegationSchema(names []string) json.RawMessage {
	enum, _ := json.Marshal(names)
	return json.RawMessage(fmt.Sprintf(
		`{"type":"object","properties":{"agent":{"type":"string","enum":%s},"prompt":{"type":"string","description":"self-contained instructions for the sub-agent; it has no access to this conversation's history"}},"required":["agent","prompt"]}`,
		enum,
	))
}

// writeAgentList renders the name/description list both delegation tools
// put in their description.
func writeAgentList(b *strings.Builder, agents map[string]config.AgentConfig) {
	for _, name := range agentNamesOf(agents) {
		desc := agents[name].Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(b, "- %s: %s\n", name, desc)
	}
}

func agentNamesOf(agents map[string]config.AgentConfig) []string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RunningIn reports which of these sessions have a background task still
// going, without stopping any of them.
//
// StopSession's first half, and separate because the answer archiving
// wants is different from the answer deleting wants. A delete is removing
// the records the work writes to, so it has to stop the work; an archive
// is not, so it refuses instead and leaves the tasks alone. Killing work
// nobody asked to kill is the silent side effect that would be.
func (tm *TaskManager) RunningIn(ids []string) []string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	var running []string
	for _, id := range ids {
		if _, ok := tm.cancels[id]; ok {
			running = append(running, id)
		}
	}
	return running
}
