package agent

import (
	"context"
	"encoding/json"
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
	tm.results[taskID] = out
	if ch, ok := tm.waiters[taskID]; ok {
		close(ch)
		delete(tm.waiters, taskID)
	}
}

// Wait blocks until taskID finishes and returns its final answer.
//
// A task that has already finished returns immediately, which is what
// makes "launch three, collect three" work regardless of the order they
// happen to complete in.
func (tm *TaskManager) Wait(ctx context.Context, taskID string) (string, error) {
	tm.mu.Lock()
	if out, done := tm.results[taskID]; done {
		delete(tm.results, taskID)
		tm.mu.Unlock()
		return out.text, out.err
	}
	ch, running := tm.waiters[taskID]
	tm.mu.Unlock()
	if !running {
		return "", fmt.Errorf("no such background task %q", taskID)
	}

	select {
	case <-ch:
	case <-ctx.Done():
		// The turn was cancelled, not the task. Leaving the task running
		// is correct — it belongs to the session, not to this turn — and
		// its result stays recorded for whoever asks next.
		return "", ctx.Err()
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()
	out := tm.results[taskID]
	delete(tm.results, taskID)
	return out.text, out.err
}

// SpawnBackground launches a task and remembers it as one this session
// still owes itself a collection for.
func (tm *TaskManager) SpawnBackground(parentSessionID, agentName, prompt, traceID string) (string, error) {
	tm.mu.Lock()
	outstanding := len(tm.pending[parentSessionID])
	tm.mu.Unlock()
	if outstanding >= maxBackgroundTasks {
		return "", fmt.Errorf("this session already has %d background tasks running and uncollected; collect them before launching more", outstanding)
	}

	taskID, err := tm.spawn(parentSessionID, agentName, prompt, traceID)
	if err != nil {
		return "", err
	}
	tm.mu.Lock()
	tm.pending[parentSessionID] = append(tm.pending[parentSessionID], taskID)
	tm.mu.Unlock()
	return taskID, nil
}

// takePending removes and returns the background tasks a session is
// waiting on: all of them, or just the one named.
func (tm *TaskManager) takePending(parentSessionID, taskID string) []string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	all := tm.pending[parentSessionID]
	if taskID == "" {
		delete(tm.pending, parentSessionID)
		return all
	}
	for i, id := range all {
		if id == taskID {
			tm.pending[parentSessionID] = append(append([]string(nil), all[:i]...), all[i+1:]...)
			return []string{id}
		}
	}
	return nil
}

// forgetSession drops everything remembered about a parent session's
// background tasks — called when the session is deleted, so a task
// launched and never collected does not keep its answer forever.
func (tm *TaskManager) forgetSession(parentSessionID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, id := range tm.pending[parentSessionID] {
		delete(tm.results, id)
	}
	delete(tm.pending, parentSessionID)
}

// TaskBackgroundTool launches a sub-agent and returns immediately.
type TaskBackgroundTool struct {
	manager *TaskManager
	agents  func() map[string]config.AgentConfig
}

func NewTaskBackgroundTool(manager *TaskManager, agents func() map[string]config.AgentConfig) TaskBackgroundTool {
	return TaskBackgroundTool{manager: manager, agents: agents}
}

func (t TaskBackgroundTool) Name() string { return "TaskBackground" }

func (t TaskBackgroundTool) Description() string {
	var b strings.Builder
	b.WriteString("Start a sub-agent working in the background and return straight away with its task id. " +
		"Use this to run two or more independent pieces of investigation at once, then call TaskCollect to " +
		"get the answers. For a single question, use Task instead: it is the same thing without the " +
		"bookkeeping. Available agents:\n")
	writeAgentList(&b, t.agents())
	return b.String()
}

func (t TaskBackgroundTool) InputSchema() json.RawMessage {
	names, _ := json.Marshal(agentNamesOf(t.agents()))
	return json.RawMessage(fmt.Sprintf(
		`{"type":"object","properties":{"agent":{"type":"string","enum":%s},"prompt":{"type":"string","description":"self-contained instructions for the sub-agent; it has no access to this conversation's history"}},"required":["agent","prompt"]}`,
		names,
	))
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
	agents := t.agents()
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
	taskID, err := t.manager.SpawnBackground(parentSessionID, args.Agent, args.Prompt, trace.ID(ctx))
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

	ids := t.manager.takePending(parentSessionID, args.TaskID)
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
	for _, id := range ids {
		text, err := t.manager.Wait(ctx, id)
		if err != nil {
			fmt.Fprintf(&b, "## %s\nfailed: %v\n\n", id, err)
			continue
		}
		if strings.TrimSpace(text) == "" {
			fmt.Fprintf(&b, "## %s\nfinished without an answer.\n\n", id)
			continue
		}
		fmt.Fprintf(&b, "## %s\n%s\n\n", id, text)
	}
	return tools.Result{Content: strings.TrimSpace(b.String())}
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
