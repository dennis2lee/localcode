package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"localcode/internal/prompt"
)

// "/context" answers the question the prompt registry exists to make
// answerable: what is actually in the request this session is sending,
// and what is not.
//
// It is a local command, like "/usage" — no model call, because the whole
// point is to inspect what a model call *would* contain, and spending one
// to find out would be absurd. And it reports the assembly for the next
// turn rather than the last one, so the answer is about the thing you are
// deciding whether to send.
//
// What it prints is deliberately identities, sizes and reasons rather
// than the prompt text. Printing the bodies would put the project's own
// instructions into the transcript, which is a durable log; the sizes and
// the inclusion reasons are what the question is actually about, and
// anybody who wants the text has the files it came from.

func parseContextCommand(text string) (bool, bool) {
	t := strings.TrimSpace(text)
	switch t {
	case "/context":
		return false, true
	case "/context all", "/context excluded":
		// The long form also lists what was left out and why. Off by
		// default because the excluded list is longer than the included
		// one and is only interesting when something is missing.
		return true, true
	}
	return false, false
}

func (l *Loop) routeContext(ctx context.Context, sessionID, agentName, text string) (bool, error) {
	all, ok := parseContextCommand(text)
	if !ok {
		return false, nil
	}
	return true, l.handleContextCommand(ctx, sessionID, agentName, text, all)
}

// handleContextCommand builds the assembly this session's next turn would
// send and reports it.
func (l *Loop) handleContextCommand(ctx context.Context, sessionID, agentName, displayText string, all bool) error {
	// Pinned exactly as a turn pins it, so the report describes the
	// request that would actually be sent rather than one built under a
	// setting that could differ by the time it is.
	ctx = l.pinSmart(ctx)
	profileName, profile, err := l.profileFor(ctx, agentName)
	if err != nil {
		return l.replyLocal(sessionID, displayText, "Cannot describe the context: "+err.Error())
	}
	agentCfg := l.agentConfig(ctx, agentName)

	actx := l.activationFor(ctx, sessionID, agentName, agentCfg, profileName, profile)
	env := prompt.Assemble(l.promptAssets(), actx)
	if len(env.System) > 1 {
		env.Manifest.Lowering = append(env.Manifest.Lowering,
			fmt.Sprintf("%d system blocks folded into one provider system string", len(env.System)))
	}
	m := env.Manifest

	var b strings.Builder
	fmt.Fprintf(&b, "Prompt assembly %s for the next turn\n", m.ID)
	fmt.Fprintf(&b, "  agent %s (%s) · profile %s · model %s · family %s\n",
		m.Agent, m.Role, m.Profile, m.Model, m.Family)
	fmt.Fprintf(&b, "  smart agent %s · workspace %s\n",
		onOff(m.SmartAgent), actx.WorkspaceClass)

	// Per category, because "the prompt is 4,000 tokens" is not
	// actionable and "the project's rules are 3,200 of it" is.
	byKind := map[prompt.Kind]int{}
	for _, e := range m.Selected {
		byKind[e.Kind] += e.Tokens
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	fmt.Fprintf(&b, "\nIncluded, about %d tokens in %d assets:\n", m.TotalTokens, len(m.Selected))
	for _, e := range m.Selected {
		fmt.Fprintf(&b, "  %-24s %-20s %-9s ~%d tokens · %s\n",
			e.ID, e.Kind, e.Trust, e.Tokens, e.Reason)
	}
	if len(kinds) > 1 {
		b.WriteString("\nBy category:\n")
		for _, k := range kinds {
			fmt.Fprintf(&b, "  %-24s ~%d tokens\n", k, byKind[prompt.Kind(k)])
		}
	}

	// The rest of what a request carries, beyond the prompt assets: the
	// conversation itself, the tool definitions, and the room reserved
	// for the answer. These are what actually fill a window, and a
	// diagnostic that stopped at the system prompt would be describing
	// the small half.
	// Counted from the specs actually advertised, not from the
	// allowlist: a nil allowlist means "everything", and reporting that
	// as zero tools described the restriction, not the request.
	specs := l.Tools.SpecsFor(ctx, l.toolsForTurn(ctx, agentCfg))
	toolTokens := 0
	for _, spec := range specs {
		toolTokens += (len(spec.Description)+len(spec.InputSchema))/4 + 1
	}
	convTokens := 0
	for _, msg := range sendableHistory(l.history(sessionID)) {
		for _, blk := range msg.Content {
			convTokens += (len([]rune(blk.Text)) + len([]rune(blk.ToolResultContent)) + len(blk.ToolInput)) / 4
		}
	}
	maxTokens := profile.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}
	window := l.contextWindow(ctx, profile)
	fmt.Fprintf(&b, "\nBeyond the prompt assets:\n")
	fmt.Fprintf(&b, "  %-24s ~%d tokens in %d tools\n", "tool definitions", toolTokens, len(specs))
	fmt.Fprintf(&b, "  %-24s ~%d tokens\n", "conversation so far", convTokens)
	fmt.Fprintf(&b, "  %-24s %d tokens\n", "reserved for the answer", maxTokens)
	fmt.Fprintf(&b, "  %-24s %d tokens\n", "context window", window)

	// The estimate is a floor on Korean and Japanese, and saying so is
	// the difference between a number somebody can use and one they
	// will be surprised by.
	b.WriteString("\nToken figures are estimates from character counts: about right for English,\nand a floor for Korean and Japanese, which run several times denser.\n")

	if len(m.Lowering) > 0 {
		b.WriteString("\nProvider lowering:\n")
		for _, lo := range m.Lowering {
			fmt.Fprintf(&b, "  %s\n", lo)
		}
	}

	if un := m.UntrustedIDs(); len(un) > 0 {
		fmt.Fprintf(&b, "\nCarrying external content (data, never instructions): %s\n", strings.Join(un, ", "))
	}
	if len(m.Warnings) > 0 {
		b.WriteString("\nWarnings:\n")
		for _, w := range m.Warnings {
			fmt.Fprintf(&b, "  %s\n", w)
		}
	}

	if all {
		fmt.Fprintf(&b, "\nNot included (%d):\n", len(m.Excluded))
		for _, x := range m.Excluded {
			fmt.Fprintf(&b, "  %-24s %s\n", x.ID, x.Reason)
		}
	} else if len(m.Excluded) > 0 {
		fmt.Fprintf(&b, "\n%d assets were not included. \"/context all\" says which, and why.\n", len(m.Excluded))
	}

	return l.replyLocal(sessionID, displayText, strings.TrimRight(b.String(), "\n"))
}
