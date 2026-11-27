package tui

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"localcode/internal/client"
	"localcode/internal/events"
)

// A stop rewrites every prompt still drawn as sent as never sent: the
// daemon drops the stopped turn's queue. A prompt that reached the daemon
// as the turn was ending is not in that queue. The daemon starts a turn for
// it once the stopped turn's end is written, so its message.user comes
// after the turn.cancelled, and the line is taken back when it does. The
// Web UI does the same (test/webui/unsent_taken_back.test.js).

const notSentNote = "not sent — the turn was stopped"

func notSentLines(m Model) int {
	n := 0
	for _, e := range m.transcript {
		if strings.Contains(e.text, notSentNote) {
			n++
		}
	}
	return n
}

// userLines is the text of every prompt drawn as its message.user, and of
// every echo still waiting for one, in order.
func userLines(m Model) (users, echoes []string) {
	for _, e := range m.transcript {
		switch e.kind {
		case entryUser:
			users = append(users, e.text)
		case entryPending, entrySent:
			echoes = append(echoes, e.text)
		}
	}
	return users, echoes
}

func userMessage(seq uint64, text string) events.Event {
	return events.Event{Seq: seq, Type: events.TypeUserMessage, Data: map[string]any{"text": text}}
}

func TestAPromptThatStartedTheNextTurnIsNotLeftSayingItWasNeverSent(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "and also check the tests")
	if !strings.Contains(m.transcriptText(), "pick this up at its next step") {
		t.Fatalf("precondition: a mid-turn send is drawn as sent: %q", m.transcriptText())
	}

	m.applyEvent(events.Event{Seq: 20, Type: events.TypeTurnCancelled})
	if notSentLines(m) != 1 {
		t.Fatalf("precondition: the stop marks it as never sent: %q", m.transcriptText())
	}
	m.applyEvent(userMessage(21, "and also check the tests"))

	if n := notSentLines(m); n != 0 {
		t.Errorf("the model was handed the prompt, and %d line(s) still say it never was:\n%s", n, m.transcriptText())
	}
	if users, echoes := userLines(m); len(users) != 1 || users[0] != "and also check the tests" || len(echoes) != 0 {
		t.Errorf("want the prompt drawn once, as its message.user; got users %q, echoes %q", users, echoes)
	}
	text := m.transcriptText()
	if strings.Index(text, "[cancelled]") > strings.Index(text, "and also check the tests") {
		t.Errorf("the prompt is drawn above the stop, not where the log has it:\n%s", text)
	}
}

func TestAPromptWaitingForItsOwnTurnIsTakenBackTheSameWay(t *testing.T) {
	m := newTestModel()
	m, _ = pressEnterWith(t, m, "start over")
	if _, echoes := userLines(m); len(echoes) != 1 {
		t.Fatalf("precondition: drawn as a dimmed prompt: %q", m.transcriptText())
	}
	// Another client's turn, stopped while this prompt was on its way.
	m.applyEvent(events.Event{Seq: 20, Type: events.TypeTurnCancelled})
	if notSentLines(m) != 1 {
		t.Fatalf("precondition: the stop marks it as never sent: %q", m.transcriptText())
	}
	m.applyEvent(userMessage(21, "start over"))
	if n := notSentLines(m); n != 0 {
		t.Errorf("%d line(s) still say the prompt was never sent:\n%s", n, m.transcriptText())
	}
}

func TestAPromptTheStopDroppedStaysNeverSentWhenTheNextTurnIsAnother(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "and also check the tests")
	m.applyEvent(events.Event{Seq: 20, Type: events.TypeTurnCancelled})
	m.applyEvent(userMessage(21, "something else"))
	if n := notSentLines(m); n != 1 {
		t.Errorf("a different prompt took back the line of one nobody was handed (%d left):\n%s", n, m.transcriptText())
	}
}

func TestAPromptSentAgainAfterTheStopAnswersItsOwnEchoFirst(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "run it")
	m.applyEvent(events.Event{Seq: 20, Type: events.TypeTurnCancelled})

	// Seen as never sent, and sent again into the idle session.
	m, _ = pressEnterWith(t, m, "run it")
	m.applyEvent(userMessage(21, "run it"))
	if n := notSentLines(m); n != 1 {
		t.Errorf("the second send's message.user took back the first send's line instead of its own echo (%d left):\n%s", n, m.transcriptText())
	}
	if _, echoes := userLines(m); len(echoes) != 0 {
		t.Errorf("the second send's echo was left above its own line: %q", echoes)
	}
}

func TestOneMessageUserTakesBackOneLineOfTheSameText(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "again")
	m, _ = pressEnterWith(t, m, "again")
	m.applyEvent(events.Event{Seq: 20, Type: events.TypeTurnCancelled})
	if notSentLines(m) != 2 {
		t.Fatalf("precondition: both marked as never sent: %q", m.transcriptText())
	}
	before := entryTexts(m)
	m.applyEvent(userMessage(21, "again"))

	// The second of the two goes, the one sent nearer the stop, and every
	// other line stays as it was.
	last := -1
	for i, text := range before {
		if strings.Contains(text, notSentNote) {
			last = i
		}
	}
	want := append(slices.Delete(slices.Clone(before), last, last+1), "again")
	if got := entryTexts(m); !slices.Equal(got, want) {
		t.Errorf("want one line taken back and the rest kept:\n got %q\nwant %q", got, want)
	}
}

