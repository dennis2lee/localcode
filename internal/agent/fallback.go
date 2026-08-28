package agent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/hooks"
	"localcode/internal/prompt"
	"localcode/internal/provider"
	"localcode/internal/trace"
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
func (l *Loop) fallbackChain(ctx context.Context, primary config.Profile) []attempt {
	if !l.smartOn(ctx) {
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
	// Checked first, and it wins. A message that names a defect in the
	// request itself is answered the same way by every endpoint, however
	// much else the message happens to say.
	for _, p := range deterministicPhrases {
		if strings.Contains(msg, p) {
			return false
		}
	}
	for _, p := range fallbackPhrases {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// deterministicPhrases are request defects: the shape of what was sent,
// not the state of what it was sent to.
//
// This list exists because an exception class is not a diagnosis. Bedrock
// raises ValidationException for a model id that does not exist in the
// region, for an account not entitled to the model, and for a request
// field the model does not accept — an availability problem, an
// authorization problem and a request bug under one name. Classifying the
// class as recoverable, which is what localcode used to do, sent a
// deprecated "temperature" field to every profile in the chain; this
// repository's own docs/MODELS.md lists all three under that one heading.
// So the cause is matched, never the wrapper.
var deterministicPhrases = []string{
	"is deprecated for this model", "malformed input request",
	"unsupported parameter", "unrecognized parameter", "unknown parameter",
	"unexpected parameter", "extraneous key", "extra inputs are not permitted",
	"invalid_request_error", "invalid tool", "tool_use_id", "invalid schema",
	"expected maxlength", "expected minlength", "failed to satisfy constraint",
}

// fallbackPhrases are what an endpoint that cannot answer looks like on
// the wire, across the three backends localcode speaks to.
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
	// The model or the credentials are not what this endpoint has. Named
	// by cause rather than by exception class, so a Bedrock
	// ValidationException reaches this list only when it says the model id
	// is wrong or the account is not entitled, and not when it says the
	// request was.
	"model not found", "model identifier is invalid", "unrecognized model",
	"not authorized", "accessdenied", "unauthorized", "401", "403", "404",
	"expiredtoken", "invalid api key", "on-demand throughput isn",
}

// Same-endpoint retry: the step before the chain.
//
// worthFallingBackOver decides whether *another* endpoint could answer.
// This decides something narrower: whether *this* endpoint could answer
// if simply asked again in a moment. A single 429 or a 503 during a
// deploy is that kind of failure, and walking the chain over it is the
// expensive response — the next profile is a different model, a
// different cache prefix, and possibly a worse answer, spent on a
// condition two seconds would have cleared. So a transient failure gets
// a bounded retry here first, and the chain is kept for the failures
// that are actually about the endpoint.
//
// What does not retry: credential and model-identity failures (a 401
// will be a 401 in two seconds too — those go straight to the chain),
// request defects (deterministicPhrases wins here exactly as it does in
// worthFallingBackOver), and DNS or route failures, which are the
// network saying this destination does not resolve rather than saying
// try again.

// maxSameEndpointRetries bounds the retries spent on one endpoint before
// the chain is consulted. Two, because the condition being covered is a
// blip: anything a third attempt would fix, the first fallback fixes
// sooner.
const maxSameEndpointRetries = 2

// retryBase is the unit of the backoff, in nanoseconds. A variable so
// tests exercising the retry loop do not sit through real seconds, and
// atomic because a test resetting it can overlap a goroutine a previous
// test leaked mid-wait; nothing outside a test changes it.
var retryBase atomic.Int64

func init() { retryBase.Store(int64(time.Second)) }

// retryBackoff is the wait before same-endpoint retry n (1-based):
// 1s, then 2s. Doubling, so the second attempt gives a struggling
// endpoint more room than the first, and bounded, because the turn is
// interactive and the chain is waiting.
func retryBackoff(n int) time.Duration {
	return time.Duration(retryBase.Load()) << (n - 1)
}

// retryableInPlace reports whether err is worth asking the same endpoint
// about again, before any fallback is considered.
func retryableInPlace(err error) bool {
	if err == nil || isContextOverflow(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Request defects win, same rule and same reason as in
	// worthFallingBackOver: a defect in the request is answered the same
	// way however many times it is sent.
	for _, p := range deterministicPhrases {
		if strings.Contains(msg, p) {
			return false
		}
	}
	for _, p := range retryPhrases {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// retryPhrases are the transient causes: what an endpoint that will be
// fine in a moment looks like on the wire. Deliberately a subset of
// fallbackPhrases — anything transient is also worth falling back over
// when the retries run out — and deliberately without the credential,
// model-identity and name-resolution causes, which time does not fix.
var retryPhrases = []string{
	// Rate limits and overload clear on their own; that is what they are.
	"429", "rate limit", "rate_limit", "too many requests", "quota",
	"overloaded", "throttl", "capacity",
	// A 5xx is the endpoint having a moment, not an identity.
	"500", "502", "503", "504", "internal server error", "bad gateway",
	"service unavailable", "gateway timeout", "server error",
	// A connection that dropped mid-handshake or mid-stream. A local
	// server restarting is the common case, and it comes back.
	"connection refused", "connection reset", "i/o timeout",
	"context deadline exceeded", "eof", "broken pipe", "tls handshake",
}

// sleepFor waits d unless ctx ends first, and reports whether the wait
// ran its course. A cancelled turn does not sit out a backoff.
// retryWaitBarrier, when non-nil, runs after a retry is announced and
// just before its backoff wait. Nil outside tests; a test sets it to
// cancel the turn at exactly that point, so the cancelled-mid-backoff
// path can be exercised without racing a timer.
var retryWaitBarrier func()

func sleepFor(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// retryOutcome is what maybeRetrySameEndpoint decided. Three outcomes,
// because "no" means two different things to the caller: not eligible
// hands the error to the fallback chain, cancelled ends the turn — a
// cancelled wait must not be spent walking the chain with a context that
// is already dead, recording model switches that never reach a provider.
type retryOutcome int

const (
	// retryNotEligible: the cause is not transient, the retries are
	// spent, or Smart Agent is off. The fallback chain is next.
	retryNotEligible retryOutcome = iota
	// retryReady: the backoff ran its course; ask the same run again.
	retryReady
	// retryCancelled: the turn was cancelled during the backoff. Nothing
	// was attempted and nothing is counted; the turn stops here.
	retryCancelled
)

// maybeRetrySameEndpoint spends one bounded retry on the endpoint that
// just failed, if the failure is the transient kind.
//
// The counts describe provider attempts, not scheduled waits and not
// intentions. This function classifies, announces and waits; it does not
// count. Committing the count is commitRetry's job, at the moment the
// request is actually going out, because between the end of the backoff
// and the call there is still a pre_model hook that can block the turn
// outright. A retry counted before that hook describes an attempt no
// provider ever received.
//
// Only with Smart Agent on, for the same reason the chain itself is: a
// turn that silently takes three seconds longer than it used to is a
// behaviour change, and this one arrives as part of the bundle that was
// opted into rather than happening to everyone.
func (l *Loop) maybeRetrySameEndpoint(ctx context.Context, sessionID string, run modelRun, err error, sameTries *int, pending *pendingRetry) retryOutcome {
	if !l.smartOn(ctx) || *sameTries >= maxSameEndpointRetries || !retryableInPlace(err) {
		return retryNotEligible
	}
	attempt := *sameTries + 1
	wait := retryBackoff(attempt)
	// In the transcript before the wait, for the same reason a fallback
	// is announced: the reader of a turn that paused needs to know it
	// was waiting on purpose.
	l.Store.Append(sessionID, events.TypeError, map[string]any{
		"error": fmt.Sprintf("%s did not answer (%v); retrying it in %s (attempt %d of %d) before any fallback",
			describeRun(run), err, wait, attempt, maxSameEndpointRetries),
		"recovered": true,
	})
	if retryWaitBarrier != nil {
		retryWaitBarrier()
	}
	if !sleepFor(ctx, wait) {
		// Cancelled mid-backoff. Said in the transcript so the announced
		// retry does not read as one that happened.
		l.Store.Append(sessionID, events.TypeError, map[string]any{
			"error":     fmt.Sprintf("cancelled while waiting to retry %s; the retry was never attempted", describeRun(run)),
			"recovered": true,
		})
		return retryCancelled
	}
	// The wait completed, and that is all this function knows. Counting
	// happens at the provider call, not here: between this point and the
	// request there is still a pre_model hook that can block the turn
	// outright, and a retry counted here would describe an attempt no
	// provider ever received. So the intent is handed to the caller and
	// committed by commitRetry when the request is actually going out.
	*pending = pendingRetry{active: true, attempt: attempt, cause: err}
	return retryReady
}

// pendingRetry is a retry that has served its backoff and is waiting for
// the request to actually leave.
//
// It exists because "the wait finished" and "the provider was asked
// again" are different events with a hook in between, and the retry count
// is documented to mean the second one. Carrying the intent rather than
// recording it is what keeps that true: a turn blocked after the backoff
// discards this and counts nothing.
type pendingRetry struct {
	active  bool
	attempt int
	cause   error
}

// commitRetry records a pending retry at the moment the request is going
// to the provider: after the hooks have allowed it, before the call.
//
// Returns whether the caller should proceed. A context that ended between
// the backoff and here means the request is not going out after all, so
// nothing is counted and the turn stops.
func (l *Loop) commitRetry(ctx context.Context, traceID, sessionID string, run modelRun, pending *pendingRetry, sameTries, retries *int) bool {
	if !pending.active {
		return true
	}
	if ctx.Err() != nil {
		*pending = pendingRetry{}
		return false
	}
	*sameTries = pending.attempt
	*retries++
	l.traceSpan(ctx, traceID, sessionID, trace.SpanRetry, trace.Record{
		Profile: run.profileName, Model: run.profile.Model, Provider: run.profile.Provider,
		Retries: *retries, Error: pending.cause.Error(),
	})
	*pending = pendingRetry{}
	return true
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
	// systemBlocks is the same system prompt with its per-asset seams
	// intact, for adapters whose wire format can keep them.
	systemBlocks []provider.SystemBlock
	maxTokens    int
	// manifest is the record of how system was assembled: which prompt
	// assets are in this request, which are not and why. Carried on the
	// run rather than recomputed, because a fallback builds a new run
	// and the two manifests are how you tell that it actually
	// re-derived the prompt rather than reusing the old rendering.
	manifest prompt.Manifest
}

// buildRun derives a request from a profile. Called once for the profile a
// turn starts on, and again for each fallback it moves to.
func (l *Loop) buildRun(ctx context.Context, sessionID, resolveAgent string, agentCfg config.AgentConfig, profileName string, profile config.Profile, modelOverride string, fallbackIndex int, advertisedTools []string) (modelRun, error) {
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

	// The prompt is assembled from the declared inventory rather than
	// concatenated here. Same text and same order as the six appends
	// this replaced; the difference is that the request now arrives with
	// a record of which assets are in it and why, and which are not.
	//
	// buildRun is also where a fallback re-derives everything, so the
	// assembly happens per attempt by construction: the note written for
	// the family that just failed cannot survive into the request that
	// goes to a different one.
	actx := l.activationFor(ctx, sessionID, resolveAgent, agentCfg, profileName, profile, fallbackIndex, advertisedTools)
	env := prompt.Assemble(l.promptAssets(), actx)
	// The request carries both forms of the system prompt: the blocks,
	// one per asset with the seams the assembly had, and the folded
	// string. An adapter with a native multi-block system field (the
	// Anthropic API, Bedrock) sends the blocks as themselves; one whose
	// protocol takes a single string (openai-compat) uses the fold, and
	// that lowering is on the record rather than silent (PR-06). The
	// folded string is also what all the sizing arithmetic measures,
	// which is correct either way: the bytes are the same.
	blocks := make([]provider.SystemBlock, 0, len(env.System))
	for _, b := range env.System {
		blocks = append(blocks, provider.SystemBlock{Text: b.Text, Asset: b.AssetID})
	}
	env.Manifest = l.lowerForProvider(env.Manifest, profile, len(env.System))
	env.Manifest.At = time.Now()
	l.Manifests.Put(env.Manifest)

	return modelRun{
		profileName:  profileName,
		profile:      profile,
		client:       client,
		system:       env.SystemText(),
		systemBlocks: blocks,
		maxTokens:    maxTokens,
		manifest:     env.Manifest,
	}, nil
}

// lowerForProvider records the adapter compatibility decision this
// profile's backend actually makes, and is the one definition of it.
//
// One definition because there are two callers and they were once two
// implementations. buildRun folded only for an openai-compatible
// endpoint and recorded it through WithLowering, which recomputes the
// manifest id; "/context" folded whenever there was more than one block,
// wrote different words, and appended to Lowering directly, which does
// not recompute anything. The consequence was the one thing a preview
// must not do: it reported an id that no request would ever carry, so
// the diagnostic answering "what will the next turn send" described an
// assembly that did not exist.
//
// Blocks are folded only where the protocol has nowhere to put them. The
// Anthropic API takes an array of system blocks and Bedrock takes a list
// of SystemContentBlocks, so on those backends nothing is lowered and
// nothing is recorded; an openai-compatible endpoint takes one string,
// and that fold is what this puts on the record.
func (l *Loop) lowerForProvider(m prompt.Manifest, profile config.Profile, systemBlocks int) prompt.Manifest {
	if systemBlocks <= 1 || l.Config.Providers[profile.Provider].Type != config.ProviderOpenAICompat {
		return m
	}
	// Through WithLowering, never by appending: a lowering changes the
	// request, so two assemblies that differ only in it must not share
	// an identity.
	return m.WithLowering(
		fmt.Sprintf("%d system blocks folded into one system string for an openai-compatible endpoint", systemBlocks))
}

// activationFor snapshots everything one request knows about itself, so
// an asset's activation condition is a pure function of it.
//
// The Smart Agent state read here is the pinned one, not the live
// setting: a turn admitted with the bundle on keeps it for every round
// including the ones after somebody flipped the switch, and the prompt
// has to agree with the tools that were chosen under the same pin.
func (l *Loop) activationFor(ctx context.Context, sessionID, resolveAgent string, agentCfg config.AgentConfig, profileName string, profile config.Profile, fallbackIndex int, advertisedTools []string) prompt.ActivationContext {
	smartOn := l.smartOn(ctx)

	role := prompt.RoleOrchestrator
	if _, specialist := l.smartAgents(ctx)[resolveAgent]; specialist {
		role = prompt.RoleSpecialist
	} else if l.Store != nil {
		// A session with a parent is somebody's child, whatever its
		// agent is called, and a child does not orchestrate.
		if sess, err := l.Store.Get(sessionID); err == nil && sess.ParentID != "" {
			role = prompt.RoleSpecialist
		}
	}

	dir := l.SessionDir(sessionID)
	rules := ""
	if l.WorkspaceRules != nil && dir != "" {
		rules = l.WorkspaceRules(dir)
	}
	wsClass := prompt.WorkspaceNone
	switch {
	case dir == "":
	case rules != "":
		wsClass = prompt.WorkspaceProject
	default:
		wsClass = prompt.WorkspacePlain
	}

	return prompt.ActivationContext{
		SmartAgent: smartOn,
		Agent:      resolveAgent,
		Role:       role,
		Profile:    profileName,
		Model:      profile.Model,
		Provider:   profile.Provider,
		Family:     modelFamily(profile.Model),
		// Where in the chain this attempt is. Zero on the profile the
		// turn started on; a fallback passes its own position, so a
		// manifest records which endpoint in the chain it describes
		// rather than always claiming the first.
		FallbackIndex: fallbackIndex,
		// The tools actually advertised for this call, resolved before
		// assembly rather than after it, so an asset can condition on
		// what the model can really call.
		Tools:          advertisedTools,
		Workspace:      dir,
		WorkspaceClass: wsClass,
		Lifecycle:      prompt.LifecycleTurn,
		Flags: map[string]bool{
			"auto_compact":     l.AutoCompactEnabled(),
			"skip_permissions": l.Config.PermissionsSkipped(),
		},
		Values: map[string]string{
			valBaseSystem:   l.SystemPrompt,
			valSkillsIndex:  l.SkillIndex(),
			valMemoryPolicy: l.MemoryPolicy,
			valMemoryIndex:  l.MemorySection,
			valProjectRules: rules,
			valAgentPrompt:  agentCfg.Prompt,
			valOrchestrator: l.orchestrationFor(ctx, sessionID, resolveAgent, profile.Model),
			valModelQuirk:   quirkNote(profile.Model),
		},
	}
}

// promptAssets returns the inventory, built once per Loop. The registry
// is immutable after construction, so sharing it across turns is safe and
// is what makes the asset IDs mean the same thing in every request.
func (l *Loop) promptAssets() *prompt.Registry {
	l.promptOnce.Do(func() { l.promptReg = promptRegistry() })
	return l.promptReg
}

// nextFallback takes the next link in the chain, if err is a failure
// worth trying another endpoint over.
//
// The classifier lives here rather than at the call sites because there
// are two of them — a request that failed outright, and a stream that died
// before saying anything — and they have to agree. They did not: both used
// to advance the chain on any error at all, so a malformed request was
// sent to every configured fallback in turn and the documented rule that
// only rate limits, outages, connectivity and credential failures move to
// another model was true only of the classifier, which nothing called.
func (l *Loop) nextFallback(chain []attempt, at *int, err error) (attempt, bool) {
	if !worthFallingBackOver(err) {
		return attempt{}, false
	}
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
