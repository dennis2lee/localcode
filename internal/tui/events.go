package tui

import (
	"fmt"
	"time"

	"localcode/internal/events"
)

// endTurn clears everything that means "a turn is running". Called
// unconditionally for every turn-terminating event, including an error
// event whose payload turns out to be malformed — previously the "waiting"
// flag was only cleared inside the `if msg, ok := ...; ok` branch of the
// TypeError case, so a malformed error payload left the spinner running
// forever with no way to recover short of restarting the TUI.
func (m *Model) endTurn() {
	m.waiting = false
	m.runningTool = ""
}

func (m *Model) applyEvent(ev events.Event) {
	rev := m.transcriptRev
	switch ev.Type {
	case events.TypeUserMessage:
		if text, ok := ev.Data["text"].(string); ok {
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
		m.endModelStream()
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
		m.endModelStream()
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
		m.pending = &pendingPermission{id: id, tool: tool, description: desc, rule: rule, canAlways: canAlways}
		m.pendingHintShown = false
		m.pendingSince = time.Now()
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
	case events.TypeTurnCancelled:
		m.endTurn()
		m.appendTool("[cancelled]")
	case events.TypeError:
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
