package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"localcode/internal/events"
	"localcode/internal/hooks"
	"localcode/internal/prompt"
	"localcode/internal/provider"
	"localcode/internal/tools"
	"localcode/internal/trace"
)

// sendWithModelText drives one full agent turn. displayText is what gets
// recorded as the message.user event (what the user actually typed);
// modelText is what the model receives as the user turn's content — they
// differ for /skill <name> and custom commands, where the model needs the
// expanded body but the transcript should stay readable. agentOverride and
// modelOverride, if non-empty, apply for this turn only (a custom
// command's "agent"/"model" frontmatter) without changing the session's
// standing agent.
//
// origin describes the opening message when it is not simply what the
// person typed: which entry the message as a whole belongs to, and
// which parts of it came from somewhere else. A command expansion is
// the case that needs both, since it can splice a file and the output
// of a shell command into the middle of its own text.
func (l *Loop) sendWithModelText(ctx context.Context, sessionID, agentName, displayText, modelText, agentOverride, modelOverride string, origin ...messageOrigin) error {
	resolveAgent := agentName
	if agentOverride != "" {
		resolveAgent = agentOverride
	}

	// The source tag for the user message this turn opens with, when it
	// is not the user's own typing: a skill body, a command expansion.
	// It travels on the message block rather than in a slice here,
	// because the message outlives this call and the slice does not.
	openingSource := ""
	var openingSpans []provider.BlockSource
	openingAuto := false
	if len(origin) > 0 {
		openingSource, openingSpans, openingAuto = origin[0].source, origin[0].spans, origin[0].auto
	}
	// A sub-agent's first message is the task its parent wrote. The
	// parent's own manifest names it too, from the tool_use block that
	// carries it, but the child's request is the one nobody was
	// describing: the same text arrives here as the turn's opening
	// message and used to be indistinguishable from something a person
	// typed.
	if d, ok := delegatedTaskFrom(ctx); ok && d.task == modelText && openingSource == "" {
		openingSource = childInputEntry(d.agent, d.task).ID
	}

	// One trace id per turn, inherited rather than minted when this turn
	// is itself a sub-agent's. Everything below writes under it, and so
	// does every tool call and every delegation that comes out of it.
	ctx, traceID := traceCtx(ctx)

	// One Smart Agent answer for this whole turn, taken here and pinned to
	// the context every part of it runs under. A turn admitted with the
	// specialists' tool allowlist keeps it even if the switch is flipped
	// while it is in a tool loop, and so do its cache markers, its
	// credential guards and its trace records. A delegation arrives with
	// its parent's snapshot already set and keeps that instead. See
	// config.WithSmartAgent.
	ctx = l.pinSmart(ctx)

	profileName, profile, err := l.profileFor(ctx, resolveAgent)
	if err != nil {
		return fmt.Errorf("resolve profile for agent %q: %w", resolveAgent, err)
	}

	// Per-agent system prompt addition and tool scoping — this is what
	// makes agentName more than just a model choice. An empty AgentConfig
	// (agent not found, or found with no Prompt/Tools set) is a no-op:
	// same behavior as before per-agent config existed.
	agentCfg := l.agentConfig(ctx, resolveAgent)

	// The tool allowlist for this turn, resolved before the prompt is
	// assembled rather than after, so the assembly can condition on the
	// tools the model will actually be offered. Read once here for the
	// same reason it always was: flipping Smart Agent off mid-turn must
	// not leave the model holding a tool_use for a tool the next
	// request no longer advertises.
	allowedTools := l.toolsForTurn(ctx, agentCfg)
	advertised := l.Tools.NamesFor(ctx, allowedTools)

	run, err := l.buildRun(ctx, sessionID, resolveAgent, agentCfg, profileName, profile, modelOverride, 0, advertised)
	if err != nil {
		return err
	}
	// Where to go if this model will not answer. Empty unless Smart Agent
	// is on and the profile names somewhere. See fallback.go.
	chain := l.fallbackChain(ctx, run.profile)
	chainAt := 0

	turnStarted := time.Now()
	l.traceSpan(ctx, traceID, sessionID, trace.SpanTurnStart, trace.Record{
		Agent: resolveAgent, Profile: run.profileName, Model: run.profile.Model, Provider: run.profile.Provider,
	})

	// Tokens-per-second describes the turn about to run, not the one
	// before it, so the accumulator starts empty here.
	l.startTurnRate(sessionID)

	compactions := 0
	if l.maybeAutoCompact(ctx, sessionID, run.client, run.profile, run.system, run.systemBlocks) {
		compactions++
		// No compact span here. compactHistory writes the one lifecycle
		// record when the history is actually replaced; a second one
		// from the caller made an automatic compaction two compact
		// records where a manual one is a single record, and the count
		// this line existed to convey is already on the turn.end record
		// as Compactions.
		l.runCompactHook(ctx, sessionID, "automatic")
	}

	// modelText differs from displayText for /skill <name>, custom
	// commands, and /init — the transcript shows the short command the
	// user typed, but the model needs the expanded body. Persist both so
	// rehydrateHistory can reconstruct the exact message the model saw,
	// not just what's shown on screen.
	userMsgData := map[string]any{"text": displayText}
	if modelText != displayText {
		userMsgData["model_text"] = modelText
	}
	// The source id, not the classification: rehydration looks the
	// entry back up from it, so the event log carries one string and
	// the constructors stay the single definition of what that string
	// means.
	if openingSource != "" {
		userMsgData["source"] = openingSource
	}
	if len(openingSpans) > 0 {
		userMsgData["sources"] = openingSpans
	}
	if openingAuto {
		userMsgData["auto"] = true
	}
	l.Store.Append(sessionID, events.TypeUserMessage, userMsgData)
	// A new turn: forget which files the last one had already copied, so
	// this one takes its own pre-images. See checkpoint.go.
	l.beginTurn(sessionID)

	l.appendHistory(sessionID, provider.Message{
		Role: provider.RoleUser,
		Content: []provider.Block{{
			Type: provider.BlockText, Text: modelText,
			Source: openingSource, Sources: openingSpans,
		}},
	})

	// Rescues come in two kinds, in order.
	//
	// The first summarizes, which is what the session wants: it keeps the
	// meaning of the conversation and costs a model call. Every one after
	// it is a forced trim, which is what the session needs when that was
	// not enough — it cannot fail, because it is allowed to cut into the
	// messages themselves, and each one cuts deeper than the last.
	//
	// Summarizing twice would be the wrong second attempt: a second
	// overflow after a successful compaction means the trouble is not the
	// length of the history, and asking the model again would spend the
	// rest of the window finding that out.
	rescues := 0
	// How many times this turn has moved down the fallback chain. Reported
	// with the turn rather than kept for its own sake: a session that is
	// quietly answering on the third model in the chain is a fact about
	// what the answers cost and how good they are.
	fallbacks := 0
	// Same-endpoint retries: how many this turn has spent in total, and
	// how many against the endpoint currently being asked. The second
	// resets when a fallback moves the turn elsewhere and when an answer
	// arrives, so each endpoint gets its own bounded allowance rather
	// than the first one spending everyone's.
	retries := 0
	// A retry that has served its backoff and is waiting for the request
	// to actually leave. Committed at the provider call, so a turn
	// blocked by a hook in between counts nothing. See pendingRetry.
	var pending pendingRetry
	sameTries := 0
	trimBudget := l.contextWindow(ctx, run.profile) / 2

	// State for keep_going: whether this turn has actually done anything
	// yet, whether the last thing it did was refused, and how many times
	// the model has already been told to carry on. See keepGoing.
	ranTools := false
	lastRefused := false
	nudgedSinceWork := false
	nudges := 0
	// Every tool call this turn has already made, so a carry-on that only
	// repeats them can be told from one that does something. A model told
	// to continue when it had in fact finished does not argue: it re-reads
	// a file or re-runs the build, and that used to count as work and buy
	// another nudge. See keepGoing.
	madeCalls := map[string]bool{}
	// Consecutive steps that asked for nothing this turn had not already
	// asked for. See the ceiling at the bottom of the loop.
	repeats := 0

	for {
		history := l.history(sessionID)
		messages := sendableHistory(history)

		req := provider.ChatRequest{
			Model:        run.profile.Model,
			System:       run.system,
			SystemBlocks: run.systemBlocks,
			Messages:     messages,
			Tools:        l.Tools.SpecsFor(ctx, allowedTools),
			// Sized against what is left of the window rather than taken
			// from config as-is. A max_tokens larger than the room
			// remaining is refused by the server as one total that does
			// not fit — see context_budget.go for the arithmetic and the
			// error it produces.
			MaxTokens:   clampMaxTokens(run.maxTokens, l.contextWindow(ctx, run.profile), l.inputEstimate(sessionID, run.system, messages)),
			Temperature: run.profile.Temperature,
			// The stable half of the request is the tools and the system
			// prompt, and in an agent session it is the same bytes every
			// turn. Marking it is the single largest cost saving
			// available, and it is part of the Smart Agent bundle because
			// it changes the shape of the request on the wire: a
			// breakpoint the provider does not honour is harmless, and it
			// is still not a change to make to everyone's requests
			// silently. See provider.ChatRequest.CachePrefix.
			CachePrefix: l.smartOn(ctx),
			// This conversation's answer if it has one, the profile's
			// otherwise, and nothing at all unless somebody asked — which
			// is what keeps every request byte-identical to what it was
			// for anybody who has not set it. See effort.go.
			Effort: l.effortFor(sessionID, run.profile),
		}

		// The last chance to say no, and the point where a policy can add
		// context the model would not otherwise have. Injected text goes
		// on the end of the system prompt, which does mean a hook that
		// injects on every call gives up system-prompt caching for that
		// session — a trade the person who wrote the hook is making
		// deliberately.
		// The request this iteration actually sends, described.
		//
		// The tool definitions are part of that: a tool description
		// steers the model exactly as a system instruction does, and an
		// MCP server's description is written by another process. They
		// stay native tool definitions on the wire and gain a manifest
		// identity here, so the record covers the whole model-visible
		// surface rather than the system block alone.
		// Derived from the messages this request carries, not from what
		// this invocation happened to build. A tool result stays in
		// history and is sent again on every later request, including
		// the next user turn and the ones after a restart; naming it
		// only on the request that first carried it made every manifest
		// after that one say the request contained no external content.
		iterManifest := run.manifest.WithRuntimeEntries(
			append(toolEntries(req.Tools), historyEntries(messages, l.isDelegatedSession(sessionID))...)...)
		if len(l.Config.Hooks) > 0 {
			out := hooks.RunOutcome(ctx, l.Config.Hooks, hooks.EventPreModel, l.SessionDir(sessionID), map[string]any{
				"session_id": sessionID,
				"agent":      resolveAgent,
				"model":      run.profile.Model,
				"provider":   run.profile.Provider,
			})
			if out.Blocked {
				reason := out.Reason
				if reason == "" {
					reason = "blocked by a pre_model hook"
				}
				l.Store.Append(sessionID, events.TypeError, map[string]any{"error": reason})
				return fmt.Errorf("pre_model hook: %s", reason)
			}
			var hookEntries []prompt.Entry
			for i, extra := range out.Context {
				req.System = req.System + "\n\n" + extra
				req.SystemBlocks = append(req.SystemBlocks, provider.SystemBlock{Text: extra, Asset: "hook.pre_model"})
				req.CachePrefix = false
				// The occurrence suffix rather than a separate literal,
				// so the id every call can produce starts with the same
				// text a reader can find. See
				// TestTheInventoryAndTheCodeNameTheSameEntries, which
				// takes the emitted ids from the syntax.
				suffix := ""
				if i > 0 {
					suffix = fmt.Sprintf("#%d", i+1)
				}
				// On the record as a runtime asset: hook text joins the
				// request after assembly by nature (a hook runs per
				// request), and the manifest carrying its identity, hash
				// and size is what keeps the one part of the prompt no
				// inventory can pre-declare from also being the one part
				// nobody can see.
				hookEntries = append(hookEntries, prompt.RuntimeEntry(
					"hook.pre_model"+suffix, prompt.KindRuntimeReminder, prompt.FromHook, prompt.TrustUser,
					prompt.PlaceSystem, extra, "injected by a pre_model hook for this request"))
			}
			iterManifest = iterManifest.WithRuntimeEntries(hookEntries...)
		}

		// The hooks have allowed this request and it is going out, which
		// is the moment a retry becomes an attempt rather than an
		// intention. A context that ended in between means it is not
		// going out after all: nothing is counted and the turn stops.
		if !l.commitRetry(ctx, traceID, sessionID, run, &pending, &sameTries, &retries) {
			return ctx.Err()
		}

		// The complete record of this request, written before it goes.
		// Everything model-visible that this iteration adds is already
		// in it, so the manifest the trace names describes what was
		// sent rather than what was planned.
		iterManifest.At = time.Now()
		l.Manifests.Put(iterManifest)

		trRun := run
		trRun.manifest = iterManifest

		callStarted := time.Now()
		stream, err := run.client.Chat(ctx, req)
		if err != nil {
			l.traceModel(ctx, traceID, sessionID, trRun, streamUsage{}, "", time.Since(callStarted), err)
			// Too long is a recoverable condition, not an error to hand
			// back: compacting is exactly what the session needs and what
			// the user would have to do by hand anyway. Reported in the
			// transcript either way, so a turn that took a detour through
			// a summary does not look like it simply took a while.
			if isContextOverflow(err) && rescues < maxRescues {
				rescues++
				if rescues == 1 {
					l.Store.Append(sessionID, events.TypeError, map[string]any{
						"error":     "the conversation no longer fits in this model's context window; summarizing it and retrying",
						"recovered": true,
					})
					if cerr := l.compactHistory(ctx, sessionID, run.client, run.profile, run.system, run.systemBlocks, "", CompactRescue); cerr == nil {
						compactions++
						// Likewise no compact span: compactHistory
						// records the replacement, and why this one
						// happened is the rescue's own business, which
						// the error record beside it already says.
						l.runCompactHook(ctx, sessionID, "overflow")
						continue
					} else {
						l.Store.Append(sessionID, events.TypeError, map[string]any{
							"error":     fmt.Sprintf("could not summarize to recover space (%v); trimming instead", cerr),
							"recovered": true,
						})
					}
				}
				// Second time, or the summary itself could not be made:
				// stop negotiating and make it fit. forceFit drops from
				// the front and then cuts into what is left, so unlike
				// summarizing there is no way for it not to work — which
				// matters, because this is the difference between a
				// session that carries on and one that is finished.
				//
				// Each refusal cuts deeper than the last, rather than
				// trying the same size again. shrinkBudget takes the
				// history's measured size into account precisely because
				// the estimate cannot be trusted to be right about the
				// window: a request the server has just refused can
				// measure as comfortably fitting on this side, and a
				// budget derived from the window alone would then cut
				// nothing and lose the turn.
				trimBudget = shrinkBudget(trimBudget, run.system, sendableHistory(l.history(sessionID)))
				trimmed, changed := forceFit(run.system, l.history(sessionID), trimBudget)
				if changed {
					l.setHistory(sessionID, trimmed)
					l.clearUsage(sessionID)
					l.Store.Append(sessionID, events.TypeError, map[string]any{
						"error":     "still too long — the oldest part of the conversation has been dropped so this turn can continue",
						"recovered": true,
					})
					continue
				}
			}
			// The model will not answer, and there is somewhere else to
			// ask. Everything the profile decides is re-derived, prompt
			// included: a fallback on another family gets the policy and
			// the quirk note written for it, not the ones written for the
			// model that just failed.
			//
			// worthFallingBackOver is what makes "somewhere else" mean
			// anything. A request another endpoint would refuse in exactly
			// the same way — a bad parameter, a tool schema the API will
			// not take — is not a reason to ask three more models the same
			// broken question, and walking the chain over it buries the
			// configuration error that actually needs fixing under
			// whatever the last model in the chain said.
			// A transient failure is asked about again here first, so a
			// single 429 does not move the conversation to a different
			// model and a different cache prefix. Only when the bounded
			// retries are spent, or the cause is not the transient kind,
			// does the chain get consulted. A turn cancelled during the
			// backoff stops here instead: walking the chain with a dead
			// context would record fallbacks no provider ever saw.
			switch l.maybeRetrySameEndpoint(ctx, sessionID, run, err, &sameTries, &pending) {
			case retryReady:
				continue
			case retryCancelled:
				l.Store.Append(sessionID, events.TypeError, map[string]any{"error": err.Error()})
				return fmt.Errorf("chat request: %w", err)
			}
			if next, ok := l.nextFallback(chain, &chainAt, err); ok {
				if newRun, berr := l.buildRun(ctx, sessionID, resolveAgent, agentCfg, next.name, next.profile, modelOverride, chainAt, advertised); berr == nil {
					l.reportFallback(sessionID, run, newRun, err)
					fallbacks++
					l.traceSpan(ctx, traceID, sessionID, trace.SpanFallback, trace.Record{
						Profile: newRun.profileName, Model: newRun.profile.Model, Provider: newRun.profile.Provider,
						Fallbacks: fallbacks, Error: err.Error(),
					})
					l.runRetryHook(ctx, sessionID, run, newRun, err)
					run = newRun
					// A new endpoint gets its own retry allowance, and
					// any retry still pending belonged to the old one.
					sameTries = 0
					pending = pendingRetry{}
					// The trim budget belongs to the old window.
					trimBudget = l.contextWindow(ctx, run.profile) / 2
					continue
				}
			}
			l.Store.Append(sessionID, events.TypeError, map[string]any{"error": err.Error()})
			return fmt.Errorf("chat request: %w", err)
		}

		assistantBlocks, toolUses, stopReason, usage, err := l.consumeStream(sessionID, stream)
		l.traceModel(ctx, traceID, sessionID, trRun, usage, stopReason, time.Since(callStarted), err)
		if len(l.Config.Hooks) > 0 {
			// Fire and forget: the reply is already here, so there is
			// nothing left to block. This is the point for logging what a
			// model actually cost, or for reacting to a truncated answer.
			hooks.Run(ctx, l.Config.Hooks, hooks.EventPostModel, l.SessionDir(sessionID), map[string]any{
				"session_id":    sessionID,
				"agent":         resolveAgent,
				"model":         run.profile.Model,
				"stop_reason":   stopReason,
				"input_tokens":  usage.inputTokens,
				"output_tokens": usage.outputTokens,
				"cache_read":    usage.cacheRead,
			})
		}
		if err != nil {
			// A stream that died before saying anything can be asked
			// again elsewhere. One that had already written into the
			// transcript cannot: the conversation would end up carrying a
			// partial answer followed by a whole one.
			if !firstOutput(assistantBlocks) {
				// Same order as a request that failed outright: a
				// stream that died of a transient cause is re-asked
				// here before the chain is spent on it, and a turn
				// cancelled during the backoff ends rather than
				// consulting the chain.
				switch l.maybeRetrySameEndpoint(ctx, sessionID, run, err, &sameTries, &pending) {
				case retryReady:
					continue
				case retryCancelled:
					return err
				}
				if next, ok := l.nextFallback(chain, &chainAt, err); ok {
					if newRun, berr := l.buildRun(ctx, sessionID, resolveAgent, agentCfg, next.name, next.profile, modelOverride, chainAt, advertised); berr == nil {
						l.reportFallback(sessionID, run, newRun, err)
						fallbacks++
						l.traceSpan(ctx, traceID, sessionID, trace.SpanFallback, trace.Record{
							Profile: newRun.profileName, Model: newRun.profile.Model, Provider: newRun.profile.Provider,
							Fallbacks: fallbacks, Error: err.Error(),
						})
						l.runRetryHook(ctx, sessionID, run, newRun, err)
						run = newRun
						sameTries = 0
						trimBudget = l.contextWindow(ctx, run.profile) / 2
						continue
					}
				}
			}
			return err
		}
		// The endpoint answered, so the next failure is a new incident
		// with its own retry allowance rather than a continuation of the
		// last one.
		sameTries = 0
		if usage.hasUsage {
			l.recordUsage(sessionID, run.profile.Model, l.contextWindow(ctx, run.profile), usage)
		}

		// Nothing is appended for a reply that produced nothing. A turn
		// cancelled before the first token gets here with no blocks —
		// the provider's stream goroutine returns on ctx.Done and closes
		// the channel without a terminal event, which reads as a clean
		// finish — and an assistant message with empty content is
		// rejected by name on the next request, for the rest of the
		// session's life. sendableHistory would drop it anyway; not
		// recording it keeps the in-memory history honest, and matches
		// what rehydrateHistory already reconstructs from the log.
		if len(assistantBlocks) > 0 {
			l.appendHistory(sessionID, provider.Message{Role: provider.RoleAssistant, Content: assistantBlocks})
		}

		// A reply that asked for tools is answered by running them,
		// whatever the server said about why it stopped.
		//
		// The stop reason used to be the gate, and local servers do not
		// agree on it: several report "stop" on a reply that carries tool
		// calls, and some send no finish_reason at all. The turn then ended
		// with the calls sitting in the transcript unrun — the model had
		// said what it was about to do and then, from the outside, simply
		// stopped, and the next prompt ("carry on") made it pick up again.
		//
		// max_tokens is the one exception: the reply was cut off mid-write,
		// so a tool call at the end of it has arguments that stop partway
		// through and running them means acting on a truncated instruction.
		wantsTools := len(toolUses) > 0 && stopReason != "max_tokens"
		if !wantsTools {
			// A reply that ran into its own length cap is not a reply that
			// finished, and it is indistinguishable from one that did:
			// providers report it as stop_reason "max_tokens" and this
			// used to drop the fact on the floor, so the text simply
			// stopped, often mid-sentence, mid-function. The default cap
			// is deliberately modest, which makes this common rather than
			// exotic — and unexplained, someone reasonably concludes the
			// model is broken rather than that a number needs raising.
			if stopReason == "max_tokens" {
				l.Store.Append(sessionID, events.TypeError, map[string]any{
					"error": fmt.Sprintf(
						"the reply hit this profile's max_tokens limit of %d and was cut off — raise max_tokens on the %q profile in config.json for longer answers",
						clampMaxTokens(run.maxTokens, l.contextWindow(ctx, run.profile), l.inputEstimate(sessionID, run.system, messages)),
						run.profile.Model),
					"recovered": true,
				})
			}
			if text, ok := l.keepGoing(sessionID, run.profile, stopReason, assistantBlocks, ranTools, lastRefused, nudgedSinceWork, nudges); ok {
				nudges++
				nudgedSinceWork = true
				l.Store.Append(sessionID, events.TypeError, map[string]any{
					"error": fmt.Sprintf(
						"the model ended its turn with the task unfinished, so localcode told it to carry on (%d of %d — keep_going for %q)",
						nudges, l.effectiveKeepGoing(run.profile), run.profile.Model),
					"recovered": true,
				})
				// Persisted as the user message it is, marked auto so no
				// client shows it as something the person typed. Without
				// this event, a daemon restarted mid-session rebuilt the
				// history with two assistant messages back to back — a
				// shape Bedrock rejects outright — and a conversation the
				// model remembered differently from how it happened.
				l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{
					"text": text, "auto": true, "source": "reminder.keep_going",
				})
				// localcode's own words, arriving in a user-role message
				// that the user did not write. The role does not say
				// that; the tag on the block does, and it stays with the
				// message for as long as the message is sent.
				l.appendHistory(sessionID, provider.Message{
					Role:    provider.RoleUser,
					Content: []provider.Block{{Type: provider.BlockText, Text: text, Source: "reminder.keep_going"}},
				})
				continue
			}

			l.traceSpan(ctx, traceID, sessionID, trace.SpanTurnEnd, trace.Record{
				Agent: resolveAgent, Profile: run.profileName, Model: run.profile.Model,
				DurationMS: time.Since(turnStarted).Milliseconds(),
				Fallbacks:  fallbacks, Retries: retries, Compactions: compactions, FinishReason: stopReason,
			})

			if len(l.Config.Hooks) > 0 {
				// Fire-and-forget: a Stop hook is purely a notification
				// point here (e.g. "ping me when a turn finishes") — its
				// block decision, if any, has no effect, since there's no
				// well-defined "keep going without a new user turn" flow
				// to force.
				hooks.Run(ctx, l.Config.Hooks, hooks.EventStop, l.SessionDir(sessionID), map[string]any{"session_id": sessionID})
			}
			return nil
		}

		// Two different questions, two different tests.
		//
		// The repeat guard asks "did this step ask for anything it had not
		// already asked for", because a step that repeats every call it
		// has made has changed nothing and the next one has the same
		// input. The carry-on guard asks something narrower: did the nudge
		// produce a change, or only more looking. See changedSomething for
		// what that distinction cost when the two shared one test.
		fresh := newWork(madeCalls, toolUses)
		if fresh {
			repeats = 0
		} else {
			repeats++
		}
		// A carry-on is earned by a call that is BOTH new and a change.
		// New alone was the old test and it let a prodded model buy
		// another nudge by looking somewhere it had not looked; a change
		// alone would let it buy one by re-running the same command. Only
		// the pair is progress. See changedSomething.
		if fresh && changedSomething(toolUses) {
			nudgedSinceWork = false
		}
		// Kept for the notice below: a turn ended for repeating itself
		// should say what it repeated, not only that it did.
		lastCalls := toolUses
		resultBlocks, refused, ended := l.runTools(ctx, sessionID, toolUses, allowedTools, l.contextWindow(ctx, run.profile))
		ranTools = true
		lastRefused = refused
		resultBlocks = append(resultBlocks, l.takeInjected(sessionID)...)
		l.appendHistory(sessionID, provider.Message{Role: provider.RoleUser, Content: resultBlocks})

		// A tool that was the end of the work. The results are in the
		// history first, so the record and the model's copy both show the
		// call that finished it.
		if ended {
			return nil
		}

		// A model that will not stop.
		//
		// The loop above has exactly one reason to end: the model stops
		// asking for tools. A model that asks for the same tool with the
		// same arguments, forever, is therefore unbounded — and it is not
		// hypothetical, it is what a debate reviewer did after recording
		// its verdict, for a thousand requests, holding the session busy
		// so that everything typed afterwards was injected into a turn
		// that would never finish.
		//
		// Repetition rather than a step count is the signal, because a
		// long turn doing real work is ordinary and must not be cut off.
		// newWork already tracks every (tool, arguments) pair this turn
		// has made; a step that adds none of them is a step that did
		// nothing new, and several in a row is a loop rather than work.
		// Re-running one command after an edit is not caught: the edit is
		// itself new work and resets the count.
		//
		// The ceiling is the daemon's live setting rather than the
		// constant, and zero is off: see config.RepeatLimit for why a
		// person gets to decide. The notice names the calls, because
		// "the same tools with the same arguments" was read as "one call
		// three times" by someone watching a model alternate two reads,
		// and the rule is about steps that add nothing, not about one
		// call.
		if limit := l.RepeatLimit(); limit > 0 && repeats >= limit {
			l.Store.Append(sessionID, events.TypeError, map[string]any{
				"error": fmt.Sprintf(
					"stopped: %d steps in a row only repeated tool calls this turn had already made (%s). "+
						"Nothing new was tried, so the turn was ended rather than left running. "+
						"/repeat-limit changes the ceiling or turns this off.", repeats, describeCalls(lastCalls)),
				"recovered": true,
			})
			return nil
		}
	}
}

