package agent

import (
	"context"
	"fmt"
	"strings"

	"localcode/internal/events"
	"localcode/internal/prompt"
	"localcode/internal/provider"
)

// parseConfigCommand recognizes "/config" and "/config <rest>". ok is
// false for anything else.
func parseConfigCommand(text string) (arg string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "/config" {
		return "", true
	}
	if rest, found := strings.CutPrefix(trimmed, "/config "); found {
		return strings.TrimSpace(rest), true
	}
	return "", false
}

// handleConfigCommand answers "/config" locally — no model call. With no
// argument it reports the current live settings; "/config <setting>
// on|off" toggles auto_compact or show_tps process-wide (every session on
// this daemon, not just the one issuing the command — see
// Loop.autoCompactEnabled/showTPS) and broadcasts an events.TypeConfigChanged
// event so this session's clients update their display immediately.
func (l *Loop) handleConfigCommand(sessionID, displayText, arg string) error {
	// Appended before the switch below (rather than folded into
	// replyLocal at the end) because that switch can itself append a
	// TypeConfigChanged event — the user's own message must stay first in
	// the log, ahead of any event the command's side effect produces.
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText, "local": true})

	fields := strings.Fields(arg)
	var text string

	switch {
	case arg == "":
		text = l.configSummary()

	case len(fields) == 2 && (fields[1] == "on" || fields[1] == "off"):
		enabled := fields[1] == "on"
		switch fields[0] {
		case "auto_compact":
			l.SetAutoCompactEnabled(enabled)
			text = fmt.Sprintf("auto_compact: %s", onOff(enabled))
		case "show_tps":
			l.SetShowTPS(enabled)
			text = fmt.Sprintf("show_tps: %s", onOff(enabled))
		case "auto_delegate":
			l.SetAutoDelegateEnabled(enabled)
			text = fmt.Sprintf("auto_delegate: %s", onOff(enabled))
			// Turning it on without an auto_delegate block configured
			// would silently do nothing, so say so rather than letting the
			// user think it took effect.
			if enabled && l.Config.AutoDelegate == nil {
				text += "\n(no auto_delegate block in config.json, so nothing will be delegated — see docs/USAGE.md)"
			}
		case "smart_agent":
			l.SetSmartAgentEnabled(enabled)
			text = fmt.Sprintf("smart_agent: %s", onOff(enabled))
			// Turning it on with nothing to route to is legal and inert:
			// the specialists need a profile to run on, and without one
			// there is no roster and no orchestration prompt.
			if enabled && len(l.smartAgents(context.Background())) == 0 {
				text += "\n(no profiles configured, so no specialist agents could be created — see docs/USAGE.md)"
			} else if enabled {
				text += "\n(available: " + strings.Join(agentNamesOf(l.smartAgents(context.Background())), ", ") + ")"
			}
		default:
			text = fmt.Sprintf("unknown setting %q. usage: /config, /config auto_compact on|off, /config show_tps on|off, /config auto_delegate on|off, /config smart_agent on|off", fields[0])
		}
		if text != "" && knownSetting(fields[0]) {
			l.Store.Append(sessionID, events.TypeConfigChanged, map[string]any{
				"auto_compact_enabled": l.AutoCompactEnabled(),
				"show_tps":             l.ShowTPS(),
				"auto_delegate":        l.AutoDelegateEnabled(),
				"smart_agent":          l.SmartAgentEnabled(),
			})
		}

	default:
		text = "usage: /config, /config auto_compact on|off, /config show_tps on|off, /config auto_delegate on|off, /config smart_agent on|off"
	}

	return l.replyText(sessionID, text)
}

func knownSetting(name string) bool {
	switch name {
	case "auto_compact", "show_tps", "auto_delegate", "smart_agent":
		return true
	}
	return false
}

