package tui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

// daemonTransport answers GET /api/sessions with one session and a busy
// flag, and GET /api/settings with show_thinking when set; everything else
// is 404.
type daemonTransport struct {
	busy         bool
	showThinking *bool
	// asked counts the session-list requests, when set.
	asked *int
}

func (dt daemonTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body any
	switch req.URL.Path {
	case "/api/sessions":
		if dt.asked != nil {
			*dt.asked++
		}
		body = []map[string]any{{"id": "s1", "busy": dt.busy}}
	case "/api/settings":
		s := map[string]any{"show_tps": true}
		if dt.showThinking != nil {
			s["show_thinking"] = *dt.showThinking
		}
		body = s
	default:
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}, nil
	}
	raw, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(raw)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func daemonModel(t *testing.T, dt daemonTransport) Model {
	t.Helper()
	prev := lostTurnGrace
	lostTurnGrace = 0
	t.Cleanup(func() { lostTurnGrace = prev })
	ch := make(chan events.Event)
	close(ch)
	c := client.New("http://daemon.invalid")
	c.HTTP = &http.Client{Transport: dt}
	return New(c, "s1", "general-purpose", ch, false)
}

// feed runs msg through Update and then every message its commands
// produce, depth first, until none is left, and returns the final model.
// Messages that would only re-arm a stream or refresh a list are dropped.
func feed(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	queue := []tea.Msg{msg}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 50 {
			t.Fatal("the lost-turn check did not settle")
		}
		next := queue[0]
		queue = queue[1:]
		switch next.(type) {
		case streamEndedMsg, agentsMsg, spinTickMsg:
			continue
		}
		updated, cmd := m.Update(next)
		m = updated.(Model)
		queue = append(queue, runCmd(cmd)...)
	}
	return m
}

func reconnected(m Model) eventMsg {
	return eventMsg{ev: events.Event{Type: client.TypeReconnected}, gen: m.streamGen}
}

const lostLine = "the turn did not finish"

// A turn the daemon is not running, after the stream came back: the wait
// ends, the reasoning block folds, what was sent into the turn is marked
// as never handed over, an open question is put away, and a line says so.
func TestATurnLostAcrossAReconnectIsEnded(t *testing.T) {
	m := daemonModel(t, daemonTransport{busy: false})
	m.waiting = true
	m.turnEpoch = 1
	m.applyEvent(thinkingDelta("mid-thought when the daemon died", true))
	m.appendSent("[sent — the model will pick this up at its next step] also this")
	m.asking = &pendingAsk{id: "q1", question: "which one?"}

	m = feed(t, m, reconnected(m))

	if m.waiting {
		t.Error("still waiting on a turn the daemon is not running")
	}
	if m.liveThinking() >= 0 {
		t.Error("the reasoning block is still live")
	}
	if m.asking != nil {
		t.Error("the lost turn's question is still open")
	}
	text := m.transcriptText()
	if strings.Count(text, lostLine) != 1 {
		t.Errorf("want one lost-turn line:\n%s", text)
	}
	if !strings.Contains(text, "[not sent") {
		t.Errorf("what was sent into the lost turn still reads as sent:\n%s", text)
	}
}

// A turn the daemon is still running is left alone: the reconnect was a
// blip it survived.
func TestABusyTurnSurvivesAReconnect(t *testing.T) {
	m := daemonModel(t, daemonTransport{busy: true})
	m.waiting = true
	m = feed(t, m, reconnected(m))
	if !m.waiting || strings.Contains(m.transcriptText(), lostLine) {
		t.Errorf("a running turn was declared lost: waiting=%v\n%s", m.waiting, m.transcriptText())
	}
}

// Nothing in progress, nothing asked.
func TestAnIdleClientAsksNothingOnAReconnect(t *testing.T) {
	m := daemonModel(t, daemonTransport{busy: false})
	updated, cmd := m.Update(reconnected(m))
	m = updated.(Model)
	for _, msg := range runCmd(cmd) {
		if _, ok := msg.(lostTurnDueMsg); ok {
			t.Fatal("a check was scheduled with no turn in progress")
		}
	}
}