func entryTexts(m Model) []string {
	texts := make([]string, len(m.transcript))
	for i, e := range m.transcript {
		texts[i] = e.text
	}
	return texts
}

// A message with only an image has no text, and neither does any line the
// TUI drew that is not a prompt a stop marked. Such a message.user, from
// the Web UI, takes back nothing.
func TestAMessageWithNoTextTakesBackNoLine(t *testing.T) {
	m := newTestModel()
	m.appendTool("[delegated to explore]")
	m.applyEvent(events.Event{Seq: 5, Type: events.TypeUserMessage, Data: map[string]any{
		"text":   "",
		"images": []any{map[string]any{"media_type": "image/png"}},
	}})
	if !strings.Contains(m.transcriptText(), "[delegated to explore]") {
		t.Errorf("a message.user with no text removed a line that was never a prompt:\n%s", m.transcriptText())
	}
}

func TestTheLineTakenBackIsFromTheStopThePromptRaced(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "run it")
	m.applyEvent(events.Event{Seq: 20, Type: events.TypeTurnCancelled})
	m.applyEvent(userMessage(21, "something else"))

	// The same words, sent into the next turn as it is stopped.
	m.waiting = true
	m, _ = pressEnterWith(t, m, "run it")
	m.applyEvent(events.Event{Seq: 30, Type: events.TypeTurnCancelled})
	m.applyEvent(userMessage(31, "run it"))

	if n := notSentLines(m); n != 1 {
		t.Fatalf("want one line left saying not sent, got %d:\n%s", n, m.transcriptText())
	}
	text := m.transcriptText()
	if strings.Index(text, notSentNote) > strings.Index(text, "[cancelled]") {
		t.Errorf("the line the first stop dropped was taken back, and the one from the stop the prompt raced is left:\n%s", text)
	}
}

func TestALineIsTakenBackOnlyUntilTheNextTurnEnds(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "yes")
	m.applyEvent(events.Event{Seq: 20, Type: events.TypeTurnCancelled})
	// Another client's turn, from start to end.
	m.applyEvent(userMessage(21, "something else"))
	m.applyEvent(events.Event{Seq: 22, Type: events.TypeTurnDone})
	// And the same words from another client after it.
	m.applyEvent(userMessage(23, "yes"))
	if n := notSentLines(m); n != 1 {
		t.Errorf("a message.user after the next turn ended took back a line the stop did drop (%d left):\n%s", n, m.transcriptText())
	}
}

// The TUI's queue sends a prompt a busy 409 put in it again with no echo
// of its own, and the queue drains only once a turn has ended.
func TestABusyRequeuedPromptDoesNotTakeBackALineAnEarlierStopDropped(t *testing.T) {
	busy := &client.StatusError{Status: http.StatusConflict, Message: "409: every session is being deleted; try again in a moment"}

	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "continue")
	m.applyEvent(events.Event{Seq: 20, Type: events.TypeTurnCancelled})

	// Later, the same words sent to the idle session, answered busy.
	m, _ = pressEnterWith(t, m, "continue")
	updated, _ := m.Update(turnDoneMsg{text: "continue", err: busy, startsTurn: true, echo: m.sends})
	m = updated.(Model)
	if len(m.queue) != 1 {
		t.Fatalf("precondition: the busy send is queued: %q", m.queue)
	}
	m.applyEvent(events.Event{Seq: 21, Type: events.TypeTurnDone})
	if cmd := m.dequeue(); cmd == nil {
		t.Fatal("precondition: the queue sent the prompt again")
	}
	m.applyEvent(userMessage(22, "continue"))

	if n := notSentLines(m); n != 1 {
		t.Errorf("the line the stop dropped was taken back by a later send of the same words (%d left):\n%s", n, m.transcriptText())
	}
}

func TestOfTheLinesOneStopRewroteTheOneSentNearestTheStopIsTakenBack(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "again")
	m.applyEvent(events.Event{Seq: 11, Type: events.TypeError, Data: map[string]any{"recovered": true, "error": "retrying the request"}})
	m, _ = pressEnterWith(t, m, "again")
	m.applyEvent(events.Event{Seq: 20, Type: events.TypeTurnCancelled})
	m.applyEvent(userMessage(21, "again"))

	if n := notSentLines(m); n != 1 {
		t.Fatalf("want one line left saying not sent, got %d:\n%s", n, m.transcriptText())
	}
	text := m.transcriptText()
	if strings.Index(text, notSentNote) > strings.Index(text, "retrying the request") {
		t.Errorf("the line sent first was taken back, and the one sent nearest the stop is left:\n%s", text)
	}
}

func TestAStopEndsTheWindowOfTheStopBeforeIt(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "yes")
	m.applyEvent(events.Event{Seq: 20, Type: events.TypeTurnCancelled})
	m.applyEvent(userMessage(21, "something else"))
	m.applyEvent(events.Event{Seq: 22, Type: events.TypeTurnCancelled})
	m.applyEvent(userMessage(23, "yes"))
	if n := notSentLines(m); n != 1 {
		t.Errorf("a message.user after the next stop took back a line the first stop did drop (%d left):\n%s", n, m.transcriptText())
	}
}