// injectedPreface tells the model where the text that follows came from.
// Without it the message arrives in the same user turn as the tool results
// and reads like part of a tool's output.
// maxRescues bounds how many times one turn will recover from a refused
// request before giving up. The first is a summary; the rest are forced
// trims, each aiming at two thirds of what the last one measured, so five
// of them reach about a fifth of where the conversation started. Bounded
// rather than open-ended because a server that phrases some other refusal
// like an overflow must not become an endless retry.
const maxRescues = 5

const injectedPreface = "[The user sent this while you were working — take it into account from here on]\n\n"

// takeInjected collects anything the user typed since this turn started
// and returns it as blocks to travel with the tool results.
//
// Travelling with them, rather than forming a message of its own, because
// the tool results are themselves a user message, and two user messages
// back to back is not a shape every provider accepts — Bedrock's Converse
// API rejects it outright.
//
// Nothing happens here unless PendingInput is wired up (the daemon does
// it); a Loop built without one simply has no mid-turn input.
func (l *Loop) takeInjected(sessionID string) []provider.Block {
	if l.PendingInput == nil {
		return nil
	}
	var out []provider.Block
	for {
		text, ok := l.PendingInput(sessionID)
		if !ok {
			return out
		}
		// Recorded only now, when it actually reaches the model. Writing
		// it at the moment it was typed would put a line in the transcript
		// that the model had not yet been told about — and if the turn
		// ended before the next tool call, it would be answered as an
		// ordinary next message and recorded a second time.
		l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{
			"text": text, "injected": true, "source": "injected.user",
		})
		// Tagged, because of where it lands. This is the person typing,
		// which is the most instruction-authoritative thing a request
		// carries, and it travels inside a user-role message otherwise
		// made entirely of tool results, which is the least. The role
		// says the same word for both; the tag is what tells them
		// apart, and the preface is localcode's own framing rather than
		// the person's, so only what they typed is inside the span.
		body := injectedPreface + text
		out = append(out, provider.Block{
			Type: provider.BlockText, Text: body, Source: "injected.user",
			Sources: []provider.BlockSource{{
				ID: "injected.user", From: len(injectedPreface), To: len(body),
			}},
		})
	}
}

