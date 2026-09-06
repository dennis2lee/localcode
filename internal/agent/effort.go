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
// ones take a budget per level. Bedrock takes the same thinking block,
// through its own field.
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
		if e := l.Store.EffortFor(sessionID, profile.Model); e != "" {
			return provider.Effort(e)
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
		if _, err := l.SetSessionEffort(sessionID, ""); err != nil {
			return true, l.replyText(sessionID, err.Error())
		}
		return true, l.replyText(sessionID,
			"effort: back to what the profile says.\n\n"+l.effortSummary(sessionID, profileName, profile))
	}
	// Through the same door the controls use, so the same levels are
	// refused whichever way one is asked for. Typing a word this model
	// does not tell apart used to store it, while the picker refused it —
	// and the stored one would show in both readouts and never be true of
	// a request.
	if _, err := l.SetSessionEffort(sessionID, arg); err != nil {
		return true, l.replyText(sessionID, err.Error()+
			"\n\nusage: /effort [off|low|medium|high|xhigh], or \"default\" to go back to the profile's.")
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
		if e := l.Store.EffortFor(sessionID, profile.Model); e != "" {
			level, source = provider.Effort(e), "this conversation"
		}
	}

	var b strings.Builder
	if level == provider.EffortUnset {
		fmt.Fprintf(&b, "effort: unset. Nothing is asked for, and %s answers as it always has.\n", modelName(profile))
	} else {
		fmt.Fprintf(&b, "effort: %s, set by %s. Model: %s.\n", level, source, modelName(profile))
	}
	b.WriteString(effortReach(l.providerTypeOf(profile), profile.Model, level))
	b.WriteString("\n\nusage: /effort [off|low|medium|high|xhigh], or \"default\" to go back to the profile's.")
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
		if museModel(model) {
			return "Muse reads its reasoning strength from the system prompt, not from a request field, so " +
				"this is sent as the line \"Reasoning strength: " + string(level) + "\" there. Its publisher asks " +
				"for high or xhigh on coding work. reasoning_effort is sent as well, capped at high, for a " +
				"server that reads it."
		}
		return "Sent to the server as \"reasoning_effort\". One that supports reasoning takes the level; " +
			"one that does not ignores the field, and nothing about the request changes."
	}
}

// effortLevelsFor is the levels worth offering on one model: the ones
// that reach the wire as different requests.
//
// Not the whole vocabulary every time. Every model in the table below
// accepts all five words — localcode maps whatever it is given — but on
// most of them two or three of those words arrive as the same request,
// and a control that offers a dial where the wire has a switch is a
// control that lies about what it did. Anthropic's newest families
// decide the amount of thinking themselves, so the honest choice there
// is on or off; the older ones take a budget per level and xhigh is the
// same budget as high; the OpenAI-compatible field's vocabulary stops at
// high, so xhigh is only a step on muse, which reads the word from its
// system prompt instead.
//
// Unset is not in the list. It is not a level but the absence of one,
// and every client offers it separately as "default".
func effortLevelsFor(providerType config.ProviderType, model string, maxTokens int) []provider.Effort {
	switch providerType {
	case config.ProviderAnthropic, config.ProviderBedrock:
		if provider.AnthropicAdaptiveThinking(model) {
			return []provider.Effort{provider.EffortOff, provider.EffortHigh}
		}
		// A budget per level, and every budget clamped to the room the
		// reply cap leaves — so on an ordinary max_tokens the top levels
		// arrive as the same number. Built from what the wire would
		// carry rather than from a literal list: a level is kept only
		// when its budget differs from the last one kept.
		levels := []provider.Effort{provider.EffortOff}
		last := 0
		for _, e := range []provider.Effort{provider.EffortLow, provider.EffortMedium, provider.EffortHigh} {
			budget, ok := provider.ThinkingBudgetFor(model, e, maxTokens)
			if !ok || budget == last {
				continue
			}
			last = budget
			levels = append(levels, e)
		}
		return levels
	default:
		if museModel(model) {
			return provider.Levels()
		}
		return []provider.Effort{provider.EffortOff, provider.EffortLow, provider.EffortMedium, provider.EffortHigh}
	}
}

