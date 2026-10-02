package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"localcode/internal/trace"
)

// runStage launches one stage's units, at most maxParallel at a time, and
// returns their outcomes in launch order however they finished.
func (l *Loop) runStage(ctx context.Context, sessionID string, p Plan, stage Stage, units []unit, carried string) []outcome {
	out := make([]outcome, len(units))
	allowed := l.toolsForRole(ctx, stage.Agent, stage.Role)
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup

	for i, u := range units {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				out[i] = outcome{unit: u, agent: stage.Agent, err: ctx.Err()}
				return
			}
			out[i] = l.runUnit(ctx, sessionID, p, stage, u, carried, allowed)
		}()
	}
	wg.Wait()
	return out
}

// runUnit is one agent turn: its own deadline, its own child session, and
// its answer read back out of that child's log.
func (l *Loop) runUnit(ctx context.Context, sessionID string, p Plan, stage Stage, u unit, carried string, allowed []string) outcome {
	o := outcome{unit: u, agent: stage.Agent}
	if l.Tasks == nil {
		o.err = fmt.Errorf("this build has no task manager, so nothing can be delegated to")
		return o
	}

	ctx, cancel := context.WithTimeout(ctx, stageTimeout)
	defer cancel()

	prompt := stagePrompt(p, stage, u.item, carried)
	ctx = withStageAnswer(withReviewerTools(ctx, allowed), stage)

	started := time.Now()
	childID, text, err := l.Tasks.SpawnSyncInto(ctx, sessionID, "", stage.Agent, prompt)
	l.traceSpan(ctx, trace.ID(ctx), sessionID, trace.SpanDelegate, trace.Record{
		Agent:  stage.Agent,
		Detail: fmt.Sprintf("orchestrate %s/%s -> %s in %s", stage.Name, u.item, childID, time.Since(started).Round(time.Millisecond)),
	})
	o.text = text
	if err != nil {
		o.err = err
		return o
	}
	if len(stage.Returns) > 0 && childID != "" {
		o.data = l.readAnswer(childID, stage)
	}
	return o
}

// stagePrompt is the three substitutions and nothing else.
func stagePrompt(p Plan, stage Stage, item, carried string) string {
	r := strings.NewReplacer(
		"{{task}}", p.Goal,
		"{{item}}", item,
		"{{input}}", carried,
	)
	body := r.Replace(stage.Prompt)
	// The goal travels even when the prompt did not ask for it: a
	// sub-agent cannot see this conversation, and a stage prompt written
	// without {{task}} is one whose author forgot that rather than one who
	// meant the agent to work blind.
	if !strings.Contains(stage.Prompt, "{{task}}") {
		body = "The run's goal: " + p.Goal + "\n\n" + body
	}
	// A rerun carries the rounds so far even when the prompt never asked
	// for {{input}}, for the same reason: without them the next round
	// works blind, repeating whatever the last one tried.
	if carried != "" && !strings.Contains(stage.Prompt, "{{input}}") {
		body += "\n\nEarlier rounds:\n" + carried
	}
	return body
}
