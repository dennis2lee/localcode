package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"localcode/internal/commands"
	"localcode/internal/events"
	"localcode/internal/hooks"
	"localcode/internal/memory"
	"localcode/internal/prompt"
	"localcode/internal/provider"
	"localcode/internal/skills"
)

// initPrompt is what "/init" sends to the model — the same idea as
// opencode's "/init": scan the repo and write an AGENTS.md rules file so
// future turns (in this project or picked up by opencode/Claude Code too,
// since both read AGENTS.md/CLAUDE.md) start with real project context.
const initPrompt = `Scan this repository (file listing, README, package/build manifests, existing build/lint/test tooling) and create or update an AGENTS.md file at the project root with concise, project-specific guidance for a coding agent: build/lint/test commands, an architecture overview, and code conventions. If AGENTS.md already exists, improve it in place rather than replacing it wholesale. Use your file tools (Glob/Grep/Read to explore, Write or Edit to save AGENTS.md).`

// SendMessage appends a user turn to sessionID's history and drives the
// agent loop (model call -> optional tool calls -> model call -> ...) until
// the model produces a final answer. agentName selects which model profile
// to use, per the config's agents map.
func (l *Loop) SendMessage(ctx context.Context, sessionID, agentName, text string) error {
	// The admission boundary for a top-level message, and therefore where
	// the Smart Agent setting is pinned. Not in sendWithModelText, which
	// is reached only after command routing and auto-delegation have had
	// their turn: auto-delegation hands the work to SpawnSync, which can
	// sit on the task semaphore, and a switch flipped while it waited used
	// to reach the child. Pinning here covers every route out of this
	// function. A delegated turn arrives with its parent's pin and keeps
	// it. See config.WithSmartAgent.
	ctx = l.pinSmart(ctx)

	if len(l.Config.Hooks) > 0 {
		blocked, reason, _ := hooks.Run(ctx, l.Config.Hooks, hooks.EventUserPromptSubmit, l.SessionDir(sessionID), map[string]any{
			"session_id": sessionID,
			"prompt":     text,
		})
		if blocked {
			l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
			l.Store.Append(sessionID, events.TypeError, map[string]any{
				"error": fmt.Sprintf("blocked by user_prompt_submit hook: %s", reason),
			})
			return nil
		}
	}

	// A delegated task is work, not a command.
	//
	// SendMessage is the one door, and everything below it assumes what
	// came through was typed by a person. A sub-agent's task arrives at
	// the same door, and used to be walked through the whole command table
	// first: a task whose first line read "/permission-skip-all on" was
	// executed as a toggle in the child session, the child did no work,
	// and the parent was handed the command's own confirmation text back
	// as though it were an answer.
	//
	// The route in is short, and it is the one the trust boundary in the
	// system prompt exists to name. A Task prompt is written by a model,
	// and the model writes it after reading files, command output and
	// whatever an MCP server returned. Data reaching the model turned into
	// a privileged action with nobody asked.
	//
	// Scoped to the delegated text itself rather than to child sessions,
	// because a person can open a sub-agent's conversation and type in it,
	// and their commands must still work. Same comparison the opening
	// message uses to tag its own source. See withDelegatedTask.
	delegated := false
	if d, ok := delegatedTaskFrom(ctx); ok && d.task == text {
		delegated = true
	}

	// Tried in order; the first match wins. This order is the precedence
	// contract: built-in commands, then custom commands, then skills, then
	// auto-delegation, then an ordinary model turn — nothing user-facing
	// can be shadowed by a later entry. See commandRoutes.
	if !delegated {
		for _, route := range l.commandRoutes(ctx, sessionID, agentName, text) {
			if handled, err := route(); handled {
				return err
			}
		}
	}

	// Everything above is a command of some kind. What's left is an
	// ordinary prompt, the only thing worth handing to a cheaper agent.
	//
	// Not a delegated task, though: something has already chosen which
	// agent this work goes to, and routing it again on a glob match
	// overrules that choice with a rule written for what a person types.
	if target, ok := l.delegateTarget(sessionID, agentName, text); ok && !delegated {
		return l.delegatePrompt(ctx, sessionID, target, text)
	}

	// "#<name>" names another conversation. Resolving it adds a line of
	// localcode's own text to the model's copy of the message and nothing
	// to the transcript, which still shows exactly what was typed.
	//
	// Not for a delegated task: that text was composed by a model, and a
	// model reaching into other conversations by writing a token into a
	// sub-agent's prompt is the transitivity this design closes.
	modelText, origin := text, messageOrigin{}
	if !delegated {
		if expanded, spans, notices := l.expandSessionRefs(sessionID, text); expanded != text {
			modelText, origin.spans = expanded, spans
			for _, n := range notices {
				l.Store.Append(sessionID, events.TypeError, map[string]any{
					"error": n, "recovered": true,
				})
			}
		}
	}

	err := l.sendWithModelText(ctx, sessionID, agentName, text, modelText, "", "", origin)

	// A debate the model asked for during that turn starts now, not
	// during it: it drives turns in this same session, and the tool call
	// that booked it was inside one of them. Taken unconditionally, error
	// or not, because a booking left behind would fire on some later and
	// unrelated message. See DebateTool.Execute.
	if d, booked := l.takePendingDebate(sessionID); booked && err == nil {
		return l.runDebate(ctx, d)
	}

	// And a command the model asked for during that turn, for the same
	// reason and in the same place: it is a turn in this session, and the
	// tool call that booked it was inside one. Marked as a command run,
	// so the one it produces cannot book a third.
	if line, booked := l.takePendingCommand(sessionID); booked && err == nil {
		return l.SendMessage(withCommandRun(ctx), sessionID, agentName, line)
	}
	return err
}