func (l *Loop) configSummary() string {
	delegate := onOff(l.AutoDelegateEnabled())
	// The target agent is the useful part of this line — "on" alone
	// doesn't say where prompts are going.
	if cfg := l.Config.AutoDelegate; cfg != nil && cfg.Agent != "" {
		delegate += fmt.Sprintf(" (-> %s)", cfg.Agent)
	} else {
		delegate += " (not configured)"
	}
	// The roster is the useful part of the smart_agent line, for the same
	// reason the target agent is for auto_delegate: "on" alone does not
	// say what it turned on, and the answer depends on the profiles this
	// config happens to have.
	smartLine := onOff(l.SmartAgentEnabled())
	if names := agentNamesOf(l.smartAgents(context.Background())); len(names) > 0 {
		smartLine += " (" + strings.Join(names, ", ") + ")"
	} else if l.SmartAgentEnabled() {
		smartLine += " (no profiles to run specialists on)"
	}
	return fmt.Sprintf("auto_compact: %s\nshow_tps: %s\nauto_delegate: %s\nsmart_agent: %s",
		onOff(l.AutoCompactEnabled()), onOff(l.ShowTPS()), delegate, smartLine)
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// parseCompactCommand recognizes "/compact" and "/compact <instructions>".
// ok is false for anything else.
func parseCompactCommand(text string) (instructions string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "/compact" {
		return "", true
	}
	if rest, found := strings.CutPrefix(trimmed, "/compact "); found {
		return strings.TrimSpace(rest), true
	}
	return "", false
}

// handleCompactCommand runs compaction on demand, regardless of
// AutoCompactEnabled or the usage threshold — unlike maybeAutoCompact,
// this always compacts when invoked. instructions, if given, replaces the
// default summarization prompt (e.g. "/compact focus on the auth
// decisions, drop exploratory dead ends").
func (l *Loop) handleCompactCommand(ctx context.Context, sessionID, agentName, displayText, instructions string) error {
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText, "local": true})

	// Counted before, because the answer used to be "Conversation
	// compacted." and nothing else. Compaction changes what is sent and
	// leaves the screen exactly as it was, so a reply with no number in it
	// is indistinguishable from a command that did nothing — which is what
	// it was reported as.
	before := len(l.history(sessionID))

	profile, err := l.Config.ResolveProfile(agentName)
	if err != nil {
		l.Store.Append(sessionID, events.TypeError, map[string]any{"error": err.Error()})
		return nil
	}
	p, ok := l.Providers[profile.Provider]
	if !ok {
		l.Store.Append(sessionID, events.TypeError, map[string]any{
			"error": fmt.Sprintf("no provider client configured for %q", profile.Provider),
		})
		return nil
	}
	// Assembled, not rebuilt by hand. This used to concatenate the
	// session prompt and the agent's own text itself, which meant the
	// one call that most needs to know what it is carrying was the one
	// call that could not say: no manifest, no per-block trust, and a
	// second definition of the prompt to keep in step with buildRun.
	agentCfg := l.agentConfig(ctx, agentName)
	actx := l.activationFor(ctx, sessionID, agentName, agentCfg, l.profileName(agentName), profile, 0,
		l.Tools.NamesFor(ctx, l.toolsForTurn(ctx, agentCfg)))
	env := prompt.Assemble(l.promptAssets(), actx)
	carried := make([]provider.SystemBlock, 0, len(env.System))
	for _, b := range env.System {
		carried = append(carried, provider.SystemBlock{Text: b.Text, Asset: b.AssetID})
	}

	if err := l.compactHistory(ctx, sessionID, p, profile, env.SystemText(), carried, instructions, CompactManual); err != nil {
		l.Store.Append(sessionID, events.TypeError, map[string]any{"error": fmt.Sprintf("compaction failed: %v", err)})
		return nil
	}

	var b strings.Builder
	b.WriteString("Compacted. The model opens the next message with a summary of this conversation rather than the whole of it.\n")
	// Unconditional: compactHistory refuses an empty history and this line
	// is only reached when it succeeded, so there is always a number to
	// give and a guard here would be a condition that cannot be false.
	fmt.Fprintf(&b, "%d message(s) in its context replaced by %d.\n", before, len(l.history(sessionID)))
	// The half people do not expect, said every time rather than
	// discovered — the same sentence "/clear" ends on, for the same
	// reason: neither command is a delete.
	b.WriteString("Everything above stays in this conversation and in its log: scroll up, or reopen it later, " +
		"and it is all still there. Summarizing is itself a model call, so the compaction's own tokens are in /usage.")
	return l.replyText(sessionID, b.String())
}

// handleCostCommand answers "/usage" locally — no model call — with a
// per-model breakdown of cumulative token usage for this session (input,
// the cache read and write where there are any, output, total, number of
// API calls), plus a grand total. Tokens only,
// deliberately no dollar figures: this project has no per-model pricing
// table to keep in sync, and the raw counts are what the context-window
// math elsewhere in this file already uses.
func (l *Loop) handleCostCommand(sessionID, displayText string) error {
	l.mu.Lock()
	totals := make(map[string]modelTotals, len(l.cumulativeUsage[sessionID]))
	for model, t := range l.cumulativeUsage[sessionID] {
		totals[model] = t
	}
	l.mu.Unlock()

	var text string
	if len(totals) == 0 {
		text = "No usage yet."
	} else {
		text = usageReport("Token usage by model:\n", totals)
	}

	return l.replyLocal(sessionID, displayText, text)
}
