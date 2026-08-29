package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
	"localcode/internal/when"
)

// The three switches that decide how a turn behaves had one home each,
// and two of those homes were a window.
//
// Smart Agent could be flipped with "/config smart_agent on", which no
// help text mentioned, so in practice it was the settings panel. Skip
// permissions had no command at all: the panel was the only way, which
// means the TUI could not reach it. Auto delegation had the /config
// spelling and the panel's pill.
//
// So this file gives each one a command of its own, and does it on the
// daemon rather than in a client, because the switch is daemon state and
// because writing it once is what keeps the TUI and the Web UI saying
// the same thing about it.
//
// Each also persists, which /config never did. That was the older
// inconsistency: the panel wrote the choice to config.json and the
// command did not, so the same switch survived a restart or not
// depending on which of the two you had used. A toggle that forgets is
// not a toggle. When the file cannot be written the change still
// applies and the reply says both halves, for the reason the settings
// endpoint answers 200 with applied and persisted separately: a change
// reported as not having happened, which did, is worse than one
// reported as unsaved.

// toggleArg reads "on", "off", or neither. An absent argument means
// "flip it", which is what makes these toggles rather than setters: the
// common use is one word at the prompt, not a word and a state.
func toggleArg(arg string, current bool) (want bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "":
		return !current, true
	case "on", "true", "yes":
		return true, true
	case "off", "false", "no":
		return false, true
	}
	return false, false
}

// routeSmartAgent answers "/smart-agent [on|off]".
func (l *Loop) routeSmartAgent(sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/smart-agent")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	want, valid := toggleArg(arg, l.SmartAgentEnabled())
	if !valid {
		return true, l.replyText(sessionID, "usage: /smart-agent [on|off]")
	}
	l.SetSmartAgentEnabled(want)

	var b strings.Builder
	fmt.Fprintf(&b, "smart_agent: %s", onOff(want))
	// The roster is the useful half: "on" alone does not say what it
	// turned on, and the answer depends on which profiles this config
	// happens to have.
	if names := agentNamesOf(l.smartAgents(context.Background())); len(names) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(names, ", "))
	} else if want {
		b.WriteString("\n(no profiles configured, so no specialist agents could be created; see docs/USAGE.md)")
	}
	b.WriteString(l.persist(func(path string) error { return config.SetSmartAgentInFile(path, want) }))

	l.announceConfig(sessionID)
	return true, l.replyText(sessionID, b.String())
}

// routeAutoDelegate answers "/auto-delegate [on|off]".
func (l *Loop) routeAutoDelegate(sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/auto-delegate")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	want, valid := toggleArg(arg, l.AutoDelegateEnabled())
	if !valid {
		return true, l.replyText(sessionID, "usage: /auto-delegate [on|off]")
	}
	l.SetAutoDelegateEnabled(want)

	var b strings.Builder
	fmt.Fprintf(&b, "auto_delegate: %s", onOff(want))
	// Where prompts are going is the useful half, and turning it on with
	// nothing configured is inert, so say so rather than let it read as
	// a change that took effect.
	if cfg := l.Config.AutoDelegateSnapshot(); cfg != nil && cfg.Agent != "" {
		fmt.Fprintf(&b, " (-> %s)", cfg.Agent)
	} else if want {
		b.WriteString("\n(no auto_delegate block in config.json, so nothing will be delegated; see docs/USAGE.md)")
	}
	b.WriteString(l.persist(func(path string) error { return config.SetAutoDelegateEnabledInFile(path, want) }))

	l.announceConfig(sessionID)
	return true, l.replyText(sessionID, b.String())
}

// The four permission switches, and why they are four.
//
// There used to be one: skip_permissions, daemon-wide, every ask turned
// into an allow. It is the setting most likely to be flipped on for one
// task and forgotten, and while it was on the model could write anywhere
// on the machine without a word — including into a project this
// conversation was never told about.
//
// So the blanket is split. skip_tools is the one people actually want:
// stop interrupting me about this project, and still ask before anything
// leaves it. read-outside and write-outside are the two halves of
// leaving, separate because they are not the same risk. skip_all is what
// it always was, and is now the only one that crosses the boundary.
//
// All four are per session (see session.Permissions), which is why these
// four commands set the conversation they are typed in rather than the
// daemon. config.json still holds the defaults for a session that has
// not answered.

