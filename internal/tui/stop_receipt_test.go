package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/client"
	"localcode/internal/events"
)

// A stop rewrites a prompt still drawn as sent as never sent: the daemon
// drops the stopped turn's queue. The 202 for a send carries the log's
// last seq when the daemon took it, and only a stop with a larger seq can
// have dropped it. A prompt taken at or after the stop's seq started the
// next turn, or went to it, and its message.user resolves the echo. The
// Web UI does the same (test/webui/stop_receipt.test.js).

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

func stopAt(seq uint64) events.Event {
	return events.Event{Seq: seq, Type: events.TypeTurnCancelled}
}

// taken delivers the daemon's 202 for the send numbered send, taken when
// its log's last seq was seq.
func taken(t *testing.T, m Model, send int, seq uint64) Model {
	t.Helper()
	updated, _ := m.Update(turnDoneMsg{echo: send, receipt: client.Receipt{Status: "accepted", Seq: seq, HasSeq: true}})
	return updated.(Model)
}

func TestAPromptTheDaemonTookAtTheStopIsNotMarkedAsNeverSent(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "and also check the tests")
	// Taken once the stop at 20 was written: begin waits for it.
	m = taken(t, m, m.sends, 20)

	m.applyEvent(stopAt(20))
	if n := notSentLines(m); n != 0 {
		t.Fatalf("a prompt the daemon took after the stop is marked as never sent:\n%s", m.transcriptText())
	}
	m.applyEvent(userMessage(21, "and also check the tests"))

	if users, echoes := userLines(m); len(users) != 1 || users[0] != "and also check the tests" || len(echoes) != 0 {
		t.Errorf("want the prompt drawn once, as its message.user; got users %q, echoes %q", users, echoes)
	}
	text := m.transcriptText()
	if strings.Index(text, "[cancelled]") > strings.Index(text, "and also check the tests") {
		t.Errorf("the prompt is drawn above the stop, not where the log has it:\n%s", text)
	}
}

func TestAPromptTheDaemonTookBeforeTheStopIsMarkedAsNeverSent(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "and also check the tests")
	m = taken(t, m, m.sends, 19)

	m.applyEvent(stopAt(20))
	if n := notSentLines(m); n != 1 {
		t.Errorf("a prompt in the queue the stop dropped is not marked as never sent:\n%s", m.transcriptText())
	}
}

func TestAStopBeforeTheAnswerWaitsForIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		at      uint64
		notSent int
	}{
		{"taken after the stop", 20, 0},
		{"taken before the stop", 19, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.waiting = true
			m, _ = pressEnterWith(t, m, "run it")
			send := m.sends

			m.applyEvent(stopAt(20))
			if n := notSentLines(m); n != 0 {
				t.Fatalf("decided before the daemon said when it took the prompt:\n%s", m.transcriptText())
			}
			m = taken(t, m, send, tc.at)
			if n := notSentLines(m); n != tc.notSent {
				t.Errorf("want %d line(s) saying not sent once the answer came, got %d:\n%s", tc.notSent, n, m.transcriptText())
			}
		})
	}
}

// The stream fell behind while two prompts were sent and taken; the
// backlog brings a stop from before either was taken.
func TestAStopReplayedFromABackedUpStreamDoesNotMarkPromptsTakenAfterIt(t *testing.T) {
	m := newTestModel()
	m, _ = pressEnterWith(t, m, "first")
	m = taken(t, m, m.sends, 30)
	m.waiting = true
	m, _ = pressEnterWith(t, m, "second")
	m = taken(t, m, m.sends, 30)

	m.applyEvent(stopAt(20))
	m.applyEvent(userMessage(31, "first"))
	m.applyEvent(events.Event{Seq: 40, Type: events.TypeTurnDone})
	m.applyEvent(userMessage(41, "second"))

	if n := notSentLines(m); n != 0 {
		t.Errorf("a replayed stop marked prompts the daemon took after it:\n%s", m.transcriptText())
	}
	if users, echoes := userLines(m); len(users) != 2 || len(echoes) != 0 {
		t.Errorf("want both prompts drawn as their message.user; got users %q, echoes %q", users, echoes)
	}
}

// Another client stops the turn and runs a short one of its own before
// this client's prompt reaches the daemon.
func TestAnotherClientsShortTurnBetweenTheStopAndThePromptDoesNotMarkIt(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "run it")
	send := m.sends

	m.applyEvent(stopAt(20))
	m.applyEvent(userMessage(21, "what time is it"))
	m.applyEvent(events.Event{Seq: 22, Type: events.TypeTurnDone})
	m = taken(t, m, send, 22)
	m.applyEvent(userMessage(23, "run it"))

	if n := notSentLines(m); n != 0 {
		t.Errorf("a prompt the daemon started a turn for is marked as never sent:\n%s", m.transcriptText())
	}
	if _, echoes := userLines(m); len(echoes) != 0 {
		t.Errorf("an echo is left: %q", echoes)
	}
}

func TestAStopBeforeAnAnswerWithNoSeqMarksThePromptOnceItComes(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "run it")
	send := m.sends
	m.applyEvent(stopAt(20))

	updated, _ := m.Update(turnDoneMsg{echo: send, receipt: client.Receipt{Status: "injected"}})
	m = updated.(Model)
	if n := notSentLines(m); n != 1 {
		t.Errorf("an answer that says nothing of when the prompt was taken kept it drawn as sent through the stop:\n%s", m.transcriptText())
	}
}

