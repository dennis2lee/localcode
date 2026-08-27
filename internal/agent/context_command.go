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

// parseContextCommand recognizes the three forms: the next turn's
// assembly, the same with exclusions, and one historical manifest by the
// id a trace line records.
func parseContextCommand(text string) (id string, all bool, ok bool) {
	t := strings.TrimSpace(text)
	switch t {
	case "/context":
		return "", false, true
	case "/context all", "/context excluded":
		// The long form also lists what was left out and why. Off by
		// default because the excluded list is longer than the included
		// one and is only interesting when something is missing.
		return "", true, true
	}
	rest, found := strings.CutPrefix(t, "/context ")
	if !found {
		return "", false, false
	}
	rest = strings.TrimSpace(rest)
	// A manifest id: hex, and nothing that could be another subcommand.
	if rest == "" || strings.ContainsAny(rest, " \t") {
		return "", false, false
	}
	for _, r := range rest {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return "", false, false
		}
	}
	return strings.ToLower(rest), false, true
}

func (l *Loop) routeContext(ctx context.Context, sessionID, agentName, text string) (bool, error) {
	id, all, ok := parseContextCommand(text)
	if !ok {
		return false, nil
	}
	if id != "" {
		return true, l.showStoredManifest(sessionID, text, id)
	}
	return true, l.handleContextCommand(ctx, sessionID, agentName, text, all)
}