// Each step checks nothing has moved since it was scheduled: a turn.done
// from the backlog, a prompt sent in the meantime, or a switch to another
// conversation all leave the turn alone.
func TestALostTurnCheckStandsDownWhenSomethingMoved(t *testing.T) {
	for name, move := range map[string]func(m *Model){
		"turn.done arrived": func(m *Model) { m.applyEvent(events.Event{Type: events.TypeTurnDone}) },
		"a prompt was sent": func(m *Model) { m.turnEpoch++ },
		"the stream moved":  func(m *Model) { m.streamGen++ },
		"another turn began": func(m *Model) {
			m.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "from another client"}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := daemonModel(t, daemonTransport{busy: false})
			m.waiting = true
			updated, cmd := m.Update(reconnected(m))
			m = updated.(Model)
			var due []tea.Msg
			for _, msg := range runCmd(cmd) {
				if d, ok := msg.(lostTurnDueMsg); ok {
					due = append(due, d)
				}
			}
			if len(due) != 1 {
				t.Fatalf("scheduled %d checks, want 1", len(due))
			}
			// The first step asks; the answer is idle; then something moves
			// before the confirm step runs.
			updated, cmd = m.Update(due[0])
			m = updated.(Model)
			answers := runCmd(cmd)
			if len(answers) != 1 {
				t.Fatalf("the first step issued %d commands", len(answers))
			}
			updated, cmd = m.Update(answers[0])
			m = updated.(Model)
			confirms := runCmd(cmd)
			waitingBefore := m.waiting
			move(&m)
			for _, c := range confirms {
				m = feed(t, m, c)
			}
			if strings.Contains(m.transcriptText(), lostLine) {
				t.Errorf("declared lost after %s:\n%s", name, m.transcriptText())
			}
			if name == "a prompt was sent" && m.waiting != waitingBefore {
				t.Error("the turn the new prompt began was ended")
			}
		})
	}
}

// A turn this client was only watching: its reasoning block folds, and
// nothing is said, since it was not this client's turn.
func TestAWatchedTurnLostAcrossAReconnectFoldsItsBlockQuietly(t *testing.T) {
	m := daemonModel(t, daemonTransport{busy: false})
	m.applyEvent(thinkingDelta("another client's turn", true))
	m = feed(t, m, reconnected(m))
	if m.liveThinking() >= 0 {
		t.Error("the watched block is still live")
	}
	if strings.Contains(m.transcriptText(), lostLine) {
		t.Errorf("a turn this client did not start was announced as lost:\n%s", m.transcriptText())
	}
}

// Re-attaching after a failed switch is the same moment: the daemon may
// be a new process, so a turn still in progress is checked.
func TestReattachingChecksATurnInProgress(t *testing.T) {
	m := daemonModel(t, daemonTransport{busy: false})
	m.waiting = true
	ch := make(chan events.Event)
	close(ch)
	m = feed(t, m, sessionSwitchedMsg{sessionID: "s1", agent: "general-purpose", events: ch, gen: m.streamGen, reattach: true})
	if m.waiting || !strings.Contains(m.transcriptText(), lostLine) {
		t.Errorf("a re-attach did not find the lost turn: waiting=%v\n%s", m.waiting, m.transcriptText())
	}
}

// The logged block, live: it folds the block the deltas drew and takes
// the record's text, which is whole where the deltas missed some.
func TestALoggedBlockFoldsTheLiveOne(t *testing.T) {
	m := newTestModel()
	m.applyEvent(thinkingDelta("the first half, then", true))
	m.applyEvent(events.Event{Type: events.TypeThinkingBlock, Data: map[string]any{
		"text": "the first half, then the second half", "elapsed_ms": float64(4200),
	}})
	m.applyEvent(events.Event{Type: events.TypeThinkingEnd, Data: map[string]any{"fold": true, "elapsed_ms": float64(4200)}})
	b := m.thinkingEntries()
	if len(b) != 1 || b[0].live || b[0].text != "the first half, then the second half" || b[0].note != "Thought for 4s" {
		t.Errorf("blocks = %#v", b)
	}
	if m.thinking {
		t.Error("the busy line still says thinking")
	}
}