// EffortView is everything a client needs to draw the control: the level
// in force, which of the two places said so, the model it belongs to,
// the levels that model tells apart, and what the level reaches there.
//
// The model is in it because the answer is per model, so a client that
// shows a level without saying which model it is for is showing half a
// fact — and because the list of levels changes with the model, a client
// that caches the list across a model switch offers steps that are not
// there.
type EffortView struct {
	Model  string   `json:"model"`
	Agent  string   `json:"agent"`
	Level  string   `json:"level"`
	Source string   `json:"source"`
	Levels []string `json:"levels"`
	Note   string   `json:"note"`
}

// EffortView answers "what is this conversation's reasoning level, on the
// model it is on".
func (l *Loop) EffortView(sessionID string) EffortView {
	agentName := "general-purpose"
	if l.Store != nil {
		if sess, err := l.Store.Get(sessionID); err == nil && sess.Agent != "" {
			agentName = sess.Agent
		}
	}
	_, profile := l.profileOrZero(agentName)

	level, source := provider.Effort(profile.Effort), "profile"
	if level == provider.EffortUnset {
		source = "unset"
	}
	if l.Store != nil {
		if e := l.Store.EffortFor(sessionID, profile.Model); e != "" {
			level, source = provider.Effort(e), "session"
		}
	}
	maxTokens := profile.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}
	distinct := effortLevelsFor(l.providerTypeOf(profile), profile.Model, maxTokens)
	levels := make([]string, 0, len(distinct))
	for _, e := range distinct {
		levels = append(levels, string(e))
	}
	return EffortView{
		Model:  profile.Model,
		Agent:  agentName,
		Level:  string(level),
		Source: source,
		Levels: levels,
		Note:   effortReach(l.providerTypeOf(profile), profile.Model, level),
	}
}

// AnnounceEffort writes this conversation's level to its own log, so
// every client watching redraws.
//
// On the Loop rather than in the daemon, because two things change the
// level — the HTTP route and the "/effort" command this file also owns —
// and a route that announces beside a command that does not is a
// readout that is right half the time. The daemon's handler calls this.
func (l *Loop) AnnounceEffort(sessionID string) {
	if l.Store == nil {
		return
	}
	view := l.EffortView(sessionID)
	l.Store.Append(sessionID, events.TypeEffortChanged, map[string]any{
		"model": view.Model, "agent": view.Agent, "level": view.Level,
		"source": view.Source, "levels": view.Levels, "note": view.Note,
	})
}

// SetSessionEffort records a level for the model this conversation is on,
// or clears it with "".
//
// A level the model does not tell apart is refused rather than stored.
// Storing it would be honest about the vocabulary and dishonest about the
// effect: the control would show medium on a family whose wire has one
// switch, and nothing would ever make it false.
func (l *Loop) SetSessionEffort(sessionID, level string) (EffortView, error) {
	view := l.EffortView(sessionID)
	if level != "" {
		if !provider.ValidEffort(level) {
			return view, fmt.Errorf("%q is not a level; %s has %s", level,
				modelName(config.Profile{Model: view.Model}), strings.Join(view.Levels, ", "))
		}
		allowed := false
		for _, ok := range view.Levels {
			if ok == level {
				allowed = true
			}
		}
		if !allowed {
			return view, fmt.Errorf("%s does not tell %q apart from the others; it has %s",
				modelName(config.Profile{Model: view.Model}), level, strings.Join(view.Levels, ", "))
		}
	}
	if _, err := l.Store.SetEffort(sessionID, view.Model, level); err != nil {
		return view, err
	}
	l.AnnounceEffort(sessionID)
	return l.EffortView(sessionID), nil
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
