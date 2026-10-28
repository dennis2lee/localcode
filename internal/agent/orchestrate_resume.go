package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"localcode/internal/events"
)

// Resuming a run: the journal, and reuse by key.
//
// Every unit a run launches is recorded in the run session's own log, on
// the records every synchronous task already writes there: its
// task.spawned carries the unit's key and where it sits in the plan, and
// the status it completes with carries its answer. A resumed run runs the
// same plan again, and a unit whose key is in the journal with a complete
// answer is not launched: its answer is taken from the journal and
// recorded again under the new run, so a resume of a resume finds it
// without walking back.
//
// By key, not by position: a failure in the middle of a fanout reruns that
// unit and nothing else. The key is what the unit was asked (its fully
// rendered prompt, which carries everything earlier stages kept), who was
// asked (agent, resolved profile and model, tool allowlist), where it sits
// (stage index and name, item, copy, pass), the directory it worked in,
// and the answer it had to give. A changed upstream answer changes every
// downstream prompt, and so every downstream key. The item is there even
// though the prompt usually carries it, because a fanout prompt need not
// say {{item}}; the directory, because an answer about one checkout says
// nothing about another.
//
// The journal is in the run session rather than the conversation, which a
// rewind filters and a fork copies; and the answer is in the journal
// rather than read back from the unit's session, which a person can open
// and type into afterwards.

// journalReplyLimit bounds the prose answer one unit's record keeps.
const journalReplyLimit = carriedInputLimit

// cachedUnit is one unit a resumed run can take from the journal.
type cachedUnit struct {
	child string
	text  string
	data  map[string]any
}

type resumeCache map[string]cachedUnit

type resumeKey struct{}

func withResume(ctx context.Context, cache resumeCache) context.Context {
	return context.WithValue(ctx, resumeKey{}, cache)
}

func resumeFrom(ctx context.Context) resumeCache {
	c, _ := ctx.Value(resumeKey{}).(resumeCache)
	return c
}

// unitKey identifies a unit across runs of the same plan.
func (l *Loop) unitKey(ctx context.Context, runID string, p Plan, stage Stage, u unit, prompt string, allowed []string) string {
	index := -1
	for i, s := range p.Stages {
		if s.Name == stage.Name {
			index = i
			break
		}
	}
	profile, model, provider := "", "", ""
	if name, prof, err := l.profileFor(ctx, runID, stage.Agent); err == nil {
		profile, model, provider = name, prof.Model, prof.Provider
	}
	tools := append([]string(nil), allowed...)
	sort.Strings(tools)
	raw, _ := json.Marshal([]any{
		index, stage.Name, stage.Agent, profile, provider, model, tools,
		prompt, string(stage.answerSchema()), u.item, u.copy, u.pass,
		l.SessionDir(runID),
	})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// runJournal is what a finished or stopped run left that a resume can
// reuse: every unit that completed, by key, with its answer.
func (l *Loop) runJournal(runID string) resumeCache {
	evs, err := l.Store.Events(runID, 0)
	if err != nil {
		return nil
	}
	keys := map[string]string{}
	cache := resumeCache{}
	for _, e := range evs {
		id, _ := e.Data["task_id"].(string)
		switch e.Type {
		case events.TypeTaskSpawned:
			if k, _ := e.Data["key"].(string); k != "" && id != "" {
				keys[id] = k
			}
		case events.TypeTaskStatus:
			k := keys[id]
			if k == "" || e.Data["status"] != "completed" {
				continue
			}
			reply, ok := e.Data["reply"].(string)
			if !ok {
				continue
			}
			c := cachedUnit{child: id, text: reply}
			if a, ok := e.Data["answer"].(map[string]any); ok {
				c.data = a
			}
			cache[k] = c
		}
	}
	return cache
}

// reuseUnit is a unit a resumed run takes from the journal: recorded again
// under this run, marked as reused, and launched nowhere.
func (l *Loop) reuseUnit(runID string, stage Stage, u unit, prompt, key string, c cachedUnit, o outcome) outcome {
	l.Store.Append(runID, events.TypeTaskSpawned, map[string]any{
		"task_id": c.child, "agent": stage.Agent, "prompt": prompt, "reused": true,
		"key": key, "stage": stage.Name, "item": u.item, "copy": u.copy, "round": u.pass,
	})
	done := map[string]any{"task_id": c.child, "status": "completed", "reused": true, "reply": c.text}
	if c.data != nil {
		done["answer"] = c.data
	}
	l.Store.Append(runID, events.TypeTaskStatus, done)
	o.text, o.data, o.reused = c.text, c.data, true
	return o
}
