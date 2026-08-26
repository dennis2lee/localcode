package agent

import (
	"context"
	"fmt"
	"strings"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/hooks"
	"localcode/internal/provider"
)

// Falling back to another model when one is not answering.
//
// In a long agent session a model failure is an ordinary condition, not an
// exception: a rate limit at the wrong moment, a provider having a bad
// hour, a local server that was restarted, a credential that expired
// overnight. Without somewhere to go, every one of those ends the turn and
// loses whatever the turn was in the middle of — which on a session that
// has been running for an hour is the expensive part.
//
// A chain says where to go. The subtlety, and the reason this file is not
// three lines, is that changing the model changes more than the model: the
// orchestration prompt is written per model family, and so is the quirk
// note. Sending Claude's policy to a local 8B because Bedrock was down
// gets a fallback that technically answers and is worse than useless. So
// switching profiles re-derives the whole request, prompt included.

// fallbackChain resolves the profiles a turn may fall back to, in order.
//
// Only when Smart Agent is on: switching models mid-session changes who is
// answering, which is a visible thing to start doing to somebody who has
// not asked for it.
//
// The chain is flat by design — a fallback's own list is not followed — so
// it cannot loop and its length is exactly what the config says.
func (l *Loop) fallbackChain(primary config.Profile) []attempt {
	if !l.SmartAgentEnabled() {
		return nil
	}
	out := make([]attempt, 0, len(primary.Fallback))
	for _, name := range primary.Fallback {
		p, ok := l.Config.Profiles[name]
		if !ok {
			// Validate rejects this at load time; a config edited at
			// runtime could still get here, and skipping is better than
			// failing the turn over a name that is only being read
			// because something else already went wrong.
			continue
		}
		out = append(out, attempt{name: name, profile: p})
	}
	return out
}

// attempt is one link in a chain: which profile, under which name.
type attempt struct {
	name    string
	profile config.Profile
}

