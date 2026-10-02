package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"localcode/internal/commands"
	"localcode/internal/events"
	"localcode/internal/hooks"
	"localcode/internal/provider"
)

// initPrompt is what "/init" sends to the model — the same idea as
// opencode's "/init": scan the repo and write an AGENTS.md rules file so
// future turns (in this project or picked up by opencode/Claude Code too,
// since both read AGENTS.md/CLAUDE.md) start with real project context.
const initPrompt = `Scan this repository and create or update an AGENTS.md file at the project root with concise, project-specific guidance for a coding agent.

Read enough to be specific rather than generic. Worth looking at:
- the README and any docs/ directory
- package and build manifests, and the lockfiles beside them (they name the versions actually in use)
- CI workflows and pre-commit configuration, which say what has to pass before a change lands
- the order build, lint and test commands are meant to run in
- whether this is a monorepo, and where the boundaries between its parts are
- rules files another agent already left: CLAUDE.md, .cursorrules, .cursor/rules/, .github/copilot-instructions.md

Write build/lint/test commands, an architecture overview, and the code conventions this repository actually follows. If AGENTS.md already exists, improve it in place rather than replacing it wholesale. Use your file tools (Glob/Grep/Read to explore, Write or Edit to save AGENTS.md).`

// SendMessage appends a user turn to sessionID's history and drives the
// agent loop (model call -> optional tool calls -> model call -> ...) until
// the model produces a final answer. agentName selects which model profile
// to use, per the config's agents map. Optional images are attached to the
// user turn.
func (l *Loop) SendMessage(ctx context.Context, sessionID, agentName, text string, images ...provider.Block) error {
	if len(images) > 0 {
		var content []provider.Block
		if text != "" {
			content = append(content, provider.TextBlock(text))
		}
		content = append(content, images...)
		if err := provider.ValidateMessageImages(provider.Message{
			Role:    provider.RoleUser,
			Content: content,
		}); err != nil {
			return err
		}
	}

	// The admission boundary for a top-level message, and therefore where
	// the Smart Agent setting is pinned. Not in sendWithModelText, which
	// is reached only after command routing and auto-delegation have had
	// their turn: auto-delegation hands the work to SpawnSync, which can
	// sit on the task semaphore, and a switch flipped while it waited used
	// to reach the child. Pinning here covers every route out of this
	// function. A delegated turn arrives with its parent's pin and keeps
	// it. See config.WithSmartAgent.
	ctx = l.pinSmart(ctx)
	// The ask_user budget is per turn, and this is where a turn begins
	// and ends for every route out of this function.
	defer l.releaseAsk(sessionID)
	// One debug-log file per prompt, opened here because this is where a
	// prompt begins. A delegated turn arrives with its parent's context
	// and keeps its sink, so a sub-agent's calls land in the file the
	// person's prompt opened rather than in one of their own.
	ctx, closeLog := l.openDebugLog(ctx, sessionID)
	defer closeLog()

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
	// contract: the "!" shell escape, then built-in commands, then custom
	// commands, then skills, then auto-delegation, then an ordinary model
	// turn — nothing user-facing can be shadowed by a later entry. See
	// commandRoutes.
	if !delegated {
		// An escaped message is ordinary text that happens to begin with
		// "!"; it walks every route below as that text, so "!!/status"
		// reaches the model rather than running anything. An unescaped
		// one never reaches the table at all.
		if escaped, ok := stripBangEscape(text); ok {
			text = escaped
		} else if command, ok := parseBang(text); ok {
			return l.runBangCommand(ctx, sessionID, text, command)
		}
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
	modelText, origin := text, messageOrigin{images: images}
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
		func() (bool, error) { return l.routeModel(sessionID, text) },
		func() (bool, error) { return l.routeSchedule(sessionID, agentName, text) },
		func() (bool, error) { return l.routeShowScheduled(sessionID, text) },
		func() (bool, error) { return l.routeKeepGoing(sessionID, agentName, text) },
		func() (bool, error) { return l.routeThinking(sessionID, text) },
		func() (bool, error) { return l.routeFoldThinking(sessionID, text) },
		func() (bool, error) { return l.routeTimestamps(sessionID, text) },
		func() (bool, error) { return l.routeRepeatLimit(sessionID, text) },
		func() (bool, error) { return l.routeDebugLog(sessionID, text) },
		func() (bool, error) { return l.routeAutoCompact(sessionID, text) },
		func() (bool, error) { return l.routeUpdate(sessionID, text) },
		func() (bool, error) { return l.routeMCPs(sessionID, text) },
		func() (bool, error) { return l.routeResetMCP(sessionID, text) },
		func() (bool, error) { return l.routeResetSkills(sessionID, text) },
		func() (bool, error) { return l.routeStatus(sessionID, text) },
		func() (bool, error) { return l.routeDebug(sessionID, agentName, text) },
		func() (bool, error) { return l.routeWorkspace(sessionID, text) },
		func() (bool, error) { return l.routeCompact(ctx, sessionID, agentName, text) },
		// Beside compaction, which is the command they are variations of:
		// all three decide what the model is sent without touching what
		// happened. Ahead of the custom-command and skill routes at the
		// bottom, so a built-in name wins — see the note on shadowing in
		// docs/USAGE.md.
		func() (bool, error) { return l.routeClear(sessionID, text) },
		func() (bool, error) { return l.routeRewind(ctx, sessionID, text) },
		func() (bool, error) { return l.routeRedo(ctx, sessionID, text) },
		func() (bool, error) { return l.routeModelInvocable(sessionID, text) },
		func() (bool, error) { return l.routeUsage(sessionID, text) },
		func() (bool, error) { return l.routeExport(sessionID, text) },
		func() (bool, error) { return l.routeLLMDoctor(ctx, sessionID, agentName, text) },
		func() (bool, error) { return l.routeContext(ctx, sessionID, agentName, text) },
		func() (bool, error) { return l.routeCustomCommand(ctx, sessionID, agentName, text) },
		func() (bool, error) { return l.routeSkillName(ctx, sessionID, agentName, text) },
		// After the two above, and it is the only built-in that is.
		//
		// Every other built-in comes first, so a name the product owns
		// cannot be shadowed — see the note on shadowing in
		// docs/USAGE.md. "/review" is the exception because the name was
		// in people's .localcode/commands before it was a built-in:
		// there was no built-in, the docs said to write a custom command
		// for exactly this, and they did. Taking the name now would
		// silently replace a file somebody wrote and tuned with a
		// template that knows nothing about their repository.
		//
		// So a review.md of your own still wins, and this answers for
		// everybody who never wrote one.
		func() (bool, error) { return l.routeReview(ctx, sessionID, agentName, text) },
		// Last, and it claims what is left that still looks like a
		// command. Everything above has had its turn, so anything that
		// resolves has already won; what reaches here is a slash nothing
		// answers, and sending that to a model is how a typo became
		// `git clean`. See routeUnknownCommand.
		func() (bool, error) { return l.routeUnknownCommand(sessionID, text) },
	}
}