// routeSkipPermissions answers "/permission-skip-all [on|off]".
func (l *Loop) routeSkipPermissions(sessionID, text string) (bool, error) {
	return l.routeSwitch(sessionID, text, "/permission-skip-all", session.SwitchSkipAll, func(b *strings.Builder, want bool) {
		if !want {
			return
		}
		// What it does and what it does not, because "skip permissions"
		// reads like "skip all safety" and it is not: a deny rule still
		// denies, and the credential guards are deny-class.
		b.WriteString("\nEvery prompt that would have asked is now allowed in this conversation, shell" +
			"\ncommands and reads and writes outside the workspace included. Rules that deny still" +
			"\ndeny, and the credential-file guards are unaffected." +
			"\nFor the same thing without the last part, use /permission-skip-tools.")
	})
}

// routeSkipTools answers "/permission-skip-tools [on|off]".
func (l *Loop) routeSkipTools(sessionID, text string) (bool, error) {
	return l.routeSwitch(sessionID, text, "/permission-skip-tools", session.SwitchSkipTools, func(b *strings.Builder, want bool) {
		if !want {
			return
		}
		b.WriteString("\nEvery tool prompt in this conversation is now allowed, and the workspace boundary" +
			"\nis not: a path that leaves " + l.SessionDir(sessionID) + " is still a question." +
			"\nAnswer that one for good with /read-outside or /write-outside.")
	})
}

// routeReadOutside answers "/read-outside [on|off|mem-clear]".
func (l *Loop) routeReadOutside(sessionID, text string) (bool, error) {
	return l.routeOutside(sessionID, text, "/read-outside", session.SwitchReadOutside, tools.OutsideRead)
}

// routeWriteOutside answers "/write-outside [on|off|mem-clear]".
func (l *Loop) routeWriteOutside(sessionID, text string) (bool, error) {
	return l.routeOutside(sessionID, text, "/write-outside", session.SwitchWriteOutside, tools.OutsideWrite)
}