// commandRoutes returns this turn's built-in/custom-command/skill
// matchers, tried in SendMessage in this exact order — the precedence
// contract described there. Each route reports (handled, err): handled
// true stops the walk (regardless of err, which SendMessage returns
// as-is); false lets the next route look at the same text. Building the
// slice fresh per call (rather than a package-level table) is what lets
// each route close over ctx/sessionID/agentName/text without a shared
// signature across routes that need different arguments.
func (l *Loop) commandRoutes(ctx context.Context, sessionID, agentName, text string) []func() (bool, error) {
	return []func() (bool, error){
		func() (bool, error) { return l.routeSkillCommand(ctx, sessionID, agentName, text) },
		func() (bool, error) { return l.routeInit(ctx, sessionID, agentName, text) },
		func() (bool, error) { return l.routeMemory(sessionID, text) },
		func() (bool, error) { return l.routeConfig(sessionID, text) },
		func() (bool, error) { return l.routeSmartAgent(sessionID, text) },
		func() (bool, error) { return l.routeOrchestrate(sessionID, text) },
		func() (bool, error) { return l.routeAutoDelegate(sessionID, text) },
		func() (bool, error) { return l.routeSkipPermissions(sessionID, text) },
		func() (bool, error) { return l.routeSkipTools(sessionID, text) },
		func() (bool, error) { return l.routeReadOutside(sessionID, text) },
		func() (bool, error) { return l.routeWriteOutside(sessionID, text) },
		func() (bool, error) { return l.routeDebate(ctx, sessionID, agentName, text) },
		func() (bool, error) { return l.routeEffort(sessionID, agentName, text) },
		func() (bool, error) { return l.routeSchedule(sessionID, agentName, text) },
		func() (bool, error) { return l.routeShowScheduled(sessionID, text) },
		func() (bool, error) { return l.routeKeepGoing(sessionID, text) },
		func() (bool, error) { return l.routeAutoCompact(sessionID, text) },
		func() (bool, error) { return l.routeResetMCP(sessionID, text) },
		func() (bool, error) { return l.routeResetSkills(sessionID, text) },
		func() (bool, error) { return l.routeCompact(ctx, sessionID, agentName, text) },
		// Beside compaction, which is the command they are variations of:
		// all three decide what the model is sent without touching what
		// happened. Ahead of the custom-command and skill routes at the
		// bottom, so a built-in name wins — see the note on shadowing in
		// docs/USAGE.md.
		func() (bool, error) { return l.routeClear(sessionID, text) },
		func() (bool, error) { return l.routeRewind(ctx, sessionID, text) },
		func() (bool, error) { return l.routeModelInvocable(sessionID, text) },
		func() (bool, error) { return l.routeUsage(sessionID, text) },
		func() (bool, error) { return l.routeContext(ctx, sessionID, agentName, text) },
		func() (bool, error) { return l.routeCustomCommand(ctx, sessionID, agentName, text) },
		func() (bool, error) { return l.routeSkillName(ctx, sessionID, agentName, text) },
	}
}

