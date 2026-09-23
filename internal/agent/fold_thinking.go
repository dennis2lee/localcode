package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"localcode/internal/config"
	"localcode/internal/events"
)

// The muse reasoning block.
//
// A muse model reasons at length before every answer, and on the servers
// it runs on the reasoning arrives on a channel of its own: LM Studio
// sends it as reasoning_content, vLLM with --reasoning-parser
// muse_glimmer as reasoning. localcode always kept the two apart. What
// did not keep them apart was the drawing: an unlabelled block of
// reasoning, often starting by restating the question, sat open above
// the reply, and read as the first half of it.
//
// So for a muse model the clients draw the reasoning as a block of its
// own, labelled with its running time, and fold it to one line when the
// answer starts. The daemon decides whether a stream gets that and says
// so on each thinking event, which keeps the family rule in one place
// rather than in three clients.

// foldsThinking reports whether a stream from model has its reasoning
// drawn as a folding block: the daemon-wide switch is on, and the model
// is a muse, by the same substring rule every other muse decision uses.
func (l *Loop) foldsThinking(model string) bool {
	return l.FoldThinkingEnabled() && museModel(model)
}

// MuseProfiles names the configured profiles that run a muse model,
// sorted, for the settings window to say whether its Muse tab reaches
// anything in this config.
func (l *Loop) MuseProfiles() []string {
	names := []string{}
	if l.Config == nil {
		return names
	}
	for name, p := range l.Config.Profiles {
		if museModel(p.Model) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// routeFoldThinking answers "/fold-thinking [on|off]".
func (l *Loop) routeFoldThinking(sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/fold-thinking")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	want, valid := toggleArg(arg, l.FoldThinkingEnabled())
	if !valid {
		return true, l.replyText(sessionID, "usage: /fold-thinking [on|off]")
	}
	l.SetFoldThinkingEnabled(want)

	var b strings.Builder
	fmt.Fprintf(&b, "fold_thinking: %s", onOff(want))
	// The scope first, as /keep-going says it: the switch is daemon-wide
	// and the feature is one family's.
	b.WriteString("\nApplies only to models whose id contains \"muse\": their reasoning is drawn as a labelled block " +
		"that folds to one line when the answer starts. Other models keep the drawing they had.")
	// The model the next turn will run, resolved the way the turn
	// resolves it: a conversation's own choice, then a Smart Agent
	// specialist's lane, then the agent's profile.
	_, next, _ := l.profileFor(l.pinSmart(context.Background()), sessionID, l.sessionAgent(sessionID))
	switch model := next.Model; {
	case model == "":
	case museModel(model):
		fmt.Fprintf(&b, "\nThis conversation is on %s, which the switch applies to.", model)
	default:
		fmt.Fprintf(&b, "\nThis conversation is on %s, which is not a muse model, so nothing changes here.", model)
	}
	if !l.ShowThinking() {
		b.WriteString("\nshow_thinking is off, so no reasoning is drawn either way; /thinking on draws it again.")
	}
	if want && len(l.MuseProfiles()) == 0 {
		b.WriteString("\n(no configured profile currently runs a muse model, so nothing changes until one does)")
	}
	b.WriteString(l.persist(func(path string) error { return config.SetFoldThinkingInFile(path, want) }))

	l.announceSettings()
	return true, l.replyText(sessionID, b.String())
}