// routeOutside is the two boundary commands, which differ only in which
// half they are about. Both take one argument the other two do not:
// mem-clear, which forgets the individual directories approved at a
// prompt without touching the switch. The two retractions are different
// and somebody who approved one directory by mistake should not have to
// change a setting to take it back.
func (l *Loop) routeOutside(sessionID, text, name string, sw session.Switch, class tools.OutsideClass) (bool, error) {
	arg, ok := matchToggleCommand(text, name)
	if !ok {
		return false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(arg), "mem-clear") {
		return l.routeSwitch(sessionID, text, name, sw, func(b *strings.Builder, want bool) {
			if want {
				fmt.Fprintf(b, "\nReaching outside %s to %s no longer asks, in this conversation.", l.SessionDir(sessionID), class)
			} else {
				fmt.Fprintf(b, "\nEach new directory outside %s will be asked about once.", l.SessionDir(sessionID))
			}
		})
	}

	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
	if l.ForgetOutside == nil {
		return true, l.replyText(sessionID, "this build has no permission broker, so there is nothing remembered to clear")
	}
	n := l.ForgetOutside(sessionID, class)
	if l.OnPermissionsChanged != nil {
		l.OnPermissionsChanged(sessionID)
	}
	if n == 0 {
		return true, l.replyText(sessionID, fmt.Sprintf("%s mem-clear: nothing was remembered.", name))
	}
	return true, l.replyText(sessionID, fmt.Sprintf(
		"%s mem-clear: forgot %d approved director%s. The next %s outside the workspace asks again.",
		name, n, plural(n, "y", "ies"), class))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// routeSwitch is the shape all four share: read the argument, set this
// session's own answer, say what it is now and what that means.
//
// Setting the session rather than the daemon is the change these
// commands carry. A switch flipped here used to be flipped for every
// conversation on the machine, so "skip permissions while I do this one
// thing" also skipped them in the window editing something that
// mattered.
func (l *Loop) routeSwitch(sessionID, text, name string, sw session.Switch, explain func(*strings.Builder, bool)) (bool, error) {
	arg, ok := matchToggleCommand(text, name)
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	now, from := l.Permissions.Effective(sessionID, sw)
	want, valid := toggleArg(arg, now)
	if !valid {
		return true, l.replyText(sessionID, fmt.Sprintf("usage: %s [on|off]", name))
	}
	if err := l.Permissions.Set(sessionID, sw, &want); err != nil {
		return true, l.replyText(sessionID, fmt.Sprintf("could not set %s: %v", sw, err))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s (this conversation)", sw, onOff(want))
	if want == now && from == SourceDefault {
		// It already read this way, from config.json. Saying so stops
		// "on" being read as a change that did something.
		fmt.Fprintf(&b, "\nIt already was, from config.json; it is now this conversation's own setting.")
	}
	if explain != nil {
		explain(&b, want)
	}
	if l.OnPermissionsChanged != nil {
		l.OnPermissionsChanged(sessionID)
	}
	return true, l.replyText(sessionID, b.String())
}

// persist writes the change to config.json when there is one to write,
// and returns the sentence to add to the reply.
//
// Empty when it saved, because a toggle that worked has nothing to say.
// The two failure cases are distinguished: no file to write to is a
// setup fact and lasts until the next restart; a write that failed is a
// problem with the file.
func (l *Loop) persist(write func(path string) error) string {
	if l.ConfigPath == "" {
		return "\n(this run only: no config.json to save it in)"
	}
	if err := write(l.ConfigPath); err != nil {
		return fmt.Sprintf("\n(applied for this run, but could not be saved to %s: %v)", l.ConfigPath, err)
	}
	return ""
}

// announceConfig records the change in this conversation's log, the same
// shape "/config" emits, and tells every client on the daemon.
//
// Both, because they answer different questions. The session event is
// history: it says this conversation is the one the switch was flipped
// in, and it replays. The broadcast is the live one, and it is the only
// half that reaches a client looking at another session or at no session
// at all, which is where a second window sat showing the old state.
func (l *Loop) announceConfig(sessionID string) {
	l.Store.Append(sessionID, events.TypeConfigChanged, map[string]any{
		"auto_compact_enabled": l.AutoCompactEnabled(),
		"show_tps":             l.ShowTPS(),
		"auto_delegate":        l.AutoDelegateEnabled(),
		"smart_agent":          l.SmartAgentEnabled(),
	})
	l.announceSettings()
}

// announceSettings tells every client on the daemon what all the
// switches are now.
func (l *Loop) announceSettings() {
	if l.OnSettingsChanged != nil {
		l.OnSettingsChanged()
	}
}

// matchToggleCommand recognizes "/name" and "/name <arg>", matching the
// name case-insensitively the way every other command here does.
func matchToggleCommand(text, name string) (arg string, ok bool) {
	t := strings.TrimSpace(text)
	if strings.EqualFold(t, name) {
		return "", true
	}
	rest, found := strings.CutPrefix(strings.ToLower(t), strings.ToLower(name)+" ")
	if !found {
		return "", false
	}
	// The original case, not the lowercased copy used for matching.
	return strings.TrimSpace(t[len(name)+1:]), rest != ""
}

// SlashCommand is one command the daemon answers itself, for a client
// that wants to list or complete them.
type SlashCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SlashCommands is every command SendMessage intercepts before the text
// reaches a model.
//
// Declared rather than derived from commandRoutes, which holds closures
// and no names. Two lists that can disagree is the fault this is one
// step away from, so the test beside it walks this list through the
// router and requires every entry to be answered locally.
//
// It exists because a client cannot complete what it cannot name. The
// clients knew the skills and the custom commands, both of which they
// fetch, and nothing about the commands the daemon answers itself, so
// "/smart-agent" was the one kind of command the completion could not
// finish.
func SlashCommands() []SlashCommand {
	return []SlashCommand{
		{Name: "skill", Description: "list installed skills"},
		{Name: "init", Description: "scan the repository and write or improve AGENTS.md"},
		{Name: "memory", Description: "show the auto-memory directory and index"},
		{Name: "config", Description: "show settings, or change one with /config <name> on|off"},
		{Name: "smart-agent", Description: "turn the Smart Agent bundle on or off"},
		{Name: "auto-delegate", Description: "turn auto-delegation on or off"},
		{Name: "permission-skip-all", Description: "allow every prompt in this conversation, the workspace boundary included"},
		{Name: "permission-skip-tools", Description: "allow every tool prompt, but still ask before leaving the workspace"},
		{Name: "effort", Description: "how hard the model is asked to think in this conversation: off, low, medium, high"},
		{Name: "debate", Description: "have other agents review this one's work, round after round: /debate <reviewer>[,<reviewer>] [rounds] <what to do>"},
		{Name: "schedule", Description: "book a prompt for later: /schedule <when> <what to do>; also cancel and rename"},
		{Name: "show-scheduled-task", Description: "list the prompts booked for later in this conversation"},
		{Name: "read-outside", Description: "reading outside the workspace: on, off, or mem-clear to forget approved directories"},
		{Name: "write-outside", Description: "writing outside the workspace: on, off, or mem-clear to forget approved directories"},
		{Name: "keep-going", Description: "toggle the carry-on nudge for muse models"},
		{Name: "auto-compact", Description: "toggle auto-compaction, or set its threshold with a percent"},
		{Name: "reset-mcp", Description: "reconnect MCP servers and pick up config changes without a restart"},
		{Name: "reset-skills", Description: "reload skills from disk without a restart"},
		{Name: "compact", Description: "summarize the conversation now, optionally with instructions"},
		{Name: "usage", Description: "cumulative token usage per model"},
		{Name: "context", Description: "what the next request is made of; /context all, /context <id>"},
	}
}

// routeKeepGoing answers "/keep-going [on|off]".
func (l *Loop) routeKeepGoing(sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/keep-going")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	want, valid := toggleArg(arg, l.KeepGoingEnabled())
	if !valid {
		return true, l.replyText(sessionID, "usage: /keep-going [on|off]")
	}
	l.SetKeepGoingEnabled(want)

	var b strings.Builder
	fmt.Fprintf(&b, "keep_going: %s", onOff(want))
	// The scope is the part worth a sentence: the switch is daemon-wide
	// and the feature is one family's. Someone turning it on while every
	// profile runs another model has changed nothing, and should hear
	// that from the reply rather than from the absence of any effect.
	b.WriteString("\nApplies only to models whose id contains \"muse\"; other models are never nudged.")
	if want && !l.anyMuseProfile() {
		b.WriteString("\n(no configured profile currently runs a muse model, so nothing changes until one does)")
	}
	b.WriteString(l.persist(func(path string) error { return config.SetKeepGoingInFile(path, want) }))

	l.announceSettings()
	return true, l.replyText(sessionID, b.String())
}

// anyMuseProfile reports whether any configured profile runs a model the
// keep-going feature applies to.
func (l *Loop) anyMuseProfile() bool {
	for _, p := range l.Config.Profiles {
		if keepGoingApplies(p.Model) {
			return true
		}
	}
	return false
}

// routeAutoCompact answers "/auto-compact [on|off|<percent>]".
//
// A number both sets the threshold and turns the feature on, because
// "/auto-compact 70" is somebody asking for compaction at 70%, and
// honouring the number while leaving the switch off would honour the
// letter of it and not the request.
func (l *Loop) routeAutoCompact(sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/auto-compact")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	if n, err := strconv.Atoi(strings.TrimSpace(arg)); err == nil {
		if n < 10 || n > 95 {
			return true, l.replyText(sessionID,
				"usage: /auto-compact [on|off|<percent>], percent between 10 and 95.\nBelow 10 would compact almost every turn; above 95 would never fire, which is what off is for.")
		}
		l.SetCompactPercent(n)
		l.SetAutoCompactEnabled(true)
		var b strings.Builder
		fmt.Fprintf(&b, "auto_compact: on, at %d%% of the context window", n)
		b.WriteString(l.persist(func(path string) error { return config.SetAutoCompactInFile(path, true, n) }))
		l.announceConfig(sessionID)
		return true, l.replyText(sessionID, b.String())
	}

	want, valid := toggleArg(arg, l.AutoCompactEnabled())
	if !valid {
		return true, l.replyText(sessionID, "usage: /auto-compact [on|off|<percent>]")
	}
	l.SetAutoCompactEnabled(want)
	var b strings.Builder
	fmt.Fprintf(&b, "auto_compact: %s", onOff(want))
	if want {
		fmt.Fprintf(&b, " (at %d%% of the context window; /auto-compact <percent> changes it)", l.CompactPercent())
	}
	b.WriteString(l.persist(func(path string) error { return config.SetAutoCompactInFile(path, want, 0) }))
	l.announceConfig(sessionID)
	return true, l.replyText(sessionID, b.String())
}

// routeResetMCP answers "/reset-mcp": stop the MCP servers, re-read
// their configuration, and reconnect, without restarting localcode.
//
// A hook rather than an implementation, because everything it needs
// lives above this package: the manager, the tool registry swap, the
// config re-read. The daemon wires it; a Loop without one says so.
func (l *Loop) routeResetMCP(sessionID, text string) (bool, error) {
	if !strings.EqualFold(strings.TrimSpace(text), "/reset-mcp") {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
	if l.ReloadMCP == nil {
		return true, l.replyText(sessionID, "this build has no MCP reload wired; restart localcode to apply MCP changes")
	}
	report, err := l.ReloadMCP()
	if err != nil {
		return true, l.replyText(sessionID, "MCP reload failed: "+err.Error())
	}
	return true, l.replyText(sessionID, report)
}

// routeResetSkills answers "/reset-skills": reload the skills from disk
// so an edited or newly installed one applies without a restart.
func (l *Loop) routeResetSkills(sessionID, text string) (bool, error) {
	if !strings.EqualFold(strings.TrimSpace(text), "/reset-skills") {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
	if l.ReloadSkills == nil {
		return true, l.replyText(sessionID, "this build has no skill reload wired; restart localcode to apply skill changes")
	}
	report, err := l.ReloadSkills()
	if err != nil {
		return true, l.replyText(sessionID, "skill reload failed: "+err.Error())
	}
	return true, l.replyText(sessionID, report)
}

// routeSchedule answers "/schedule <when> <prompt>" and
// "/schedule cancel <id>".
//
// The time is read by internal/when, not by the model, and the reply
// echoes what was read. That echo is the whole reason parsing is allowed
// to guess at all: a misread time is caught here, before the work is
// booked, rather than by the work not happening.
func (l *Loop) routeSchedule(sessionID, agentName, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/schedule")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
	if l.Schedules == nil {
		return true, l.replyText(sessionID, "this build has no scheduler")
	}
	if arg == "" {
		return true, l.replyText(sessionID, "usage: /schedule <when> <what to do>\n"+when.Examples+
			"\n\"/schedule cancel <id>\" removes one, \"/schedule rename <id> <name>\" labels one,"+
			"\nand \"/show-scheduled-task\" lists them.")
	}
	if _, found := strings.CutPrefix(strings.ToLower(arg), "rename "); found {
		rest := strings.TrimSpace(arg[len("rename "):])
		id, name, _ := strings.Cut(rest, " ")
		if id == "" {
			return true, l.replyText(sessionID, "usage: /schedule rename <id> <name>  (an empty name clears it)")
		}
		entry, ok := l.Schedules.Rename(sessionID, id, strings.TrimSpace(name))
		if !ok {
			return true, l.replyText(sessionID, fmt.Sprintf("no scheduled task %q in this conversation", id))
		}
		if entry.Name == "" {
			return true, l.replyText(sessionID, "cleared the name of "+id)
		}
		return true, l.replyText(sessionID, fmt.Sprintf("%s is now %q", id, entry.Name))
	}
	if _, found := strings.CutPrefix(strings.ToLower(arg), "cancel "); found {
		// Sliced from the original rather than the lowercased copy: an id
		// is case-sensitive and only the prefix was being matched loosely.
		id := strings.TrimSpace(arg[len("cancel "):])
		if l.Schedules.Cancel(sessionID, id) {
			return true, l.replyText(sessionID, "cancelled "+id)
		}
		return true, l.replyText(sessionID, fmt.Sprintf("no scheduled task %q in this conversation", id))
	}

	now := time.Now()
	at, prompt, err := when.Parse(arg, now)
	if err != nil {
		return true, l.replyText(sessionID, err.Error())
	}
	entry, err := l.Schedules.Add(sessionID, agentName, prompt, at)
	if err != nil {
		return true, l.replyText(sessionID, err.Error())
	}

	var b strings.Builder
	fmt.Fprintf(&b, "scheduled for %s\n  %s\n  %s", when.Format(at, now), promptSummary(prompt), entry.ID)
	// Said every time, because it is the one promise this feature does
	// not make and the one people assume it does.
	b.WriteString("\n\nIt runs only while localcode is running. If this machine is asleep or" +
		"\nlocalcode is closed at that moment, the task is reported as missed rather than" +
		"\nrun late. It will work in " + l.SessionDir(sessionID) + " under this conversation's" +
		"\npermission settings.")
	// No separate announcement: Add already wrote schedule.created to
	// this conversation's log, which is where a client's row comes from.
	return true, l.replyText(sessionID, b.String())
}

// routeShowScheduled answers "/show-scheduled-task".
func (l *Loop) routeShowScheduled(sessionID, text string) (bool, error) {
	if _, ok := matchToggleCommand(text, "/show-scheduled-task"); !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
	if l.Schedules == nil {
		return true, l.replyText(sessionID, "this build has no scheduler")
	}
	list := l.Schedules.List(sessionID)
	if len(list) == 0 {
		return true, l.replyText(sessionID, "no scheduled tasks in this conversation.\n"+
			"Book one with \"/schedule <when> <what to do>\". "+when.Examples)
	}
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "%d scheduled task(s) in this conversation:\n\n", len(list))
	for _, e := range list {
		b.WriteString(Describe(e, now))
		b.WriteString("\n\n")
	}
	b.WriteString("\"/schedule cancel <id>\" removes one. They run only while localcode is running.")
	return true, l.replyText(sessionID, strings.TrimRight(b.String(), "\n"))
}
