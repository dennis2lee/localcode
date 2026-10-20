package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/events"
)

// The daemon logs a model message as its deltas, a tool.start for each
// call the message asks for, and then the message.part.end with the
// whole text: tool.start comes before the end, because the stream is
// drained before any tool runs (see turn_stream.go). These are the
// events of one such message, in that order.
func delta(text string) events.Event {
	return events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": text}}
}

func partEnd(text string) events.Event {
	return events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": text}}
}

func toolStart(id, name, input string) events.Event {
	return events.Event{Type: events.TypeToolStart, Data: map[string]any{"tool_use_id": id, "name": name, "input": input}}
}

// kinds lists the transcript as kind:text pairs, which is what these
// tests compare: where each line is, not only whether it is there.
func kinds(m Model) []string {
	out := make([]string, len(m.transcript))
	for i, e := range m.transcript {
		name := map[entryKind]string{
			entryUser: "user", entryModel: "model", entryTool: "tool", entryLocal: "local",
			entryPending: "pending", entrySent: "sent", entryThinking: "thinking",
		}[e.kind]
		out[i] = name + ":" + e.text
	}
	return out
}

func wantTranscript(t *testing.T, m Model, want ...string) {
	t.Helper()
	got := kinds(m)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("transcript:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// Reported from real use: every reply that said something before calling
// a tool was drawn twice, once above the tool's line and once below it.
// tool.start closed the reply, and the end that followed it found nothing
// open and drew the whole text again as a new reply.
func TestAReplyBeforeAToolCallIsDrawnOnce(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "how big is it?"}})
	m.applyEvent(delta("Let me check "))
	m.applyEvent(delta("the build logs."))
	m.applyEvent(toolStart("t1", "bash", `{"command":"ls build"}`))
	m.applyEvent(partEnd("Let me check the build logs."))
	m.applyEvent(events.Event{Type: events.TypeToolEnd, Data: map[string]any{"tool_use_id": "t1", "content": "a.log"}})
	m.applyEvent(delta("No build is running."))
	m.applyEvent(toolStart("t2", "ask_user", `{"question":"Which one?"}`))
	m.applyEvent(partEnd("No build is running."))

	wantTranscript(t, m,
		"user:how big is it?",
		"model:Let me check the build logs.",
		"tool:▸ bash  ls build",
		"model:No build is running.",
		"tool:▸ ask_user  Which one?",
	)
}

// The end is the daemon's whole copy of the reply and is written over
// what the deltas drew, wherever the reply now sits: a reconnect can lose
// deltas, and the end is what puts them back. Text a message streams
// after its tool call (a provider may send a text block after a tool_use
// block) belongs to the same reply, above the call, because that is the
// one text the end carries.
func TestTheEndOfAReplyLandsOnItAcrossToolLines(t *testing.T) {
	t.Run("missed deltas", func(t *testing.T) {
		m := newTestModel()
		m.applyEvent(delta("Let me"))
		m.applyEvent(toolStart("t1", "glob", `{"pattern":"*.go"}`))
		m.applyEvent(toolStart("t2", "grep", `{"pattern":"TODO"}`))
		m.applyEvent(partEnd("Let me look at both."))
		wantTranscript(t, m,
			"model:Let me look at both.",
			"tool:▸ glob  *.go",
			"tool:▸ grep  TODO",
		)
	})
	t.Run("text after the call", func(t *testing.T) {
		m := newTestModel()
		m.applyEvent(delta("First. "))
		m.applyEvent(toolStart("t1", "glob", `{"pattern":"*.go"}`))
		m.applyEvent(delta("Second."))
		m.applyEvent(partEnd("First. Second."))
		wantTranscript(t, m,
			"model:First. Second.",
			"tool:▸ glob  *.go",
		)
	})
	t.Run("a call with no text", func(t *testing.T) {
		m := newTestModel()
		m.applyEvent(toolStart("t1", "glob", `{"pattern":"*.go"}`))
		m.applyEvent(partEnd(""))
		m.applyEvent(delta("Found it."))
		m.applyEvent(partEnd("Found it."))
		wantTranscript(t, m,
			"tool:▸ glob  *.go",
			"model:Found it.",
		)
	})
	t.Run("a replay", func(t *testing.T) {
		// The daemon replays a finished reply that a tool call followed
		// as one delta in the reply's place; see collapseFinishedDeltas.
		m := newTestModel()
		m.applyEvent(delta("Let me look."))
		m.applyEvent(toolStart("t1", "glob", `{"pattern":"*.go"}`))
		m.applyEvent(partEnd("Let me look."))
		wantTranscript(t, m,
			"model:Let me look.",
			"tool:▸ glob  *.go",
		)
	})
}