// routeSkillCommand recognizes "/skill" and "/skill <name> [args]" — the
// older spelling of running a skill, kept working so it doesn't break
// under anyone's fingers; "/<name>" (routeSkillName) is the documented one.
func (l *Loop) routeSkillCommand(ctx context.Context, sessionID, agentName, text string) (bool, error) {
	arg, ok := parseSkillCommand(text)
	if !ok {
		return false, nil
	}
	if arg == "" {
		return true, l.listSkills(sessionID, text)
	}
	name, args := arg, ""
	if idx := strings.IndexAny(arg, " \t"); idx >= 0 {
		name, args = arg[:idx], strings.TrimSpace(arg[idx+1:])
	}
	sk, found := l.findSkill(name)
	if !found {
		l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
		l.Store.Append(sessionID, events.TypeError, map[string]any{
			"error": fmt.Sprintf("unknown skill %q. Available: %s", name, l.skillNames()),
		})
		return true, nil
	}
	skillText, skillSpans := skillModelText(sk, args)
	return true, l.sendWithModelText(ctx, sessionID, agentName, text, skillText, "", "",
		messageOrigin{source: "skill.frame." + sk.Name, spans: skillSpans})
}

func (l *Loop) routeInit(ctx context.Context, sessionID, agentName, text string) (bool, error) {
	if strings.TrimSpace(text) != "/init" {
		return false, nil
	}
	return true, l.sendWithModelText(ctx, sessionID, agentName, text, initPrompt, "", "",
		messageOrigin{source: "command.init"})
}

func (l *Loop) routeMemory(sessionID, text string) (bool, error) {
	if strings.TrimSpace(text) != "/memory" {
		return false, nil
	}
	return true, l.showMemoryInfo(sessionID, text)
}

func (l *Loop) routeConfig(sessionID, text string) (bool, error) {
	arg, ok := parseConfigCommand(text)
	if !ok {
		return false, nil
	}
	return true, l.handleConfigCommand(sessionID, text, arg)
}

func (l *Loop) routeCompact(ctx context.Context, sessionID, agentName, text string) (bool, error) {
	arg, ok := parseCompactCommand(text)
	if !ok {
		return false, nil
	}
	return true, l.handleCompactCommand(ctx, sessionID, agentName, text, arg)
}

func (l *Loop) routeUsage(sessionID, text string) (bool, error) {
	if strings.TrimSpace(text) != "/usage" {
		return false, nil
	}
	return true, l.handleCostCommand(sessionID, text)
}

func (l *Loop) routeCustomCommand(ctx context.Context, sessionID, agentName, text string) (bool, error) {
	cmd, args, ok := l.matchCustomCommand(text)
	if !ok {
		return false, nil
	}
	// The session's directory, not the daemon's default. A command body
	// expands @file by reading it and !`cmd` by running it, and both are
	// the same claim a tool makes when it takes a relative path: it means
	// the project this conversation is in. Resolved daemon-wide, a
	// /review in a session working somewhere else quoted the other
	// project's file back at the model with this project's name on it.
	segs, err := commands.ExpandSegments(cmd, args, l.SessionDir(sessionID))
	if err != nil {
		l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
		l.Store.Append(sessionID, events.TypeError, map[string]any{"error": err.Error()})
		return true, nil
	}
	modelText, spans := expansionSpans(cmd.Name, segs)
	return true, l.sendWithModelText(ctx, sessionID, agentName, text, modelText, cmd.Agent, cmd.Model,
		messageOrigin{source: "command." + cmd.Name, spans: spans})
}

