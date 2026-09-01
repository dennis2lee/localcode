package tui

import (
	"fmt"
	"strings"
	"time"

	"localcode/internal/events"
)

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

// endTurn clears everything that means "a turn is running". Called
// unconditionally for every turn-terminating event, including an error
// event whose payload turns out to be malformed — previously the "waiting"
// flag was only cleared inside the `if msg, ok := ...; ok` branch of the
// TypeError case, so a malformed error payload left the spinner running
// forever with no way to recover short of restarting the TUI.
func (m *Model) endTurn() {
	m.waiting = false
	m.runningTool = ""
	m.thinking = false
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
		if text, ok := ev.Data["text"].(string); ok {
			// Every prompt this session has seen goes into Up/Down recall,
			// whoever typed it and whenever — which is what gives a session
			// the TUI has just attached to a recall list at all.
			m.recordHistory(text)
			m.appendUser(text)
		}
	case events.TypeMessagePartDelta:
		if text, ok := ev.Data["text"].(string); ok {
			m.appendModelDelta(text)
		}
	case events.TypeMessagePartEnd:
		// One model message ended — NOT the turn. A turn with tool calls
		// streams several of these (text, then the post-tool follow-up),
		// and treating the first as end-of-turn is what used to make a
		// prompt typed during tool execution skip the queue and 409.
		text, _ := ev.Data["text"].(string)
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
	case events.TypeRewound:
		what, _ := ev.Data["turn_text"].(string)
		if what != "" {
			what = ": " + what
		}
		m.appendTool("[rewound one turn" + what + rewoundFiles(ev.Data) + "]")
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
		name, _ := ev.Data["name"].(string)
		input, _ := ev.Data["input"].(string)
		m.endModelStream("")
		if arg := summarizeToolInput(input); arg != "" {
			m.appendEntry(entryTool, "▸ "+name+"  "+arg)
		} else {
			m.appendEntry(entryTool, "▸ "+name)
		}
	case events.TypeToolEnd:
		m.runningTool = ""
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
		m.pending = &pendingPermission{
			id: id, tool: tool, description: desc, rule: rule, canAlways: canAlways,
			outside: outside, outsideDir: outsideDir, workspace: workspace,
		}
		m.pendingHintShown = false
		m.pendingSince = time.Now()
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
		// must not dismiss the request currently on screen.
		if id, _ := ev.Data["id"].(string); m.pending != nil && m.pending.id == id {
			m.pending = nil
			m.pendingHintShown = false
		}
	case events.TypeTaskSpawned:
		// No transcript line — background tasks surface in the busy
		// indicator below the prompt box, and /tasks inspects them.
		taskID, _ := ev.Data["task_id"].(string)
		agentName, _ := ev.Data["agent"].(string)
		prompt, _ := ev.Data["prompt"].(string)
		m.tasks[taskID] = taskState{agent: agentName, status: "spawned", prompt: prompt}
	case events.TypeTaskStatus:
		taskID, _ := ev.Data["task_id"].(string)
		status, _ := ev.Data["status"].(string)
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
	case events.TypeDelegated:
		if name, ok := ev.Data["agent"].(string); ok {
			m.appendTool(fmt.Sprintf("[delegated to %s]", name))
		}
	case events.TypeThinkingDelta:
		// The status line, not the transcript. Reasoning is worth knowing
		// about while it happens and is not worth scrolling past
		// afterwards, and the TUI's transcript is the part that keeps.
		m.thinking = true
	case events.TypeThinkingEnd:
		m.thinking = false
	case events.TypeDebateStarted:
		author, _ := ev.Data["author"].(string)
		reviewer, _ := ev.Data["reviewer"].(string)
		model, _ := ev.Data["model"].(string)
		rounds := intField(ev.Data, "rounds")
		m.appendTool(fmt.Sprintf("[debate: %s writes, %s (%s) reviews, up to %d rounds]", author, reviewer, model, rounds))
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
	case events.TypeTurnCancelled:
		m.endTurn()
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