// Lines this client writes while a reply streams go below it, and the
// reply keeps growing above them. Each of these closed the reply before,
// and its end then drew the reply a second time: a message sent into the
// running turn, a command refused mid-turn, a note from the daemon, a
// Ctrl+O note.
func TestLinesWrittenMidReplyLeaveItOpen(t *testing.T) {
	t.Run("a message sent into the turn", func(t *testing.T) {
		m := newTestModel()
		m.waiting = true
		m.applyEvent(delta("Working "))
		m, _ = pressEnterWith(t, m, "also check the tests")
		m.applyEvent(delta("on it."))
		m.applyEvent(partEnd("Working on it."))
		wantTranscript(t, m,
			"model:Working on it.",
			"sent:[sent — the model will pick this up at its next step] also check the tests",
		)
	})
	t.Run("a command refused mid-turn", func(t *testing.T) {
		m := newTestModel()
		m.waiting = true
		m.applyEvent(delta("Working "))
		m, _ = pressEnterWith(t, m, "/compact")
		m.applyEvent(delta("on it."))
		m.applyEvent(partEnd("Working on it."))
		got := kinds(m)
		if len(got) != 2 || got[0] != "model:Working on it." || !strings.HasPrefix(got[1], "local:/compact can't run") {
			t.Errorf("transcript:\n  %s", strings.Join(got, "\n  "))
		}
	})
	t.Run("a status line from the daemon", func(t *testing.T) {
		m := newTestModel()
		m.applyEvent(delta("Working "))
		m.applyEvent(events.Event{Type: events.TypeError, Data: map[string]any{"error": "the endpoint is slow; still waiting", "recovered": true}})
		m.applyEvent(delta("on it."))
		m.applyEvent(partEnd("Working on it."))
		wantTranscript(t, m,
			"model:Working on it.",
			"tool:[the endpoint is slow; still waiting]",
		)
	})
	t.Run("Ctrl+O with nothing to open", func(t *testing.T) {
		m := newTestModel()
		m.applyEvent(delta("Working "))
		next, _, _ := m.handleKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
		m = next.(Model)
		m.applyEvent(delta("on it."))
		m.applyEvent(partEnd("Working on it."))
		got := kinds(m)
		if len(got) != 2 || got[0] != "model:Working on it." || !strings.HasPrefix(got[1], "local:No folded reasoning") {
			t.Errorf("transcript:\n  %s", strings.Join(got, "\n  "))
		}
	})
}

// A reasoning block that opens after the answer has started goes in
// front of the answer, and stays in front of it when a tool line has
// been written below the answer since.
func TestReasoningAfterAToolLineGoesInFrontOfTheReply(t *testing.T) {
	m := newTestModel()
	m.applyEvent(delta("Answer"))
	m.applyEvent(toolStart("t1", "glob", `{"pattern":"*.go"}`))
	m.applyEvent(thinkingDelta("late thought", true))
	m.applyEvent(partEnd("Answer."))
	got := kinds(m)
	if len(got) != 3 || !strings.HasPrefix(got[0], "thinking:late thought") || got[1] != "model:Answer." || got[2] != "tool:▸ glob  *.go" {
		t.Errorf("transcript:\n  %s", strings.Join(got, "\n  "))
	}
}

// A reply whose end never came (a log written before the daemon always
// closed one, or a daemon that died mid-stream) is closed by the turn's
// end or by the next message to the model, and the next reply is a reply
// of its own.
func TestATurnsEndClosesAReplyLeftOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   events.Event
		want []string
	}{
		{"turn.done", events.Event{Type: events.TypeTurnDone}, nil},
		{"a prompt", events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "next"}}, []string{"user:next"}},
		{"an auto message", events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "carry on", "auto": true}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.applyEvent(delta("cut off"))
			m.applyEvent(tc.ev)
			m.applyEvent(delta("next answer"))
			m.applyEvent(partEnd("next answer"))
			want := append(append([]string{"model:cut off"}, tc.want...), "model:next answer")
			wantTranscript(t, m, want...)
		})
	}
}

// The reply is found by its index, so a line taken out above it moves it
// up one, and the end still lands on the reply rather than on whatever
// slid into its old place.
func TestALineRemovedAboveAnOpenReplyMovesItUp(t *testing.T) {
	m := newTestModel()
	m.appendPendingUser("first")
	m.applyEvent(delta("Work"))
	m.appendEntry(entryTool, "▸ glob")
	m.resolvePendingUser("first")
	m.applyEvent(delta("ing."))
	m.applyEvent(partEnd("Working."))
	wantTranscript(t, m,
		"model:Working.",
		"tool:▸ glob",
	)

	// And one taken out below it leaves it where it is.
	m = newTestModel()
	m.applyEvent(delta("Work"))
	m.appendPendingUser("second")
	m.resolvePendingUser("second")
	m.applyEvent(delta("ing."))
	m.applyEvent(partEnd("Working."))
	wantTranscript(t, m, "model:Working.")
}

// The end corrects what missed deltas drew, and the screen shows the
// correction when it arrives, not at the next event after it.
func TestAnEndThatCorrectsAReplyRedrawsIt(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	m.applyEvent(delta("Let me"))
	m.applyEvent(toolStart("t1", "glob", `{"pattern":"*.go"}`))
	m.applyEvent(partEnd("Let me look at both."))
	if view := stripTestANSI(m.viewport.View()); !strings.Contains(view, "Let me look at both.") {
		t.Errorf("the screen still shows the reply as the deltas left it:\n%s", view)
	}
}
