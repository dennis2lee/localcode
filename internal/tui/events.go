package tui

import (
	"fmt"
	"strings"
	"time"

	"localcode/internal/client"
	"localcode/internal/events"
)

// strField reads a string out of an event payload, or "" when it is
// missing or something else.
func strField(data map[string]any, key string) string {
	s, _ := data[key].(string)
	return s
}

// stringsField reads a list of strings. The same payload arrives as
// []any over the wire and can arrive as []string from a store in this
// process, so both are taken — the reason intField below takes two
// number types.
func stringsField(data map[string]any, key string) []string {
	switch v := data[key].(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// modelsField reads a name-to-model map out of an event payload. The
// same payload arrives as map[string]any over the wire and can arrive
// as map[string]string from a store in this process, so both are taken
// — the reason stringsField above takes two list types.
func modelsField(data map[string]any, key string) map[string]string {
	switch v := data[key].(type) {
	case map[string]string:
		out := make(map[string]string, len(v))
		for name, model := range v {
			out[name] = model
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(v))
		for name, x := range v {
			if model, ok := x.(string); ok {
				out[name] = model
			}
		}
		return out
	}
	return nil
}

// intField reads a number out of an event payload. JSON has one number
// type and it arrives as a float64 over the wire but as an int from a
// store in this same process, so both are accepted — a debate round
// rendered as "round 0/0" because the event came from the wrong side of
// that line would be a bug nobody could see the cause of.
func intField(data map[string]any, key string) int {
	switch v := data[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// showThinkingOf reads show_thinking off a thinking event: the switch
// rides on the event, the way show_tps rides on usage, because this
// client keeps no copy of the daemon's settings. Absent means on, which
// is the switch's own default.
func showThinkingOf(data map[string]any) bool {
	v, ok := data["show_thinking"].(bool)
	return !ok || v
}

// endTurn clears everything that means "a turn is running". Called
// unconditionally for every turn-terminating event, including an error
// event whose payload turns out to be malformed — previously the "waiting"
// flag was only cleared inside the `if msg, ok := ...; ok` branch of the
// TypeError case, so a malformed error payload left the spinner running
// forever with no way to recover short of restarting the TUI.
func (m *Model) endTurn() {
	m.waiting = false
	m.runningTool = ""
	m.toolStartedAt = time.Time{}
	m.thinking = false
	// A block still open when the turn ends is one whose end never came:
	// the stream stopped, or the turn was cancelled mid-thought.
	m.foldThinking(0)
	// A cancelled call never gets its tool.end, so whatever its start
	// left behind would otherwise sit here until a call reuses its id.
	clear(m.pendingTools)
}

func (m *Model) applyEvent(ev events.Event) {
	rev := m.transcriptRev
	switch ev.Type {
	case events.TypeUserMessage:
		// auto marks a message localcode sent on the user's behalf —
		// keep_going telling a stalled model to carry on. Logged so the
		// model's history survives a restart, announced by its own note,
		// and not something to paint as a line the person typed or to put
		// in Up/Down recall.
		if auto, _ := ev.Data["auto"].(bool); auto {
			break
		}
		text, _ := ev.Data["text"].(string)
		if text != "" {
			// Every prompt this session has seen goes into Up/Down recall,
			// whoever typed it and whenever — which is what gives a session
			// the TUI has just attached to a recall list at all.
			m.recordHistory(text)
		}
		var imgNotes []string
		if rawImgs, ok := ev.Data["images"].([]any); ok {
			for _, item := range rawImgs {
				if imgMap, ok := item.(map[string]any); ok {
					mt, _ := imgMap["media_type"].(string)
					if mt != "" {
						imgNotes = append(imgNotes, fmt.Sprintf("[image: %s]", mt))
					} else {
						imgNotes = append(imgNotes, "[image]")
					}
				}
			}
		} else if typedImgs, ok := ev.Data["images"].([]events.Image); ok {
			for _, img := range typedImgs {
				if img.MediaType != "" {
					imgNotes = append(imgNotes, fmt.Sprintf("[image: %s]", img.MediaType))
				} else {
					imgNotes = append(imgNotes, "[image]")
				}
			}
		}
		displayText := text
		if len(imgNotes) > 0 {
			notes := strings.Join(imgNotes, "\n")
			if displayText != "" {
				displayText = displayText + "\n" + notes
			} else {
				displayText = notes
			}
		}
		if displayText != "" {
			m.appendUser(text, displayText)
		}
	case events.TypeMessagePartDelta:
		if text, ok := ev.Data["text"].(string); ok {
			// The answer has started, so the reasoning before it is
			// over whether or not its end arrived first.
			m.foldThinking(0)
			m.appendModelDelta(text)
		}
	case events.TypeMessagePartEnd:
		// One model message ended — NOT the turn. A turn with tool calls
		// streams several of these (text, then the post-tool follow-up),
		// and treating the first as end-of-turn is what used to make a
		// prompt typed during tool execution skip the queue and 409.
		text, _ := ev.Data["text"].(string)
		// Command output the person ran is not something the model said,
		// so it draws as a status line rather than a model message. The
		// header names the command; the user message above it already
		// shows the line as typed.
		if command, _ := ev.Data["shell_command"].(string); command != "" {
			m.appendTool("$ " + command + "\n" + text)
			break
		}
		m.endModelStream(text)
	case events.TypeSessionForked:
		// A fork copies the conversation verbatim, so nothing else in this
		// transcript says it is a copy.
		from, _ := ev.Data["from_title"].(string)
		if from == "" {
			from, _ = ev.Data["from"].(string)
		}
		m.appendTool("[this is a fork of \"" + from + "\" — the original is untouched]")
	case events.TypeCleared:
		// Nothing else in the transcript says the model stopped seeing
		// what is above this line, because everything above it is still
		// there — which is the point, and is exactly why it needs saying.
		m.appendTool("[cleared: the model starts fresh from here. Everything above stays in this conversation]")
		m.forgetContextFill()
	case events.TypeCompacted:
		// The transcript line for a compaction is the model's own
		// summary arriving as a message; here only the gauge changes.
		m.forgetContextFill()
	case events.TypeRewound:
		what, _ := ev.Data["turn_text"].(string)
		if what != "" {
			what = ": " + what
		}
		m.appendTool("[rewound one turn" + what + rewoundFiles(ev.Data) + "]")
		// The undone prompt goes back in the box, so the turn can be
		// retyped from where it went wrong. Only into an empty box:
		// whatever is already typed is a newer intention than the one
		// being undone, and overwriting it would be the same loss in
		// the other direction.
		if prompt, _ := ev.Data["prompt"].(string); prompt != "" && strings.TrimSpace(m.input.Value()) == "" {
			m.input.SetValue(prompt)
			m.resizeLayout()
		}
		m.forgetContextFill()
	case events.TypeRedone:
		what, _ := ev.Data["turn_text"].(string)
		if what != "" {
			what = ": " + what
		}
		m.appendTool("[put the turn back" + what + redoneFiles(ev.Data) + "]")
		m.forgetContextFill()
	case events.TypeSessionScheduled:
		// Opened on its own, a run session is a conversation that starts
		// with an instruction nobody in it typed, at a moment nobody was
		// there for. This is where it came from.
		m.appendTool("[" + scheduledHead(ev.Data) + "]")
	case events.TypeTurnDone:
		// The daemon's real turn boundary, emitted after its busy flag is
		// cleared — safe to stop waiting and let the queue drain.
		m.endTurn()
	case events.TypeToolStart:
		// A line per call, as well as the busy indicator below the prompt
		// box. The indicator alone says only what is running right now and
		// clears when it stops, so a turn that spends minutes in tools
		// left nothing on screen either while it worked or afterwards.
		m.runningTool, _ = ev.Data["name"].(string)
		// The clock starts with the start event rather than the first
		// paint of the indicator, so a tool that runs between frames is
		// still timed from when it actually began.
		m.toolStartedAt = time.Now()
		name, _ := ev.Data["name"].(string)
		input, _ := ev.Data["input"].(string)
		// Kept for the end event, which has to know this was an edit
		// to draw its diff but carries no name of its own. Several
		// calls can be in flight at once, hence keyed by id rather
		// than one slot.
		if id, _ := ev.Data["tool_use_id"].(string); id != "" {
			if m.pendingTools == nil {
				m.pendingTools = map[string]pendingToolCall{}
			}
			m.pendingTools[id] = pendingToolCall{name: name, input: input}
		}
		m.foldThinking(0)
		m.endModelStream("")
		if arg := summarizeToolInput(input); arg != "" {
			m.appendEntry(entryTool, "▸ "+name+"  "+arg)
		} else {
			m.appendEntry(entryTool, "▸ "+name)
		}
	case events.TypeToolEnd:
		m.runningTool = ""
		m.toolStartedAt = time.Time{}
		// An edit or a created file says what changed, in "- "/"+" rows
		// stored as plain text — no escape sequences, which
		// renderTranscript alone may introduce. Anything else (a bash
		// call, a failed edit, an end nobody's start preceded) adds
		// nothing, the way ends never did.
		if id, _ := ev.Data["tool_use_id"].(string); id != "" {
			if pend, ok := m.pendingTools[id]; ok {
				delete(m.pendingTools, id)
				if isErr, _ := ev.Data["is_error"].(bool); !isErr {
					content, _ := ev.Data["content"].(string)
					if rows := editToolDiff(pend.name, pend.input, content); len(rows) > 0 {
						m.appendEntry(entryTool, renderToolDiff(rows))
					}
				}
			}
		}
	case events.TypePermissionRequest:
		id, _ := ev.Data["id"].(string)
		tool, _ := ev.Data["tool"].(string)
		desc, _ := ev.Data["description"].(string)
		rule, _ := ev.Data["rule"].(string)
		canAlways, _ := ev.Data["can_always"].(bool)
		if desc == "" {
			desc = "(no description given)"
		}
		outside, _ := ev.Data["outside"].(string)
		outsideDir, _ := ev.Data["outside_dir"].(string)
		workspace, _ := ev.Data["workspace"].(string)
		m.enqueuePermission(&pendingPermission{
			id: id, tool: tool, description: desc, rule: rule, canAlways: canAlways,
			outside: outside, outsideDir: outsideDir, workspace: workspace,
		})
	case events.TypePermissionResolved:
		// Both halves of a permission are in the log, and resume replays
		// the log from the start — so without this, every request ever
		// answered in this session came back as a live modal on reopening
		// it. handleEnter then refused every message ("Resolve the
		// permission request above"), and the only way to type anything
		// was to answer a question from days ago, firing a Resolve the
		// broker no longer has a channel for.
		//
		// Matched on id rather than cleared outright: the broker's ids
		// are process-global, so a stale event from an earlier session
		// must not dismiss the request currently on screen. Settled
		// through the queue rather than against the screen alone, so a
		// resolution for a request waiting behind the shown one drops
		// just that one instead of being ignored until replay buries
		// the modal in answered questions.
		if id, _ := ev.Data["id"].(string); id != "" {
			m.settlePermission(id)
		}
	case events.TypeTaskSpawned:
		// No transcript line — background tasks surface in the busy
		// indicator below the prompt box, and /tasks inspects them.
		taskID, _ := ev.Data["task_id"].(string)
		if _, ok := orchestrateStage(taskID); ok {
			// A progress report filed as a birth: the daemon reports
			// stage progress on this channel, and there is no session
			// behind an orchestrate: id to ever open.
			break
		}
		agentName, _ := ev.Data["agent"].(string)
		prompt, _ := ev.Data["prompt"].(string)
		m.tasks[taskID] = taskState{agent: agentName, status: "spawned", prompt: prompt}
	case events.TypeTaskStatus:
		taskID, _ := ev.Data["task_id"].(string)
		status, _ := ev.Data["status"].(string)
		if stage, ok := orchestrateStage(taskID); ok {
			// Stage progress is a transcript line, not a task row.
			// Filing it as a task drew a row with an empty agent and
			// an empty prompt, and /tasks offered output for a
			// session id that does not exist.
			m.appendTool(stageLine(stage, status, ev.Data))
			break
		}
		t := m.tasks[taskID]
		t.status = status
		m.tasks[taskID] = t
	case events.TypeAgentSwitched:
		// Just update the status line the footer already renders every
		// frame — do NOT also write a transcript line here. This event
		// fires on every Tab press/switch, and appending to the
		// (persistent, ever-growing) transcript made each press leave a
		// permanent "switched to X" line on screen forever instead of
		// just updating the one-line status shown below the prompt.
		if name, ok := ev.Data["agent"].(string); ok {
			m.currentAgent = name
		}
		// The conversation's own model choice is kept per agent on the
		// server, so the cached view belonged to the agent just left.
		// Dropped outright: the daemon announces the new agent's view
		// right after this event, which refills it when that agent has
		// its own — and until then the footer falls back to the new
		// agent's profile, which is what the next turn will run on.
		m.model = client.ModelView{}
	case events.TypeModelChanged:
		// The whole answer is in the event, so the footer follows a model
		// chosen in another client without a request of its own.
		m.model = client.ModelView{
			Agent:    strField(ev.Data, "agent"),
			Profile:  strField(ev.Data, "profile"),
			Model:    strField(ev.Data, "model"),
			Provider: strField(ev.Data, "provider"),
			Source:   strField(ev.Data, "source"),
		}
	case events.TypeUsage:
		// Merged rather than replaced: the live estimates broadcast
		// during a stream carry tps alone, and the exact figures arrive
		// at the end. A client that overwrote the whole readout each
		// time would blank the context percentage every few hundred
		// milliseconds while the model was talking.
		if p, ok := ev.Data["percent"].(float64); ok && p > 0 {
			m.usagePercent = p
		}
		if v, ok := ev.Data["tps"].(float64); ok {
			m.tps = v
		}
		if v, ok := ev.Data["show_tps"].(bool); ok {
			m.showTPS = v
		}
	case events.TypeEffortChanged:
		// The whole answer is in the event, so this needs no request of
		// its own — which matters because applyEvent cannot issue one.
		//
		// It arrives when somebody changes the level, here or in another
		// client, and when the agent changes: a new agent is usually a
		// new profile and so a new model, and the level is kept per
		// model. Without this the footer went on naming the level of the
		// model this conversation had just left.
		m.effort = client.EffortView{
			Model:  strField(ev.Data, "model"),
			Agent:  strField(ev.Data, "agent"),
			Level:  strField(ev.Data, "level"),
			Source: strField(ev.Data, "source"),
			Levels: stringsField(ev.Data, "levels"),
			Note:   strField(ev.Data, "note"),
		}
	case events.TypeDelegated:
		if name, ok := ev.Data["agent"].(string); ok {
			m.appendTool(fmt.Sprintf("[delegated to %s]", name))
		}
	case events.TypeThinkingDelta:
		// The status line, and for a muse model a block in the
		// transcript as well. Reasoning is worth knowing about while it
		// happens and is not worth scrolling past afterwards, which is
		// why every other model gets only the status line, and why the
		// muse block folds to one line once the answer starts. See
		// thinking.go.
		m.thinking = true
		if fold, _ := ev.Data["fold"].(bool); fold && showThinkingOf(ev.Data) {
			if text, _ := ev.Data["text"].(string); text != "" {
				m.appendThinkingDelta(text)
			}
		}
	case events.TypeThinkingEnd:
		m.thinking = false
		m.foldThinking(time.Duration(intField(ev.Data, "elapsed_ms")) * time.Millisecond)
	case events.TypeInputRequest:
		id, _ := ev.Data["id"].(string)
		question, _ := ev.Data["question"].(string)
		raw, _ := ev.Data["options"].([]any)
		opts := make([]string, 0, len(raw))
		for _, o := range raw {
			if s, _ := o.(string); s != "" {
				opts = append(opts, s)
			}
		}
		m.asking = &pendingAsk{id: id, question: question, options: opts}
	case events.TypeInputResolved:
		// Same replay problem the permission modal has: both halves are
		// in the log, and resume replays it from the start.
		if id, _ := ev.Data["id"].(string); m.asking != nil && m.asking.id == id {
			m.asking = nil
		}
	case events.TypePlanUpdated:
		// The whole list, every time. A plan is read to see what is left,
		// and a diff of it ("step 3 is now done") answers a different
		// question than the one the person is asking.
		m.appendTool(planLines(ev.Data))
	case events.TypeDebateStarted:
		author := strField(ev.Data, "author")
		// The panel arrives as reviewers/models; the singulars stay as
		// the fallback, because a log written before the plural fields
		// existed replays through this same renderer and must still
		// name its reviewer and model.
		reviewers := stringsField(ev.Data, "reviewers")
		if len(reviewers) == 0 {
			if reviewer := strField(ev.Data, "reviewer"); reviewer != "" {
				reviewers = []string{reviewer}
			}
		}
		models := modelsField(ev.Data, "models")
		fallback := strField(ev.Data, "model")
		names := make([]string, 0, len(reviewers))
		for _, r := range reviewers {
			name := r
			if mdl := models[r]; mdl != "" {
				name += " (" + mdl + ")"
			} else if len(models) == 0 && fallback != "" {
				name += " (" + fallback + ")"
			}
			names = append(names, name)
		}
		rounds := intField(ev.Data, "rounds")
		m.appendTool(fmt.Sprintf("[debate: %s writes, %s reviews, up to %d rounds]", author, strings.Join(names, ", "), rounds))
	case events.TypeDebateReview:
		// The review in full, not a one-line note. It is the half of a
		// debate the person is here for, and it is another model's
		// argument about this session's work — a status line saying it
		// happened would be the least useful summary available.
		reviewer, _ := ev.Data["reviewer"].(string)
		text, _ := ev.Data["text"].(string)
		verdict := "changes requested"
		if approved, _ := ev.Data["approved"].(bool); approved {
			verdict = "approved"
		}
		m.appendTool(fmt.Sprintf("[%s · round %d/%d · %s]",
			reviewer, intField(ev.Data, "round"), intField(ev.Data, "rounds"), verdict))
		if strings.TrimSpace(text) != "" {
			m.appendLocal(text)
		}
	case events.TypeDebateEnded:
		if note, _ := ev.Data["note"].(string); note != "" {
			m.appendTool("[" + note + "]")
		}
		// A collapse replaces the history, as a compaction does. Only a
		// debate that says it had nothing to collapse keeps the reading;
		// one from an older daemon that does not say is let go of, since
		// a blank gauge refills on the next turn and a stale one misleads.
		if collapsed, said := ev.Data["collapsed"].(bool); !said || collapsed {
			m.forgetContextFill()
		}
	case events.TypeDaemonReplaced:
		// The daemon under this TUI is a newer process now, and this
		// stream is about to end; the reconnect lands on it on its own.
		// Said once, because nothing else on screen changes.
		version, _ := ev.Data["version"].(string)
		m.appendTool("[localcode " + version + " took over this address; the next message goes to it]")
	case events.TypeTurnCancelled:
		m.endTurn()
		// The queue went with the turn — turnTracker.cancel drops it — so
		// anything still drawn as sent was never handed to anybody.
		m.abandonPendingUsers()
		m.appendTool("[cancelled]")
	case events.TypeError:
		// A recovered condition is not the end of anything: the loop has
		// already dealt with it and the turn is still running. Ending the
		// turn here stopped the spinner and put a red error on screen for
		// a session that then carried on and answered — which is exactly
		// the "this thing keeps erroring" impression the recovery exists
		// to remove. It goes in the transcript as a note instead.
		if recovered, _ := ev.Data["recovered"].(bool); recovered {
			if msg, ok := ev.Data["error"].(string); ok {
				m.appendTool("[" + msg + "]")
			}
			// A trim that dropped the oldest messages replaced the
			// history the gauge was a reading of.
			if replaced, _ := ev.Data["history_replaced"].(bool); replaced {
				m.forgetContextFill()
			}
			break
		}
		m.endTurn()
		if msg, ok := ev.Data["error"].(string); ok {
			m.errMsg = msg
		} else {
			m.errMsg = "the daemon reported an error with a malformed payload"
		}
	}
	// Only re-render when this event actually wrote to the transcript. The
	// majority of events (tool.start/end, task.*, agent.switched, usage,
	// permission.request) only move the status line or the modal, and
	// re-wrapping every entry for each of those is pure waste on a long
	// session — the footer and the modal are rendered fresh by View() every
	// frame regardless.
	if m.transcriptRev != rev {
		m.refreshViewport()
	}
}

// scheduledHead is the line at the top of a run session's transcript.
//
// It names the booking the way its row does — by name when it has one and
// by id otherwise — and says which run this is out of however many were
// asked for, because "run 3" and "run 3 of 5" are different amounts of
// reassurance when you are looking at a series.
func scheduledHead(data map[string]any) string {
	name, _ := data["name"].(string)
	if name == "" {
		id, _ := data["schedule"].(string)
		name = "scheduled task " + id
	}
	var b strings.Builder
	fmt.Fprintf(&b, "created by %s", name)
	if run := intField(data, "run"); run > 0 {
		if total := intField(data, "run_total"); total > 0 {
			fmt.Fprintf(&b, ", run %d of %d", run, total)
		} else if repeat, _ := data["repeat"].(string); repeat != "" {
			fmt.Fprintf(&b, ", run %d (%s)", run, repeat)
		}
	}
	if at, _ := data["at"].(string); at != "" {
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			fmt.Fprintf(&b, ", %s", t.Local().Format("2006-01-02 15:04"))
		}
	}
	return b.String()
}

// rewoundFiles is the file half of a rewind marker, or "" when that turn
// changed no files through write_file or edit.
//
// Counted rather than listed: the full list went out in the command's own
// reply, which is directly below this line, and repeating it here would
// put the same paths on screen twice.
func rewoundFiles(data map[string]any) string {
	num := func(key string) int {
		v, _ := data[key].(float64)
		return int(v)
	}
	var parts []string
	for _, p := range []struct {
		key, label string
	}{
		{"restored", "restored"},
		{"created", "removed"},
		{"skipped", "left alone"},
	} {
		if n := num(p.key); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, p.label))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, ", ")
}

// redoneFiles is rewoundFiles for the other direction. Its own function
// rather than a shared one taking a table, because the two payloads
// carry different keys and a single function switching on them would be
// longer than both.
func redoneFiles(data map[string]any) string {
	num := func(key string) int {
		switch v := data[key].(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
		return 0
	}
	var parts []string
	if n := num("written"); n > 0 {
		parts = append(parts, fmt.Sprintf("%d written again", n))
	}
	if n := num("skipped"); n > 0 {
		parts = append(parts, fmt.Sprintf("%d left alone", n))
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, ", ")
}

// planLines renders a plan.updated payload as the checklist it is.
//
// The whole list rather than a count: "3 of 7 done" tells the person how
// far along the model thinks it is, and the seven lines tell them whether
// it is doing the right work, which is the question they actually have
// while watching.
func planLines(data map[string]any) string {
	steps, _ := data["plan"].([]any)
	var b strings.Builder
	b.WriteString("[plan]")
	if why, _ := data["explanation"].(string); why != "" {
		b.WriteString(" " + why)
	}
	for _, raw := range steps {
		s, _ := raw.(map[string]any)
		text, _ := s["step"].(string)
		mark := " "
		switch status, _ := s["status"].(string); status {
		case "completed":
			mark = "x"
		case "in_progress":
			mark = ">"
		}
		fmt.Fprintf(&b, "\n  [%s] %s", mark, text)
	}
	return b.String()
}

// forgetContextFill blanks the context percentage. Every event that calls
// it replaces the history the percentage was a reading of, and the daemon
// drops its own count with it and says nothing until the next turn
// reports. A gauge that went on showing the old fill was wrong at the one
// moment somebody was looking at it. The rate stays: it is about the
// model, not about the conversation that was replaced.
func (m *Model) forgetContextFill() {
	m.usagePercent = 0
}