// streamUsage carries the token usage seen while draining one stream, plus
// enough timing information to compute tokens-per-second.
//
// elapsed is generation time, not wall-clock time for the request: it runs
// from the first piece of output to the last. Everything before the first
// token — prefill of a long prompt, and on a local server the wait behind
// whatever else is queued — is real waiting, but it is not generation, and
// dividing the output tokens by it reported a rate several times below
// what the model was actually doing. That gap is widest on exactly the
// long-context turns where someone looks at the number.
type streamUsage struct {
	hasUsage     bool
	inputTokens  int
	outputTokens int
	// What the provider's prompt cache did for this request, where it
	// says. Not folded into inputTokens: they are priced differently, and
	// the read count is the only way to tell a working cache breakpoint
	// from one that is silently doing nothing.
	cacheRead  int
	cacheWrite int
	elapsed    time.Duration
}

// tpsTickInterval is how often a live tokens-per-second estimate goes out
// while a model is still generating. A var so a test can shorten it.
var tpsTickInterval = time.Second

// consumeStream drains one model response, mirroring each piece into the
// session's event log, and returns the assistant's content blocks, any
// tool_use blocks it requested, and whatever token usage the provider
// reported (see provider.EventUsage — not every provider/request reports
// it, hence streamUsage.hasUsage).
func (l *Loop) consumeStream(sessionID string, stream <-chan provider.StreamEvent) (blocks []provider.Block, toolUses []provider.Block, stopReason string, usage streamUsage, err error) {
	var text strings.Builder
	toolNames := map[string]string{}
	toolInputs := map[string]*strings.Builder{}
	// The model's reasoning, kept for the message it belongs to. It goes
	// in front of the answer and the tool calls, which is the order the
	// API requires of a continuation.
	var thinking []provider.Block

	// Generation timing, and the live rate estimate built on top of it.
	// deltas counts stream deltas, not tokens — the authoritative token
	// count only arrives with the provider's usage report at the end of
	// the stream, and the whole point of a live figure is to exist before
	// then. For the local servers this is aimed at (llama.cpp, Ollama,
	// vLLM) a delta is one token, so the estimate is close; for providers
	// that batch several tokens per delta it reads low. It is shown with
	// a "~" for that reason, and is replaced by the real number the
	// moment the stream ends.
	var genStart, lastTick time.Time
	deltas := 0
	generated := func() {
		if genStart.IsZero() {
			genStart = time.Now()
			lastTick = genStart
		}
		deltas++
		if time.Since(lastTick) < tpsTickInterval {
			return
		}
		lastTick = time.Now()
		if secs := time.Since(genStart).Seconds(); secs > 0 {
			l.Store.Broadcast(sessionID, events.TypeUsage, map[string]any{
				"tps":       float64(deltas) / secs,
				"estimated": true,
				"show_tps":  l.ShowTPS(),
			})
		}
	}

	for ev := range stream {
		switch ev.Type {
		case provider.EventTextDelta:
			text.WriteString(ev.TextDelta)
			l.Store.Append(sessionID, events.TypeMessagePartDelta, map[string]any{"text": ev.TextDelta})
			generated()

		case provider.EventThinkingDelta:
			// Broadcast, not appended: reasoning is worth watching while
			// it happens and is not part of the transcript afterwards.
			// The API does not want it back on a later turn either, and
			// the block that does have to go back is carried in memory
			// for exactly as long as that is true — see EventThinkingEnd
			// and toAnthropicMessages.
			l.Store.Broadcast(sessionID, events.TypeThinkingDelta, map[string]any{"text": ev.ThinkingDelta})
			generated()

		case provider.EventThinkingEnd:
			// First in the message, before any text or tool_use: the API
			// requires that order, and a continuation whose thinking
			// arrives after the tool call it explains is refused.
			thinking = append(thinking, provider.Block{
				Type: provider.BlockThinking, Text: ev.ThinkingDelta, Signature: ev.Signature,
			})
			l.Store.Broadcast(sessionID, events.TypeThinkingEnd, map[string]any{})

		case provider.EventToolUseStart:
			toolNames[ev.ToolUseID] = ev.ToolName
			toolInputs[ev.ToolUseID] = &strings.Builder{}

		case provider.EventToolUseInputDelta:
			if b, ok := toolInputs[ev.ToolUseID]; ok {
				b.WriteString(ev.InputDelta)
			}
			generated()

		case provider.EventToolUseEnd:
			input := ev.ToolInput
			if len(input) == 0 {
				if b, ok := toolInputs[ev.ToolUseID]; ok && b.Len() > 0 {
					input = json.RawMessage(b.String())
				} else {
					input = json.RawMessage("{}")
				}
			}
			// tool.start is emitted here rather than at ToolUseStart so it
			// can carry the arguments: they stream in one fragment at a
			// time and are only complete now. This still lands before
			// message.part.end, which is what rehydrateHistory pairs it
			// against, and it is closer to when the tool actually runs —
			// nothing executes until the whole stream has been drained.
			l.Store.Append(sessionID, events.TypeToolStart, map[string]any{
				"tool_use_id": ev.ToolUseID,
				"name":        toolNames[ev.ToolUseID],
				"input":       string(input),
			})
			toolUses = append(toolUses, provider.Block{
				Type:      provider.BlockToolUse,
				ToolUseID: ev.ToolUseID,
				ToolName:  toolNames[ev.ToolUseID],
				ToolInput: input,
			})

		case provider.EventMessageStop:
			stopReason = ev.StopReason

		case provider.EventUsage:
			usage.hasUsage = true
			usage.inputTokens = ev.InputTokens
			usage.outputTokens = ev.OutputTokens
			usage.cacheRead = ev.CacheReadTokens
			usage.cacheWrite = ev.CacheWriteTokens

		case provider.EventError:
			// The answer so far is closed as a message before the error
			// is recorded, in that order because that is the order it
			// happened in.
			//
			// Without it the reply reaches the log as deltas and no
			// message.part.end, and an unterminated reply is one the
			// replay filter later deletes: collapseFinishedDeltas drops
			// every delta lying before the last part.end in the range, so
			// the moment any later message completes, the half-written
			// answer stops being drawn by anything. The bytes stay in the
			// log and no client ever reads them again.
			//
			// This is the record, which is a different question from the
			// history — that stays as it was, since a failed response is
			// not a turn and must not be sent back as one. The model did
			// say these words, and the session is where that is kept.
			if text.Len() > 0 {
				l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": text.String()})
			}
			l.Store.Append(sessionID, events.TypeError, map[string]any{"error": ev.Err.Error()})
			// Whatever had already been said comes back with the error.
			// Nothing is appended to the history from it — a failed
			// response is not a turn — but the caller has to be able to
			// tell a stream that died silently from one that died
			// half-way through an answer, because only the first can be
			// asked again somewhere else. See firstOutput.
			if text.Len() > 0 {
				blocks = append(blocks, provider.TextBlock(text.String()))
			}
			return blocks, toolUses, stopReason, usage, fmt.Errorf("provider stream error: %w", ev.Err)
		}
	}
	// A rate needs a span to measure across, and one delta is a point.
	// Providers that deliver a short reply in a single chunk would
	// otherwise divide the whole output by the microseconds between
	// receiving it and the stream closing, and report six-figure
	// tokens-per-second. Leaving elapsed at zero says "not measurable
	// here", and recordUsage keeps the last figure it did measure.
	if deltas >= 2 {
		usage.elapsed = time.Since(genStart)
	}

	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": text.String()})

	blocks = append(blocks, thinking...)
	if text.Len() > 0 {
		blocks = append(blocks, provider.TextBlock(text.String()))
	}
	blocks = append(blocks, toolUses...)
	return blocks, toolUses, stopReason, usage, nil
}

