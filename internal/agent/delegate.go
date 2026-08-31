package agent

import (
	"context"
	"fmt"

	"localcode/internal/events"
	"localcode/internal/provider"
)

// delegateTarget decides whether this prompt should be answered by a
// cheaper sub-agent instead of the session's own, and names that agent.
//
// The guards matter more than the match: delegating from within a
// delegated session, or to the agent already running, would recurse
// forever, so both are refused before the patterns are even consulted.
func (l *Loop) delegateTarget(sessionID, agentName, text string) (string, bool) {
	// Snapshot rather than the live block: a client can change the target
	// agent and patterns mid-turn, and this needs one coherent view.
	cfg := l.Config.AutoDelegateSnapshot()
	if cfg == nil || !l.AutoDelegateEnabled() {
		return "", false
	}
	// Delegating to the agent that's already running would spawn a child
	// whose prompt matches the same rule, and so on without end.
	if cfg.Agent == "" || cfg.Agent == agentName {
		return "", false
	}
	// Task and delegated sessions are children (they carry a parent ID and
	// are hidden from the session list). Recursing from one is the other
	// half of the same infinite-regress problem.
	if sess, err := l.Store.Get(sessionID); err == nil && sess.ParentID != "" {
		return "", false
	}
	// A prompt naming another conversation is answered here, by the agent
	// the person is talking to.
	//
	// Not a preference. A delegated turn is a child session, and a child
	// session does not resolve references — deliberately, so that a model
	// writing "#X" into a sub-agent's prompt cannot reach another
	// conversation through it. Delegating a prompt the *person* wrote with a
	// reference in it therefore does not fail loudly; it sends the sub-agent
	// a bare "#S2" with no notice attached and no session_read in its tool
	// list, and the answer comes back confidently about a conversation
	// nobody read. Auto-delegation is a cost optimisation, and the cheaper
	// answer to a question that names something it cannot see is the wrong
	// answer.
	if names, _ := findSessionRefs(text); len(names) > 0 {
		return "", false
	}
	if !cfg.MatchesPrompt(text) {
		return "", false
	}
	return cfg.Agent, true
}

// delegatePrompt answers a turn from a sub-agent instead of the session's
// own model, and is the whole point of the feature: the sub-agent runs in
// its own session, so its (different) model never touches this session's
// cached prefix. Switching the session's own model would have invalidated
// tools, system prompt, and every prior turn at once.
//
// The transcript records the prompt and the answer exactly as an ordinary
// turn would, plus a marker naming the agent that handled it, so the
// delegation is visible rather than silently swapping models underneath
// the user.
func (l *Loop) delegatePrompt(ctx context.Context, sessionID, targetAgent, text string) error {
	if l.Tasks == nil {
		// No task manager wired up (a bare Loop in a test, say). Fall back
		// to answering normally rather than failing the turn.
		return l.sendWithModelText(ctx, sessionID, l.sessionAgent(sessionID), text, text, "", "")
	}

	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text})
	l.Store.Append(sessionID, events.TypeDelegated, map[string]any{"agent": targetAgent, "prompt": text})

	answer, err := l.Tasks.SpawnSync(ctx, sessionID, targetAgent, text)
	if err != nil {
		failure := fmt.Sprintf("delegation to %q failed: %v", targetAgent, err)
		l.Store.Append(sessionID, events.TypeError, map[string]any{"error": failure})
		// The prompt is already in the log, so a restart replays it — and
		// without this it replays with nothing after it, leaving the model
		// a question it never answered and the restored history a shape
		// the live one never had. Recording the failure as the reply keeps
		// the two the same and tells the model what happened, which is
		// better than silence either way.
		l.appendDelegatedTurn(sessionID, text, failure)
		return nil
	}

	// Record both halves in the history the main model sees, so its next
	// turn has the exchange as context even though it never ran for it.
	// Both are needed: appending only the answer would leave the history
	// with two assistant turns in a row, which some providers reject.
	l.appendDelegatedTurn(sessionID, text, answer)
	l.Store.Append(sessionID, events.TypeMessagePartDelta, map[string]any{"text": answer})
	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": answer})
	return nil
}

// sessionAgent reads a session's current agent, falling back to the
// configured default when the session is unknown.
func (l *Loop) sessionAgent(sessionID string) string {
	if sess, err := l.Store.Get(sessionID); err == nil && sess.Agent != "" {
		return sess.Agent
	}
	return "general-purpose"
}

// appendDelegatedTurn adds the prompt and the sub-agent's answer to the
// in-memory history the main model sees, without any provider call.
func (l *Loop) appendDelegatedTurn(sessionID, prompt, answer string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages[sessionID] = append(l.messages[sessionID],
		provider.Message{Role: provider.RoleUser, Content: []provider.Block{provider.TextBlock(prompt)}},
		provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock(answer)}},
	)
}