// worthFallingBackOver reports whether err is the kind of failure another
// model could survive.
//
// The distinction being drawn is "this endpoint cannot answer" versus
// "this request cannot be answered". A conversation too long for the
// window is the second kind and is already handled properly, by
// summarizing and retrying on the same model; sending it to a smaller
// fallback would make it worse and lose the cache prefix as well. A
// malformed request is the second kind too, and would be malformed
// everywhere.
//
// Matched on the error text, like isContextOverflow above it, because
// that is what the providers actually give: an HTTP status and the body
// that came with it. A typed error would be nicer and would require every
// backend to agree on a taxonomy none of them publishes.
func worthFallingBackOver(err error) bool {
	if err == nil || isContextOverflow(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, p := range fallbackPhrases {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// fallbackPhrases are what the failures in the paragraph above look like
// on the wire, across the three backends localcode speaks to.
var fallbackPhrases = []string{
	// Rate limits and overload.
	"429", "rate limit", "rate_limit", "too many requests", "quota",
	"overloaded", "throttl", "capacity",
	// The endpoint is having a bad time.
	"500", "502", "503", "504", "internal server error", "bad gateway",
	"service unavailable", "gateway timeout", "server error",
	// It is not reachable at all. A local server that was restarted, a
	// laptop that changed network.
	"connection refused", "no such host", "connection reset",
	"i/o timeout", "context deadline exceeded", "eof", "broken pipe",
	"network is unreachable", "tls handshake",
	// The model or the credentials are not what this endpoint has.
	"model not found", "does not exist", "not authorized", "accessdenied",
	"unauthorized", "401", "403", "404", "expiredtoken", "invalid api key",
	"validationexception", "unrecognized",
}

// firstOutput reports whether anything from this response has already
// reached the transcript.
//
// It decides whether a mid-stream failure may be retried elsewhere. Once
// text has been written into the session log, a fallback would produce a
// second answer after a partial one and the conversation would carry both
// — so a stream that has already said something is not restarted, however
// badly it then ends.
func firstOutput(blocks []provider.Block) bool { return len(blocks) > 0 }

// modelRun is everything one profile choice decides about a request: who
// to ask, with what system prompt, and how long an answer to allow.
//
// It exists because a fallback changes all four together. The model is the
// obvious one; the client is different because a fallback usually lives on
// another provider, which is the point; and the system prompt is different
// because both the orchestration policy and the per-model note are written
// per model family. Sending the prompt written for a hosted flagship to
// the local 8B that caught the overflow produces an answer that is worse
// than the failure it replaced, which is the trap this type exists to
// close.
type modelRun struct {
	profileName string
	profile     config.Profile
	client      provider.Provider
	system      string
	maxTokens   int
}

// buildRun derives a request from a profile. Called once for the profile a
// turn starts on, and again for each fallback it moves to.
func (l *Loop) buildRun(sessionID, resolveAgent string, agentCfg config.AgentConfig, profileName string, profile config.Profile, modelOverride string) (modelRun, error) {
	// A custom command's "model:" frontmatter pins the model for this turn
	// and travels with the fallback, because the person asked for that
	// model specifically and a chain is about reaching an endpoint rather
	// than about choosing a model.
	if modelOverride != "" {
		profile.Model = modelOverride
	}
	client, ok := l.Providers[profile.Provider]
	if !ok {
		return modelRun{}, fmt.Errorf("no provider client configured for %q (check Providers map at startup)", profile.Provider)
	}

	maxTokens := profile.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}

	system := l.systemPromptFor(sessionID)
	if agentCfg.Prompt != "" {
		system = system + "\n\n" + agentCfg.Prompt
	}
	// Smart Agent's orchestration policy, when this turn is the one doing
	// the orchestrating. Added after the agent's own prompt so a
	// specialist's instructions are never overridden by it, and before the
	// per-model note below for the same reason that one is last.
	if policy := l.orchestrationFor(sessionID, resolveAgent, profile.Model); policy != "" {
		system = system + "\n\n" + policy
	}
	// Last, so a per-model note about how to write for this window is not
	// buried under the project's rules — and only for the models that need
	// one. See quirks.go.
	if note := quirkNote(profile.Model); note != "" {
		system = system + "\n\n" + note
	}

	return modelRun{
		profileName: profileName,
		profile:     profile,
		client:      client,
		system:      system,
		maxTokens:   maxTokens,
	}, nil
}

// nextAttempt takes the next link in the chain, advancing the cursor.
func (l *Loop) nextAttempt(chain []attempt, at *int) (attempt, bool) {
	if *at >= len(chain) {
		return attempt{}, false
	}
	next := chain[*at]
	*at++
	return next, true
}

// reportFallback puts the switch in the transcript.
//
// Recorded rather than silent, and as a recovered error rather than a
// note, because the two things a reader needs are that the turn did not
// fail and that the answer they are about to read came from a different
// model than the one they configured. A fallback nobody can see is a
// session that mysteriously got worse.
func (l *Loop) reportFallback(sessionID string, from, to modelRun, cause error) {
	l.Store.Append(sessionID, events.TypeError, map[string]any{
		"error": fmt.Sprintf("%s did not answer (%v); falling back to %s",
			describeRun(from), cause, describeRun(to)),
		"recovered": true,
		"fallback":  to.profile.Model,
	})
}

func describeRun(r modelRun) string {
	if r.profileName == "" {
		return r.profile.Model
	}
	return fmt.Sprintf("%s (%s)", r.profile.Model, r.profileName)
}

// runRetryHook tells a "retry" hook that this turn has changed model, and
// which way. Fire and forget: the switch has been decided, and a hook that
// wanted a say should be a pre_model hook on the model it does not want.
func (l *Loop) runRetryHook(ctx context.Context, sessionID string, from, to modelRun, cause error) {
	if len(l.Config.Hooks) == 0 {
		return
	}
	hooks.Run(ctx, l.Config.Hooks, hooks.EventRetry, map[string]any{
		"session_id": sessionID,
		"from_model": from.profile.Model,
		"to_model":   to.profile.Model,
		"error":      cause.Error(),
	})
}

// runCompactHook tells a "compact" hook the history has been summarized,
// and whether that was the routine threshold or a rescue.
func (l *Loop) runCompactHook(ctx context.Context, sessionID, reason string) {
	if len(l.Config.Hooks) == 0 {
		return
	}
	hooks.Run(ctx, l.Config.Hooks, hooks.EventCompact, map[string]any{
		"session_id": sessionID,
		"reason":     reason,
	})
}