// routeSkillName recognizes "/<skill-name>" and "/<skill-name> <args>",
// the same shape custom commands use. Checked last among built-ins so
// nothing user-facing can be shadowed by a skill that happens to share a
// name: built-in commands win first, then custom commands, then skills.
func (l *Loop) routeSkillName(ctx context.Context, sessionID, agentName, text string) (bool, error) {
	sk, args, ok := l.matchSkillName(text)
	if !ok {
		return false, nil
	}
	skillText, skillSpans := skillModelText(sk, args)
	return true, l.sendWithModelText(ctx, sessionID, agentName, text, skillText, "", "",
		messageOrigin{source: "skill.frame." + sk.Name, spans: skillSpans})
}

// replyLocal records a command the user typed and the locally computed
// answer — no model call. The delta/end pair mirrors how a streamed model
// reply lands in the log, so clients render local answers with zero
// special cases.
func (l *Loop) replyLocal(sessionID, displayText, answer string) error {
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText, "local": true})
	return l.replyText(sessionID, answer)
}

// replyText appends just the local-answer half (delta+end) of replyLocal,
// for a handler that already recorded the user's command earlier — before
// doing work that can fail without producing an answer at all (e.g.
// handleCompactCommand, which must not emit a reply if compaction errors).
func (l *Loop) replyText(sessionID, answer string) error {
	l.Store.Append(sessionID, events.TypeMessagePartDelta, map[string]any{"text": answer})
	l.Store.Append(sessionID, events.TypeMessagePartEnd, map[string]any{"text": answer})
	return nil
}

// parseSkillCommand recognizes "/skill" and "/skill <name>". ok is false
// for anything else (including a message that merely mentions "/skill" in
// the middle of a sentence).
func parseSkillCommand(text string) (arg string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "/skill" {
		return "", true
	}
	if rest, found := strings.CutPrefix(trimmed, "/skill "); found {
		return strings.TrimSpace(rest), true
	}
	return "", false
}

// listSkills answers "/skill" locally — no model call — with the same
// name/description index that's in the system prompt.
func (l *Loop) listSkills(sessionID, displayText string) error {
	text := "No skills registered."
	if list := l.SkillList(); len(list) > 0 {
		var b strings.Builder
		b.WriteString("Available skills (/<name> to run one):\n")
		for _, s := range list {
			fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
		}
		text = b.String()
	}
	return l.replyLocal(sessionID, displayText, text)
}

// showMemoryInfo answers "/memory" locally — no model call — with the
// auto-memory directory path and current MEMORY.md index content, the
// same information Claude Code's "/memory" command surfaces.
func (l *Loop) showMemoryInfo(sessionID, displayText string) error {
	var text string
	if l.MemoryDir == "" {
		text = "Auto memory is disabled (config.json's \"auto_memory_enabled\": false)."
	} else {
		index := memory.LoadIndex(l.MemoryDir)
		var b strings.Builder
		fmt.Fprintf(&b, "Auto memory directory: %s\n", l.MemoryDir)
		fmt.Fprintf(&b, "Index file: %s\n\n", memory.IndexPath(l.MemoryDir))
		if index == "" {
			b.WriteString("No memory saved yet.")
		} else {
			b.WriteString(index)
		}
		text = b.String()
	}
	return l.replyLocal(sessionID, displayText, text)
}

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

	return l.replyText(sessionID, "Conversation compacted.")
}