// The logged block, replayed: no block is streaming, so it is drawn
// folded, in front of an answer already started, open when Ctrl+O left
// blocks open, and not at all while show_thinking is off.
func TestALoggedBlockIsDrawnOnAReplay(t *testing.T) {
	block := events.Event{Type: events.TypeThinkingBlock, Data: map[string]any{"text": "kept", "elapsed_ms": float64(65000)}}

	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "q"}})
	m.applyEvent(block)
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "the answer"}})
	b := m.thinkingEntries()
	if len(b) != 1 || b[0].live || b[0].open || b[0].note != "Thought for 1m5s" {
		t.Fatalf("blocks = %#v", b)
	}
	out := stripTestANSI(renderTranscript(m.transcript, 80))
	if strings.Index(out, "Thought for") > strings.Index(out, "the answer") {
		t.Errorf("the block is not in front of the answer:\n%s", out)
	}

	m = newTestModel()
	m.applyEvent(events.Event{Type: events.TypeMessagePartDelta, Data: map[string]any{"text": "started "}})
	m.applyEvent(block)
	if last := m.transcript[len(m.transcript)-1]; last.kind != entryModel {
		t.Errorf("a replayed block went after the open answer: %#v", m.transcript)
	}

	m = newTestModel()
	m.thinkingExpanded = true
	m.applyEvent(block)
	if b := m.thinkingEntries(); len(b) != 1 || !b[0].open {
		t.Errorf("a replayed block ignored Ctrl+O's choice: %#v", b)
	}

	m = newTestModel()
	m.hideThinking = true
	m.applyEvent(block)
	if b := m.thinkingEntries(); len(b) != 0 {
		t.Errorf("a replayed block was drawn with show_thinking off: %#v", b)
	}

	m = newTestModel()
	m.applyEvent(events.Event{Type: events.TypeThinkingBlock, Data: map[string]any{"text": " \n "}})
	if b := m.thinkingEntries(); len(b) != 0 {
		t.Errorf("whitespace was drawn: %#v", b)
	}
}

// This client's copy of show_thinking: the daemon's answer at start, the
// settings broadcast, a delta, and a switch each set it; a daemon that
// does not say leaves the default, shown.
func TestTheTerminalKeepsShowThinking(t *testing.T) {
	off, on := false, true
	m := newTestModel()
	updated, _ := m.Update(settingsMsg{settings: client.Settings{ShowThinking: &off}})
	m = updated.(Model)
	if !m.hideThinking {
		t.Error("the answer at start was not applied")
	}
	updated, _ = m.Update(settingsMsg{settings: client.Settings{}})
	m = updated.(Model)
	if !m.hideThinking {
		t.Error("an answer without the key changed the switch")
	}
	m.applyEvent(events.Event{Type: events.TypeSettingsChanged, Data: map[string]any{"show_thinking": true}})
	if m.hideThinking {
		t.Error("settings.changed was not applied")
	}
	m.applyEvent(events.Event{Type: events.TypeThinkingDelta, Data: map[string]any{"text": "x", "fold": true, "show_thinking": false}})
	if !m.hideThinking {
		t.Error("the delta's switch was not applied")
	}
	ch := make(chan events.Event)
	close(ch)
	updated, _ = m.Update(sessionSwitchedMsg{sessionID: "s2", agent: "general-purpose", events: ch, gen: m.streamGen, showThinking: &on})
	m = updated.(Model)
	if m.hideThinking {
		t.Error("the switch's reading was not applied")
	}
}

// The first replay waits for the settings, so a block in it is drawn
// under the daemon's switch rather than the default.
func TestTheSettingsAreReadBeforeTheFirstEvent(t *testing.T) {
	off := false
	m := daemonModel(t, daemonTransport{showThinking: &off})
	for _, msg := range runCmd(m.Init()) {
		if _, ok := msg.(settingsMsg); ok {
			t.Fatal("the settings were read beside the stream rather than before it")
		}
		// A sequence is bubbletea's own unexported slice of commands;
		// it is recognised by shape.
		v := reflect.ValueOf(msg)
		if v.Kind() != reflect.Slice || v.Type().Elem() != reflect.TypeOf(tea.Cmd(nil)) || v.Len() == 0 {
			continue
		}
		if _, isBatch := msg.(tea.BatchMsg); isBatch {
			continue
		}
		first := v.Index(0).Interface().(tea.Cmd)()
		if s, ok := first.(settingsMsg); !ok || s.settings.ShowThinking == nil || *s.settings.ShowThinking {
			t.Fatalf("the sequence does not start with the settings: %#v", first)
		}
		return
	}
	t.Fatal("Init reads no settings")
}