// routeInit answers "/init" and "/init <what to focus on>".
//
// The argument matters more than it looks: before it was accepted, "/init
// focus on the tests" matched nothing, fell to routeUnknownCommand, and
// was answered "there is no /init in this build" — directly above a list
// with /init in it.
func (l *Loop) routeInit(ctx context.Context, sessionID, agentName, text string) (bool, error) {
	focus, ok := matchToggleCommand(text, "/init")
	if !ok {
		return false, nil
	}
	prompt := initPrompt
	if focus != "" {
		prompt += "\n\nThe person asking added this, and it takes precedence over the general guidance above:\n" + focus
	}
	return true, l.sendWithModelText(ctx, sessionID, agentName, text, prompt, "", "",
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
	arg, ok := matchToggleCommand(text, "/usage")
	if !ok {
		return false, nil
	}
	if arg == "" {
		return true, l.handleCostCommand(sessionID, text)
	}
	// A window, or nothing this command knows. The usage line names the
	// words rather than guessing at one, because guessing wrong here
	// answers a question about a different period than the one asked.
	w, valid := parseUsageWindow(arg, time.Now())
	if !valid {
		return true, l.replyLocal(sessionID, text,
			"usage: /usage for this conversation, or /usage all|today|week|month across every conversation.")
	}
	totals, sessions, unread := l.usageAcross(w)
	return true, l.replyLocal(sessionID, text, usageAcrossReport(w, totals, sessions, unread))
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