// runTools executes each requested tool call in order and returns the
// resulting tool_result blocks to feed back to the model. allowedTools, if
// non-empty, is enforced here too (not just in the specs the model saw) —
// a belt-and-suspenders check in case a model calls a tool it wasn't
// offered.
// runTools executes one batch of tool calls and returns the result blocks,
// plus whether any of them was refused rather than run — a deny rule, a
// blocking hook, or the person at the keyboard clicking Deny. See
// keepGoing for what that second value decides.
func (l *Loop) runTools(ctx context.Context, sessionID string, toolUses []provider.Block, allowedTools []string, window int) (blocks []provider.Block, refused, ended bool) {
	ctx = WithSessionID(ctx, sessionID)
	// Every tool in this turn resolves relative paths, and runs shell
	// commands, in this session's own directory. This is what replaced
	// os.Chdir: the workspace travels with the turn rather than being a
	// property of the process, so a second session working somewhere else
	// is simply a second context.
	ctx = tools.WithWorkingDir(ctx, l.SessionDir(sessionID))
	results := make([]provider.Block, 0, len(toolUses))
	for _, tu := range toolUses {
		var res tools.Result
		// A model can ask for several tools in one step, and Esc during the
		// first should not be followed by the other four running anyway.
		// Each still gets a result block, because a tool_use with no
		// tool_result is a history the provider rejects — and this history
		// is what a later /compact or a restart replays.
		if err := ctx.Err(); err != nil {
			res = tools.Result{Content: "not run: the turn was cancelled", IsError: true}
			l.Store.Append(sessionID, events.TypeToolEnd, map[string]any{
				"tool_use_id": tu.ToolUseID,
				"content":     res.Content,
				"is_error":    true,
				"input":       string(tu.ToolInput),
			})
			results = append(results, provider.ToolResultBlock(tu.ToolUseID, res.Content, true))
			continue
		}

		started := time.Now()
		// The name the model asked for, resolved against the roster it was
		// actually shown.
		//
		// Two failures used to arrive here identically and be answered the
		// same unhelpful way: a name that is a decorated form of a real
		// tool ("bash.command" for bash), and a name for a tool this agent
		// genuinely does not have. The first now runs, because the model
		// asked for something that exists and spelled it with a decoration
		// this repository can strip without inventing anything. The second
		// is refused with the roster attached, so the model can pick a
		// real name instead of guessing at the same wrong one — which is
		// what it did, five times in a row, in the transcript that
		// prompted this.
		//
		// Resolution is against NamesFor(allowedTools), never the whole
		// registry: a restricted agent cannot reach a tool it was not
		// offered by misspelling one, because the tool it wants is not in
		// the set being searched.
		name := tu.ToolName
		offered := l.Tools.AllowedNames(allowedTools)
		match, exact, candidates := tools.Nearest(offered, name)
		switch {
		case exact:
			res = l.Tools.Call(ctx, name, tu.ToolInput, "")
		case match != "":
			// Said, not silent. A call that ran under a different name
			// than the model wrote is a fact the transcript should carry,
			// and a model told which spelling worked stops producing the
			// other one.
			name = match
			res = l.Tools.Call(ctx, match, tu.ToolInput, "")
			res.Content = fmt.Sprintf("(there is no tool %q; ran %q, which is what that names. Use %q.)\n\n%s",
				tu.ToolName, match, match, res.Content)
		default:
			res = tools.Result{
				Content: tools.NoSuchTool(offered, tu.ToolName, candidates),
				IsError: true,
			}
		}
		// Timed around the call rather than around the execution, so a
		// tool that spent four minutes waiting on a permission prompt
		// reads as four minutes. That is where the time went.
		l.traceTool(ctx, trace.ID(ctx), sessionID, name, time.Since(started), res.IsError, firstLine(res.Content))
		// Capped here, before it becomes either a stored event or a
		// message — a tool result is the one part of a conversation whose
		// size nobody chose, and one `cat` of a log file could exceed the
		// whole window in a single message. See capToolResult.
		res.Content = capToolResult(res.Content, window)
		l.Store.Append(sessionID, events.TypeToolEnd, map[string]any{
			"tool_use_id": tu.ToolUseID,
			"content":     res.Content,
			"is_error":    res.IsError,
			// input carries this call's arguments (not just its result) —
			// the event log is otherwise the only place a tool_use block's
			// ToolInput would live, and rehydrateHistory needs it to
			// reconstruct the exact message sent to the model after a
			// restart. See rehydrateHistory in loop_rehydrate.go.
			"input": string(tu.ToolInput),
			// And whose material the result carries, with the spans, so
			// the rebuilt history describes the same sources the live
			// one did rather than one anonymous answer.
			"sources": res.Sources,
		})
		// The result is part of what every later request says, and it
		// is the least trusted text in any of them. Nothing is recorded
		// here: the manifest derives it from the tool_use block this
		// answers, which is in the history the request carries, so the
		// description lasts exactly as long as the text does.
		block := provider.ToolResultBlock(tu.ToolUseID, res.Content, res.IsError)
		// A result carrying somebody else's material says whose and
		// where. No pairing with the tool_use block can recover that:
		// the tool's name says which tool ran, not who wrote the words
		// it came back with.
		for _, src := range res.Sources {
			block.Sources = append(block.Sources, provider.BlockSource{
				ID: "child.result." + src.ID, From: src.From, To: src.To,
			})
		}
		results = append(results, block)
		if res.Refused {
			refused = true
		}
		// A terminal tool ends the turn even when it was one of several
		// called in the same step: the others still ran and still get
		// their result blocks, and the loop stops after this batch rather
		// than asking the model what to do next. See tools.Result.EndsTurn.
		if res.EndsTurn {
			ended = true
		}
	}
	return results, refused, ended
}

