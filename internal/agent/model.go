package agent

import (
	"fmt"
	"sort"
	"strings"

	"localcode/internal/config"
	"localcode/internal/events"
)

// Which model answers in this conversation, separately from which agent
// is answering.
//
// The two were one choice here, and a parity review named what that
// costs: an agent carries a prompt, a tool allowlist and a model, so
// changing the model meant changing all three. Somebody who wanted their
// reviewer agent to answer on a bigger model had to write a second agent
// that was the first one with a different profile.
//
// What is chosen is a profile, not a bare model id. A model does not
// travel alone — the provider that serves it, the token ceiling, the
// context window — and storing a model id against an agent pointed
// somewhere else would name a model that provider cannot serve. The
// profile already knows all of it, and the agent keeps its prompt, its
// tools and its permissions.

// ModelChoice is one profile a conversation could answer on.
type ModelChoice struct {
	Profile  string `json:"profile"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	// Current marks the one in force, so a client does not have to
	// compare strings to know which row to tick.
	Current bool `json:"current,omitempty"`
}

// ModelView is what a client shows: the model in force, which of the two
// places said so, and everything this config can reach.
type ModelView struct {
	Agent    string `json:"agent"`
	Profile  string `json:"profile"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	// Source is "agent" when the agent's own profile is answering and
	// "conversation" when this conversation chose one. A real
	// distinction rather than a label: only the first follows the agent
	// when it is switched.
	Source  string        `json:"source"`
	Choices []ModelChoice `json:"choices"`
}

// ModelView reports the model this conversation answers on.
func (l *Loop) ModelView(sessionID string) ModelView {
	agentName := l.sessionAgent(sessionID)
	profileName, profile := l.profileOrZero(agentName)
	source := "agent"
	if l.Store != nil {
		if chosen := l.Store.ProfileFor(sessionID, agentName); chosen != "" {
			if p, ok := l.Config.Profiles[chosen]; ok {
				profileName, profile, source = chosen, p, "conversation"
			}
		}
	}

	view := ModelView{
		Agent:    agentName,
		Profile:  profileName,
		Model:    profile.Model,
		Provider: profile.Provider,
		Source:   source,
		Choices:  l.modelChoices(profileName),
	}
	return view
}

// modelChoices is every profile this config can reach, sorted so the
// list does not reorder itself between two readings of the same config.
func (l *Loop) modelChoices(current string) []ModelChoice {
	if l.Config == nil {
		return nil
	}
	out := make([]ModelChoice, 0, len(l.Config.Profiles))
	for name, p := range l.Config.Profiles {
		out = append(out, ModelChoice{
			Profile: name, Model: p.Model, Provider: p.Provider,
			Current: name == current,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Profile < out[j].Profile })
	return out
}

// SetSessionModel records the profile this conversation answers on for
// the agent it is talking to, or clears it with "" so the agent's own
// applies again.
//
// Clearing is a real third state, the way it is for effort: a
// conversation that never chose and one that chose the agent's own
// profile look alike from outside, and only the first follows the agent
// when its config changes.
func (l *Loop) SetSessionModel(sessionID, profileName string) (ModelView, error) {
	if l.Store == nil {
		return ModelView{}, fmt.Errorf("no session store")
	}
	agentName := l.sessionAgent(sessionID)
	profileName = strings.TrimSpace(profileName)
	if profileName != "" {
		if _, ok := l.Config.Profiles[profileName]; !ok {
			return ModelView{}, fmt.Errorf("no profile called %q. This config can reach: %s",
				profileName, strings.Join(profileNames(l.Config), ", "))
		}
	}
	if _, err := l.Store.SetSessionProfile(sessionID, agentName, profileName); err != nil {
		return ModelView{}, err
	}
	l.AnnounceModel(sessionID)
	return l.ModelView(sessionID), nil
}

// AnnounceModel tells every client watching this conversation. Written to
// the session log the way effort.changed is, so a client that joins later
// replays it rather than having to ask.
func (l *Loop) AnnounceModel(sessionID string) {
	if l.Store == nil {
		return
	}
	v := l.ModelView(sessionID)
	l.Store.Append(sessionID, events.TypeModelChanged, map[string]any{
		"agent": v.Agent, "profile": v.Profile, "model": v.Model,
		"provider": v.Provider, "source": v.Source, "choices": v.Choices,
	})
}

// routeModel answers "/model" and "/model <profile>".
//
// The terminal intercepts "/model" before this and opens a picker, the
// way "/effort-set" does, so what reaches here is the Web UI's — and a
// client that has no picker still needs a way to say it.
func (l *Loop) routeModel(sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/model")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	switch strings.ToLower(arg) {
	case "":
		return true, l.replyText(sessionID, l.modelSummary(sessionID))
	case "default", "unset", "clear":
		if _, err := l.SetSessionModel(sessionID, ""); err != nil {
			return true, l.replyText(sessionID, err.Error())
		}
		return true, l.replyText(sessionID,
			"model: back to what the agent says.\n\n"+l.modelSummary(sessionID))
	}
	if _, err := l.SetSessionModel(sessionID, arg); err != nil {
		return true, l.replyText(sessionID, err.Error()+
			"\n\nusage: /model <profile>, or \"default\" to go back to the agent's.")
	}
	return true, l.replyText(sessionID, l.modelSummary(sessionID))
}

// modelSummary is what "/model" alone says.
func (l *Loop) modelSummary(sessionID string) string {
	v := l.ModelView(sessionID)
	var b strings.Builder
	where := fmt.Sprintf("the %q agent", v.Agent)
	if v.Source == "conversation" {
		where = "this conversation"
	}
	fmt.Fprintf(&b, "model: %s (profile %q on %q), chosen by %s.\n\n",
		firstNonEmpty(v.Model, "none"), v.Profile, v.Provider, where)
	if len(v.Choices) > 0 {
		b.WriteString("This config can reach:\n")
		for _, c := range v.Choices {
			mark := "  "
			if c.Current {
				mark = "* "
			}
			fmt.Fprintf(&b, "%s%s — %s on %s\n", mark, c.Profile, firstNonEmpty(c.Model, "no model"), c.Provider)
		}
		b.WriteString("\n")
	}
	b.WriteString("usage: /model <profile>, or \"default\" to go back to the agent's. " +
		"The agent keeps its prompt, its tools and its permissions either way.")
	return b.String()
}

func profileNames(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
