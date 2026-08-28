package agent

import (
	"context"
	"time"

	"localcode/internal/hooks"
	"localcode/internal/trace"
)

// The turn, written down.
//
// Everything here is a side effect: nothing in this file changes what a
// turn does, and a nil writer makes every call a no-op. That is deliberate
// — an observability layer that can fail a turn is worse than no
// observability layer.

// tracer is the writer to use right now, or nil.
//
// Gated on Smart Agent for the same reason the rest of the bundle is: it
// writes a file, under the user's home directory, recording which models
// answered and what they cost. That is useful and it is also a thing to
// opt into rather than to discover.
// The setting is read from ctx rather than live, so a turn that started
// while Smart Agent was on is written down from beginning to end. Reading
// the switch per record produced traces with a start and no end, and
// records belonging to no start at all.
func (l *Loop) tracer(ctx context.Context) *trace.Writer {
	if !l.smartOn(ctx) {
		return nil
	}
	return l.Trace
}

// traceCtx makes sure ctx carries a trace id, minting one if this is the
// top of a turn.
//
// A sub-agent's turn arrives with the caller's id already on the context
// and keeps it, which is the whole point: one id spans the orchestrator
// and everything it delegated to.
func traceCtx(ctx context.Context) (context.Context, string) {
	if id := trace.ID(ctx); id != "" {
		return ctx, id
	}
	id := trace.NewID()
	return trace.WithID(ctx, id), id
}

// stamp fills in the fields that come from the session rather than from
// the event: which session, and whose child it is.
func (l *Loop) stamp(sessionID string, r trace.Record) trace.Record {
	r.SessionID = sessionID
	if l.Store != nil {
		if sess, err := l.Store.Get(sessionID); err == nil {
			r.ParentSessionID = sess.ParentID
			if r.Agent == "" {
				r.Agent = sess.Agent
			}
		}
	}
	return r
}

// traceModel records one provider call: what answered, what it cost, and
// what the cache did.
func (l *Loop) traceModel(ctx context.Context, traceID, sessionID string, run modelRun, usage streamUsage, stopReason string, took time.Duration, err error) {
	w := l.tracer(ctx)
	if w == nil {
		return
	}
	rec := trace.Record{
		TraceID:          traceID,
		Span:             trace.SpanModel,
		Profile:          run.profileName,
		Model:            run.profile.Model,
		Provider:         run.profile.Provider,
		InputTokens:      usage.inputTokens,
		OutputTokens:     usage.outputTokens,
		CacheReadTokens:  usage.cacheRead,
		CacheWriteTokens: usage.cacheWrite,
		FinishReason:     stopReason,
		DurationMS:       took.Milliseconds(),
		PromptManifest:   run.manifest.ID,
		PromptAssets:     run.manifest.SelectedIDs(),
		PromptUntrusted:  run.manifest.UntrustedIDs(),
	}
	if err != nil {
		rec.Error = err.Error()
	}
	w.Write(l.stamp(sessionID, rec))
}

// traceTool records one tool execution. Where the time goes in a
// multi-agent turn is almost always here, and almost always in one call.
func (l *Loop) traceTool(ctx context.Context, traceID, sessionID, name string, took time.Duration, isError bool, detail string) {
	w := l.tracer(ctx)
	if w == nil {
		return
	}
	rec := trace.Record{
		TraceID:    traceID,
		Span:       trace.SpanTool,
		Tool:       name,
		DurationMS: took.Milliseconds(),
		Detail:     detail,
	}
	if isError {
		rec.Error = detail
		rec.Detail = ""
	}
	w.Write(l.stamp(sessionID, rec))
}

// traceSpan records the one-line events: a turn beginning or ending, a
// fallback, a compaction, a delegation.
func (l *Loop) traceSpan(ctx context.Context, traceID, sessionID, span string, r trace.Record) {
	w := l.tracer(ctx)
	if w == nil {
		return
	}
	r.TraceID = traceID
	r.Span = span
	w.Write(l.stamp(sessionID, r))
}

// delegateBlocked asks a "delegate" hook whether this sub-agent may be
// started.
//
// The one governance point a prompt cannot provide. "No agent of mine
// spawns another", or "nothing may be delegated to the implement agent on
// this machine", is a rule about capability, and the place to enforce a
// rule about capability is where the capability is exercised. A refusal
// comes back to the calling model as a failed tool call, which is a thing
// it can reason about, rather than as a crash.
func (l *Loop) delegateBlocked(ctx context.Context, parentSessionID, agentName, prompt string) (bool, string) {
	if len(l.Config.Hooks) == 0 {
		return false, ""
	}
	blocked, reason, _ := hooks.Run(ctx, l.Config.Hooks, hooks.EventDelegate, l.SessionDir(parentSessionID), map[string]any{
		"session_id": parentSessionID,
		"agent":      agentName,
		"prompt":     prompt,
	})
	if blocked && reason == "" {
		reason = "blocked by a delegate hook"
	}
	return blocked, reason
}