// showStoredManifest answers "/context <id>": the manifest of a call
// that already happened, read back from the store by the id its trace
// line records.
//
// This is the half that makes a trace id worth having. Without it the
// only thing "/context" could describe was a hypothetical next turn
// built from current state, which cannot answer a question about the
// request that actually went out an hour ago.
func (l *Loop) showStoredManifest(sessionID, displayText, id string) error {
	m, ok := l.Manifests.Get(id)
	if !ok {
		return l.replyLocal(sessionID, displayText, "No stored assembly with id "+id+
			".\nManifest ids come from the turn log's prompt_manifest field. Manifests are bounded by the\n"+
			"same two settings the trace is, so a trace line can outlive its manifest when the two\n"+
			"directories fill at different rates.")
	}
	return l.replyLocal(sessionID, displayText, renderManifest(m, true,
		"Prompt assembly "+m.ID+", recorded "+m.At.Format("2006-01-02 15:04:05")))
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

	// The same tool list the next turn would advertise, resolved the
	// same way, so the report describes that request rather than an
	// approximation of it.
	specs := l.Tools.SpecsFor(ctx, l.toolsForTurn(ctx, agentCfg))
	advertised := make([]string, len(specs))
	for i, sp := range specs {
		advertised[i] = sp.Name
	}
	actx := l.activationFor(ctx, sessionID, agentName, agentCfg, profileName, profile, 0, advertised)
	env := prompt.Assemble(l.promptAssets(), actx)
	// The same tool entries the real request would carry, so the preview
	// and the run describe the same assembly rather than two that differ
	// by exactly the part the preview forgot.
	// The same runtime entries the real request would carry: the tool
	// definitions, and the sources already in the history this session
	// would send. A preview that named only the tools described a
	// request missing every tool result the conversation is carrying.
	history := sendableHistory(l.history(sessionID))
	env.Manifest = env.Manifest.WithRuntimeEntries(
		append(toolEntries(specs), historyEntries(history)...)...)
	// The same adapter lowering the real request records, through the
	// same function. Computing it here separately is what made the
	// preview report an id no request would carry.
	env.Manifest = l.lowerForProvider(env.Manifest, profile, len(env.System))
	m := env.Manifest

	var b strings.Builder
	b.WriteString(renderManifest(m, all, "Prompt assembly "+m.ID+" for the next turn"))

	// The rest of what a request carries, beyond the prompt assets: the
	// conversation itself, the tool definitions, and the room reserved
	// for the answer. These are what actually fill a window, and a
	// diagnostic that stopped at the system prompt would be describing
	// the small half. Counted from the specs actually advertised rather
	// than from the allowlist, because a nil allowlist means
	// "everything" and reporting that as zero tools would describe the
	// restriction instead of the request.
	// Built-in and MCP counted apart, because they are different
	// questions: how much the product costs you, and how much another
	// process is adding to every request.
	builtinTokens, builtinCount := 0, 0
	mcpTokens, mcpCount := 0, 0
	mcpServers := map[string]int{}
	for _, e := range toolEntries(specs) {
		if e.Provenance == prompt.FromMCPServer {
			mcpTokens += e.Tokens
			mcpCount++
			if server, ok := mcpServerOf(strings.TrimPrefix(e.ID, "tool.mcp.")); ok {
				mcpServers[server] += e.Tokens
			}
			continue
		}
		builtinTokens += e.Tokens
		builtinCount++
	}
	toolTokens := builtinTokens + mcpTokens
	convTokens := 0
	for _, msg := range history {
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
	fmt.Fprintf(&b, "    %-22s ~%d tokens in %d\n", "built-in", builtinTokens, builtinCount)
	if mcpCount > 0 {
		fmt.Fprintf(&b, "    %-22s ~%d tokens in %d\n", "from MCP servers", mcpTokens, mcpCount)
		servers := make([]string, 0, len(mcpServers))
		for name := range mcpServers {
			servers = append(servers, name)
		}
		sort.Strings(servers)
		for _, name := range servers {
			fmt.Fprintf(&b, "      %-20s ~%d tokens\n", name, mcpServers[name])
		}
	}
	fmt.Fprintf(&b, "  %-24s ~%d tokens\n", "conversation so far", convTokens)
	fmt.Fprintf(&b, "  %-24s %d tokens\n", "reserved for the answer", maxTokens)
	fmt.Fprintf(&b, "  %-24s %d tokens\n", "context window", window)

	// The estimate is a floor on Korean and Japanese, and saying so is
	// the difference between a number somebody can use and one they
	// will be surprised by.
	b.WriteString("\nToken figures are estimates from character counts: about right for English,\nand a floor for Korean and Japanese, which run several times denser.\n")

	return l.replyLocal(sessionID, displayText, strings.TrimRight(b.String(), "\n"))
}

// renderManifest prints one assembly: what is in it and why, what is
// not, and the facts about the call it was built for. Shared by the
// live report and the stored-manifest lookup so the two cannot drift
// into describing the same thing differently.
//
// Identities, sizes and reasons only. The bodies include the workspace's
// own instructions and whatever a hook injected, and both a transcript
// and the manifest store are places those must not be copied into.
func renderManifest(m prompt.Manifest, all bool, heading string) string {
	var b strings.Builder
	b.WriteString(heading + "\n")
	fmt.Fprintf(&b, "  agent %s (%s) · profile %s · model %s · family %s\n",
		m.Agent, m.Role, m.Profile, m.Model, m.Family)
	fmt.Fprintf(&b, "  smart agent %s · lifecycle %s · fallback position %d\n",
		onOff(m.SmartAgent), lifecycleOr(m.Lifecycle), m.FallbackIndex)

	byKind := map[prompt.Kind]int{}
	for _, e := range m.Selected {
		byKind[e.Kind] += e.Tokens
	}
	fmt.Fprintf(&b, "\nIncluded, about %d tokens in %d assets:\n", m.TotalTokens, len(m.Selected))
	// The conversation's own sources are listed in full only when they
	// are few enough to read. A long session carries one entry per tool
	// result it is still sending, which is the point of describing them
	// at all, and printing four hundred rows would bury the eleven that
	// answer "what is this request made of". The full list is behind
	// "/context all", where somebody has asked for it.
	shown, folded, foldedTokens := 0, 0, 0
	for _, e := range m.Selected {
		if !all && e.Placement == prompt.PlaceMessage && conversationEntries(m) > maxListedConversationEntries {
			folded++
			foldedTokens += e.Tokens
			continue
		}
		shown++
		fmt.Fprintf(&b, "  %-24s %-20s %-9s ~%d tokens · %s\n",
			e.ID, e.Kind, e.Trust, e.Tokens, e.Reason)
	}
	if folded > 0 {
		fmt.Fprintf(&b, "  %-24s %-20s %-9s ~%d tokens · \"/context all\" lists them\n",
			fmt.Sprintf("(%d conversation sources)", folded), "message", "mixed", foldedTokens)
	}
	if len(byKind) > 1 {
		kinds := make([]string, 0, len(byKind))
		for k := range byKind {
			kinds = append(kinds, string(k))
		}
		sort.Strings(kinds)
		b.WriteString("\nBy category:\n")
		for _, k := range kinds {
			fmt.Fprintf(&b, "  %-24s ~%d tokens\n", k, byKind[prompt.Kind(k)])
		}
	}
	if un := m.UntrustedIDs(); len(un) > 0 {
		fmt.Fprintf(&b, "\nCarrying content that may not be followed as instruction: %s\n", strings.Join(un, ", "))
	}
	if len(m.Lowering) > 0 {
		b.WriteString("\nProvider lowering:\n")
		for _, lo := range m.Lowering {
			fmt.Fprintf(&b, "  %s\n", lo)
		}
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
	return b.String()
}

// maxListedConversationEntries is how many message-placed sources are
// printed one per line before the short form folds them into a count. A
// turn or two of tool use stays readable; a long session does not turn
// the report into a transcript index.
const maxListedConversationEntries = 12

// conversationEntries counts the message-placed sources on a manifest.
func conversationEntries(m prompt.Manifest) int {
	n := 0
	for _, e := range m.Selected {
		if e.Placement == prompt.PlaceMessage {
			n++
		}
	}
	return n
}

func lifecycleOr(l prompt.Lifecycle) string {
	if l == "" {
		return string(prompt.LifecycleTurn)
	}
	return string(l)
}