// firstLine is a tool result cut down to something a log line can carry:
// the first line, capped. The full result is in the session log; what a
// trace needs is enough to recognise which call this was.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// drainText concatenates every text delta from stream and returns the
// final text plus any token usage the provider reported — used for the
// internal compaction call, which must NOT go through consumeStream (that
// would write message.part.delta/end events into the visible transcript,
// making an internal summarization call look like a normal assistant
// reply).
func drainText(ctx context.Context, stream <-chan provider.StreamEvent) (string, streamUsage, string, error) {
	var text strings.Builder
	var usage streamUsage
	// Kept, because a utility call ends for the same reasons a
	// conversational one does and the difference matters most here: a
	// summary cut off at max_tokens is a summary that lost the end of
	// the conversation, and a trace that did not record why the call
	// stopped could not tell that from a short answer. It was dropped
	// on this path while the turn loop kept it, which made the one
	// call whose truncation is silent also the one nobody could see.
	var stop string
	for {
		select {
		case ev, ok := <-stream:
			if !ok {
				return text.String(), usage, stop, nil
			}
			switch ev.Type {
			case provider.EventTextDelta:
				text.WriteString(ev.TextDelta)
			case provider.EventUsage:
				usage.hasUsage = true
				usage.inputTokens = ev.InputTokens
				usage.outputTokens = ev.OutputTokens
			case provider.EventMessageStop:
				if ev.StopReason != "" {
					stop = ev.StopReason
				}
			case provider.EventError:
				return "", usage, stop, ev.Err
			}
		case <-ctx.Done():
			return "", usage, stop, ctx.Err()
		}
	}
}