// handleCostCommand answers "/usage" locally — no model call — with a
// per-model breakdown of cumulative token usage for this session (input,
// output, total, number of API calls), plus a grand total. Tokens only,
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
		models := make([]string, 0, len(totals))
		for m := range totals {
			models = append(models, m)
		}
		sort.Strings(models)

		var b strings.Builder
		b.WriteString("Token usage by model:\n")
		var grandInput, grandOutput, grandCalls int
		for _, m := range models {
			t := totals[m]
			fmt.Fprintf(&b, "- %s: input %d · output %d · total %d (%d calls)\n", m, t.InputTokens, t.OutputTokens, t.InputTokens+t.OutputTokens, t.Calls)
			grandInput += t.InputTokens
			grandOutput += t.OutputTokens
			grandCalls += t.Calls
		}
		fmt.Fprintf(&b, "\nGrand total: input %d · output %d · total %d (%d calls)", grandInput, grandOutput, grandInput+grandOutput, grandCalls)
		text = b.String()
	}

	return l.replyLocal(sessionID, displayText, text)
}

func (l *Loop) findSkill(name string) (skills.Skill, bool) {
	for _, s := range l.SkillList() {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	return skills.Skill{}, false
}

func (l *Loop) skillNames() string {
	list := l.SkillList()
	names := make([]string, len(list))
	for i, s := range list {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

// matchSkillName recognizes "/<skill-name>" and "/<skill-name> <args>"
// against a registered skill.
func (l *Loop) matchSkillName(text string) (skills.Skill, string, bool) {
	trimmed := strings.TrimSpace(text)
	rest, ok := strings.CutPrefix(trimmed, "/")
	if !ok {
		return skills.Skill{}, "", false
	}
	name, args := rest, ""
	if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
		name, args = rest[:idx], strings.TrimSpace(rest[idx+1:])
	}
	if name == "" {
		return skills.Skill{}, "", false
	}
	sk, found := l.findSkill(name)
	if !found {
		return skills.Skill{}, "", false
	}
	return sk, args, true
}

// skillModelText builds what the model actually receives when a skill is
// invoked: the skill's whole body, plus whatever the user typed after the
// command name, if anything. The transcript keeps only the short
// "/<name> ..." line the user typed.
// skillModelText returns the text and the spans that say which part of
// it is whose. Three authors in one message: localcode wrote the
// framing, whoever installed the skill wrote the body, and the person
// typed the arguments. Hashing the lot as one skill entry attributed
// all three to the skill.
func skillModelText(sk skills.Skill, args string) (string, []provider.BlockSource) {
	head := fmt.Sprintf("Follow the %q skill's instructions below to help with my request.\n\n---\n", sk.Name)
	var b strings.Builder
	var spans []provider.BlockSource
	b.WriteString(head)
	from := b.Len()
	b.WriteString(sk.Body)
	spans = append(spans, provider.BlockSource{ID: "skill.body." + sk.Name, From: from, To: b.Len()})
	b.WriteString("\n---")
	if args != "" {
		b.WriteString("\n\nMy request: ")
		from = b.Len()
		b.WriteString(args)
		spans = append(spans, provider.BlockSource{ID: "argument." + sk.Name, From: from, To: b.Len()})
	}
	return b.String(), spans
}

// matchCustomCommand recognizes "/<name>" or "/<name> <args>" against a
// loaded custom command. Built-in commands (/skill, /init) are checked by
// the caller first, so they always take precedence over a same-named
// custom command.
func (l *Loop) matchCustomCommand(text string) (commands.Command, string, bool) {
	trimmed := strings.TrimSpace(text)
	rest, ok := strings.CutPrefix(trimmed, "/")
	if !ok {
		return commands.Command{}, "", false
	}
	name, args := rest, ""
	if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
		name, args = rest[:idx], strings.TrimSpace(rest[idx+1:])
	}
	for _, c := range l.Commands {
		if c.Name == name {
			return c, args, true
		}
	}
	return commands.Command{}, "", false
}