// A turn that ended before the check came due is not asked about: the
// daemon is not asked a question whose answer could change nothing.
func TestATurnThatEndedIsNotAskedAbout(t *testing.T) {
	asked := 0
	m := daemonModel(t, daemonTransport{busy: false, asked: &asked})
	m.waiting = true
	updated, cmd := m.Update(reconnected(m))
	m = updated.(Model)
	var due tea.Msg
	for _, msg := range runCmd(cmd) {
		if d, ok := msg.(lostTurnDueMsg); ok {
			due = d
		}
	}
	if due == nil {
		t.Fatal("no check was scheduled")
	}
	m.applyEvent(events.Event{Type: events.TypeTurnDone})
	m = feed(t, m, due)
	if asked != 0 {
		t.Errorf("asked the daemon %d times about a turn that had ended", asked)
	}
}

// The backlog a reconnect replays can hold the lost turn's own prompt,
// logged just before the daemon went away. That is not a new turn, and
// the check still finds the turn lost.
func TestTheLostTurnsOwnPromptInTheBacklogDoesNotStopTheCheck(t *testing.T) {
	m := daemonModel(t, daemonTransport{busy: false})
	m.waiting = true
	m.turnEpoch = 1
	updated, cmd := m.Update(reconnected(m))
	m = updated.(Model)
	var due tea.Msg
	for _, msg := range runCmd(cmd) {
		if d, ok := msg.(lostTurnDueMsg); ok {
			due = d
		}
	}
	if due == nil {
		t.Fatal("no check was scheduled")
	}
	m.applyEvent(events.Event{Seq: 9, Type: events.TypeUserMessage, Data: map[string]any{"text": "the lost turn's prompt"}})
	m = feed(t, m, due)
	if m.waiting || !strings.Contains(m.transcriptText(), lostLine) {
		t.Errorf("the replayed prompt stopped the check: waiting=%v\n%s", m.waiting, m.transcriptText())
	}
}

// A queued prompt goes with a lost turn the way it goes with a stop:
// marked as not sent, and not sent behind the reader's back.
func TestALostTurnDropsItsQueue(t *testing.T) {
	m := daemonModel(t, daemonTransport{busy: false})
	m.waiting = true
	m.queue = []string{"queued after a 409"}
	m.appendLocal("[queued] queued after a 409")
	m = feed(t, m, reconnected(m))
	if len(m.queue) != 0 {
		t.Errorf("the queue survived the lost turn: %v", m.queue)
	}
}

// A line sent into a running turn gives way to the message's own line
// when the model is given it, so a later stop cannot call it unseen.
func TestASentLineResolvesWhenTheModelGetsIt(t *testing.T) {
	m := newTestModel()
	m.appendSent("[sent — the model will pick this up at its next step] also check the tests")
	m.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "also check the tests"}})
	m.abandonPendingUsers()
	text := m.transcriptText()
	if strings.Contains(text, "[sent") || strings.Contains(text, "[not sent") {
		t.Errorf("a message the model was given is still drawn as sent or as not sent:\n%s", text)
	}
	if strings.Count(text, "also check the tests") != 1 {
		t.Errorf("the message is drawn %d times:\n%s", strings.Count(text, "also check the tests"), text)
	}
}

// A page that connects just as a block ends gets the logged block and
// then that block's last deltas: they do not open it a second time.
func TestABlocksLateDeltasAfterItsLoggedCopyAreDropped(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Seq: 4, Type: events.TypeThinkingBlock, Data: map[string]any{"text": "the whole block", "elapsed_ms": float64(2000)}})
	m.applyEvent(thinkingDelta("block", true))
	m.applyEvent(events.Event{Type: events.TypeThinkingEnd, Data: map[string]any{"fold": true, "elapsed_ms": float64(2000)}})
	if b := m.thinkingEntries(); len(b) != 1 {
		t.Fatalf("blocks = %#v, want the logged one alone", b)
	}
	// The next block streams normally.
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "answer"}})
	m.applyEvent(thinkingDelta("the next request", true))
	if b := m.thinkingEntries(); len(b) != 2 || !b[1].live {
		t.Errorf("the next block did not stream: %#v", b)
	}
}