func TestAnAnswerWithNoSeqLeavesAStopMarkingThePrompt(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "run it")
	updated, _ := m.Update(turnDoneMsg{echo: m.sends, receipt: client.Receipt{Status: "injected"}})
	m = updated.(Model)

	m.applyEvent(stopAt(20))
	if n := notSentLines(m); n != 1 {
		t.Errorf("an older daemon's answer, with no seq, kept the prompt drawn as sent through a stop:\n%s", m.transcriptText())
	}
}

func TestAStopWithNoSeqMarksEveryPrompt(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "taken")
	m = taken(t, m, m.sends, 50)
	m, _ = pressEnterWith(t, m, "awaited")

	m.applyEvent(events.Event{Type: events.TypeTurnCancelled})
	if n := notSentLines(m); n != 2 {
		t.Errorf("want both prompts marked by a stop with nothing to compare against, got %d:\n%s", n, m.transcriptText())
	}
}

func TestALostTurnMarksEveryPrompt(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "taken")
	m = taken(t, m, m.sends, 50)
	m, _ = pressEnterWith(t, m, "awaited")

	m.endLostTurn()
	if n := notSentLines(m); n != 2 {
		t.Errorf("want both prompts marked when the turn is lost, got %d:\n%s", n, m.transcriptText())
	}
}

// Another client's message with the same words, logged before this one
// was taken, comes in a backlog after the answer. It is not this send's.
func TestAMessageFromBeforeTheSendWasTakenDoesNotStandForIt(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "yes")
	m = taken(t, m, m.sends, 100)

	m.applyEvent(userMessage(100, "yes"))
	if _, echoes := userLines(m); len(echoes) != 1 {
		t.Fatalf("an older message with the same words resolved the echo: %q", m.transcriptText())
	}
	m.applyEvent(stopAt(101))
	if n := notSentLines(m); n != 1 {
		t.Errorf("the stop dropped the send, and nothing says so:\n%s", m.transcriptText())
	}
}

func TestAMessageAfterTheSendWasTakenStandsForIt(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "yes")
	m = taken(t, m, m.sends, 100)

	m.applyEvent(userMessage(101, "yes"))
	if users, echoes := userLines(m); len(users) != 1 || len(echoes) != 0 {
		t.Errorf("want the echo resolved by its message.user; got users %q, echoes %q", users, echoes)
	}
}

func TestALateAnswerForAnEchoAlreadyResolvedChangesNothing(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "run it")
	send := m.sends
	m.applyEvent(userMessage(21, "run it"))
	m.applyEvent(stopAt(22))
	before := m.transcriptText()

	m = taken(t, m, send, 20)
	if after := m.transcriptText(); after != before || notSentLines(m) != 0 {
		t.Errorf("an answer for a send already handed to the model changed the transcript:\nbefore %q\nafter  %q", before, after)
	}
}

// Of two echoes with the same words, the stop decides each by its own
// send's answer.
func TestEachEchoIsDecidedByItsOwnSend(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "again")
	first := m.sends
	m, _ = pressEnterWith(t, m, "again")
	second := m.sends
	m = taken(t, m, first, 19)

	m.applyEvent(stopAt(20))
	m = taken(t, m, second, 20)
	m.applyEvent(userMessage(21, "again"))

	if n := notSentLines(m); n != 1 {
		t.Errorf("want the send taken before the stop marked and the other drawn, got %d marked:\n%s", n, m.transcriptText())
	}
	if users, echoes := userLines(m); len(users) != 1 || len(echoes) != 0 {
		t.Errorf("got users %q, echoes %q", users, echoes)
	}
}

// Two stops while the answer is on its way: the later one decides. A send
// taken between them was in the queue the second one dropped.
func TestOfTwoStopsBeforeTheAnswerTheLaterDecides(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "run it")
	send := m.sends

	m.applyEvent(stopAt(20))
	m.applyEvent(userMessage(21, "something else"))
	m.applyEvent(stopAt(22))
	m = taken(t, m, send, 21)
	if n := notSentLines(m); n != 1 {
		t.Errorf("a send taken before the second stop is still drawn as sent:\n%s", m.transcriptText())
	}
}

func TestAnAnswerForAnEchoALostTurnMarkedChangesNothing(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	m, _ = pressEnterWith(t, m, "run it")
	send := m.sends
	m.applyEvent(stopAt(20))
	m.waiting = true
	m.endLostTurn()
	before := m.transcriptText()

	m = taken(t, m, send, 19)
	if after := m.transcriptText(); after != before {
		t.Errorf("an answer for a line already marked changed it:\nbefore %q\nafter  %q", before, after)
	}
}

// The send itself carries the daemon's answer back to the echo.
func TestASendCarriesTheReceiptToItsEcho(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"injected","seq":7}`))
	}))
	defer srv.Close()
	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event), false)

	msg, ok := m.sendMessage("run it", false, 3)().(turnDoneMsg)
	if !ok {
		t.Fatal("the send did not answer with a turnDoneMsg")
	}
	if want := (client.Receipt{Status: "injected", Seq: 7, HasSeq: true}); msg.err != nil || msg.receipt != want || msg.echo != 3 {
		t.Errorf("got err %v, receipt %+v, echo %d; want the daemon's receipt %+v for echo 3", msg.err, msg.receipt, msg.echo, want)
	}
	if msg.session != "s1" {
		t.Errorf("the answer names session %q, want the one the send went to, s1", msg.session)
	}
}
