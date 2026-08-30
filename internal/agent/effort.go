package agent

import (
	"fmt"
	"strings"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
)

// How hard the model is asked to think, and where that answer comes from.
//
// The setting is one word — off, low, medium, high — and underneath it
// the wires do not agree. An OpenAI-compatible server takes
// "reasoning_effort", which is what a local muse or gemma understands
// when it understands anything at all. Anthropic's API takes extended
// thinking, and there the newest Claude families decide the amount
// themselves, so every level reaches the same switch and only the older
// ones take a budget per level. Bedrock is not wired to it yet.
//
// That unevenness is reported rather than smoothed over. A setting that
// claims three positions on a wire with two lies twice: once when it is
// set, and again when somebody tries to tell "high" from "medium" by
// watching what it costs. So "/effort" says what the level reaches on
// this model, in a sentence, every time it is asked.
//
// Two places can answer, and the conversation wins over the profile. The
// question belongs to the work rather than to the model — the same model
// answering "which file is this in" and "why does this deadlock" wants
// different amounts of reasoning — and without a per-session answer the
// only way to have both would be two profiles pointing at one model.

// effortFor resolves the level for a turn: this conversation's own answer
// if it has given one, otherwise the profile's, otherwise nothing.
func (l *Loop) effortFor(sessionID string, profile config.Profile) provider.Effort {
	if l.Store != nil {
		if sess, err := l.Store.Get(sessionID); err == nil && sess.Effort != "" {
			return provider.Effort(sess.Effort)
		}
	}
	return provider.Effort(profile.Effort)
}

// routeEffort answers "/effort [off|low|medium|high|default]".
func (l *Loop) routeEffort(sessionID, agentName, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/effort")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	profileName, profile := l.profileOrZero(agentName)
	arg = strings.ToLower(strings.TrimSpace(arg))

	switch arg {
	case "":
		return true, l.replyText(sessionID, l.effortSummary(sessionID, profileName, profile))
	case "default", "unset", "clear":
		if _, err := l.Store.SetEffort(sessionID, ""); err != nil {
			return true, l.replyText(sessionID, err.Error())
		}
		return true, l.replyText(sessionID,
			"effort: back to what the profile says.\n\n"+l.effortSummary(sessionID, profileName, profile))
	}
	if !provider.ValidEffort(arg) {
		return true, l.replyText(sessionID,
			"usage: /effort [off|low|medium|high], or \"default\" to go back to the profile's.")
	}
	if _, err := l.Store.SetEffort(sessionID, arg); err != nil {
		return true, l.replyText(sessionID, err.Error())
	}
	return true, l.replyText(sessionID, fmt.Sprintf(
		"effort: %s in this conversation, whatever the %q profile says.\n%s",
		arg, profileName, effortReach(l.providerTypeOf(profile), profile.Model, provider.Effort(arg))))
}

// effortSummary is what "/effort" alone says: the level in force, which
// of the two places said so, and what it actually reaches on this model.
func (l *Loop) effortSummary(sessionID, profileName string, profile config.Profile) string {
	level := provider.Effort(profile.Effort)
	source := fmt.Sprintf("the %q profile", profileName)
	if l.Store != nil {
		if sess, err := l.Store.Get(sessionID); err == nil && sess.Effort != "" {
			level, source = provider.Effort(sess.Effort), "this conversation"
		}
	}

	var b strings.Builder
	if level == provider.EffortUnset {
		fmt.Fprintf(&b, "effort: unset. Nothing is asked for, and %s answers as it always has.\n", modelName(profile))
	} else {
		fmt.Fprintf(&b, "effort: %s, set by %s. Model: %s.\n", level, source, modelName(profile))
	}
	b.WriteString(effortReach(l.providerTypeOf(profile), profile.Model, level))
	b.WriteString("\n\nusage: /effort [off|low|medium|high], or \"default\" to go back to the profile's.")
	return b.String()
}

// effortReach says what a level does on this particular model, because
// the answer differs by provider and by family, and a setting that looks
// as though it took effect and did not is the failure worth two lines.
func effortReach(providerType config.ProviderType, model string, level provider.Effort) string {
	if level == provider.EffortUnset || level == provider.EffortOff {
		return "Nothing is sent either way, so the model does whatever it does by default."
	}
	switch providerType {
	case config.ProviderAnthropic:
		if provider.AnthropicAdaptiveThinking(model) {
			return "On " + model + " this asks for extended thinking. That family decides the amount " +
				"itself, so low, medium and high all reach the same switch: here the setting is on or " +
				"off rather than a dial."
		}
		return "On " + model + " this asks for extended thinking, with a token budget chosen for the level."
	case config.ProviderBedrock:
		if provider.AnthropicAdaptiveThinking(model) {
			return "On " + model + " this asks Bedrock for extended thinking. That family decides the " +
				"amount itself, so low, medium and high all reach the same switch: here the setting is " +
				"on or off rather than a dial."
		}
		return "On " + model + " this asks Bedrock for extended thinking, with a token budget for the level."
	default:
		return "Sent to the server as \"reasoning_effort\". One that supports reasoning takes the level; " +
			"one that does not ignores the field, and nothing about the request changes."
	}
}

// profileOrZero resolves an agent's profile for the command path, where a
// configuration that cannot answer is something to report rather than an
// error to return: every other path already fails loudly on it.
func (l *Loop) profileOrZero(agentName string) (string, config.Profile) {
	p, err := l.Config.ResolveProfile(agentName)
	if err != nil {
		return l.profileName(agentName), config.Profile{}
	}
	return l.profileName(agentName), p
}

func (l *Loop) providerTypeOf(profile config.Profile) config.ProviderType {
	if p, ok := l.Config.Providers[profile.Provider]; ok {
		return p.Type
	}
	return ""
}

func modelName(profile config.Profile) string {
	if profile.Model == "" {
		return "this model"
	}
	return profile.Model
}
