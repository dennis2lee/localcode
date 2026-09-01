package agent

import (
	"context"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// The arithmetic of undoing a turn, tested as arithmetic.
//
// These are pure functions over an event list on purpose: the failures
// worth catching here — cutting inside a turn, undoing the same turn
// twice, walking back across a barrier — are all shape errors that a
// test with a model in it would report as something else.

// logEv is one log line. The sequence is explicit because it is the whole
// subject: a rewind names an absolute seq and everything composes on
// that.
func logEv(seq uint64, t events.Type, data map[string]any) events.Event {
	return events.Event{Seq: seq, Type: t, Data: data}
}

func userEv(seq uint64, text string) events.Event {
	return logEv(seq, events.TypeUserMessage, map[string]any{"text": text})
}

func replyEv(seq uint64, text string) events.Event {
	return logEv(seq, events.TypeMessagePartEnd, map[string]any{"text": text})
}

// Two ordinary turns, then a rewind of the second. The first must be
// intact and the second must be gone, both from the replay and from what
// a second rewind would consider most recent.
func TestARewindRemovesExactlyTheTurnItNames(t *testing.T) {
	evs := []events.Event{
		userEv(1, "first question"),
		replyEv(2, "first answer"),
		userEv(3, "second question"),
		replyEv(4, "second answer"),
		logEv(5, events.TypeRewound, map[string]any{"from_seq": float64(3)}),
	}

	got := applyRewinds(evs)
	var seqs []uint64
	for _, e := range got {
		seqs = append(seqs, e.Seq)
	}
	want := []uint64{1, 2, 5}
	if len(seqs) != len(want) {
		t.Fatalf("kept %v, want %v", seqs, want)
	}
	for i := range want {
		if seqs[i] != want[i] {
			t.Fatalf("kept %v, want %v", seqs, want)
		}
	}

	// And the replay agrees: one exchange, not two.
	msgs := rehydrateHistory(got)
	if len(msgs) != 2 {
		t.Fatalf("rehydrated %d messages, want the first exchange only: %+v", len(msgs), msgs)
	}
	if text := msgs[0].Content[0].Text; text != "first question" {
		t.Errorf("history opens with %q, want the first question", text)
	}
}

// Rewinding twice walks back two turns. This is the property that lets
// last-turn-only reach further without a picker, and it works only
// because the second rewind reads the already-filtered list.
func TestTwoRewindsWalkBackTwoTurns(t *testing.T) {
	evs := []events.Event{
		userEv(1, "first"),
		replyEv(2, "a"),
		userEv(3, "second"),
		replyEv(4, "b"),
		logEv(5, events.TypeRewound, map[string]any{"from_seq": float64(3)}),
	}

	from, ok := lastTurnStart(applyRewinds(evs))
	if !ok || from != 1 {
		t.Fatalf("after one rewind the next anchor is (%d, %v), want (1, true)", from, ok)
	}

	evs = append(evs, logEv(6, events.TypeRewound, map[string]any{"from_seq": from}))
	if msgs := rehydrateHistory(applyRewinds(evs)); len(msgs) != 0 {
		t.Errorf("after two rewinds %d messages survive, want none: %+v", len(msgs), msgs)
	}
	if _, ok := lastTurnStart(applyRewinds(evs)); ok {
		t.Error("a third rewind found a turn to undo when both are gone")
	}
}

// The anchor is a turn opening, and three kinds of user message are not
// one. Anchoring on an injected message is the sharp case: it cuts a turn
// in half and leaves a tool_use with no tool_result.
func TestTheAnchorSkipsMessagesThatDidNotOpenATurn(t *testing.T) {
	for _, flag := range []string{"local", "injected", "auto"} {
		evs := []events.Event{
			userEv(1, "the real question"),
			replyEv(2, "working"),
			logEv(3, events.TypeUserMessage, map[string]any{"text": "not a turn", flag: true}),
		}
		from, ok := lastTurnStart(evs)
		if !ok || from != 1 {
			t.Errorf("with a %q message last, the anchor is (%d, %v), want (1, true)", flag, from, ok)
		}
	}
}

// A barrier is where the record stops being replayable: the events before
// it are still in the log, but the history the model holds no longer
// derives from them. Rewinding past that would ask replay to rebuild a
// turn whose earlier half has already been discarded.
func TestARewindStopsAtABarrier(t *testing.T) {
	for _, barrier := range []events.Event{
		logEv(3, events.TypeCleared, nil),
		logEv(3, events.TypeCompacted, map[string]any{"summary": "we talked about the parser"}),
	} {
		evs := []events.Event{userEv(1, "question"), replyEv(2, "answer"), barrier}
		if _, ok := lastTurnStart(evs); ok {
			t.Errorf("a turn was offered for rewinding across a %s barrier", barrier.Type)
		}
	}

	// But a turn after the barrier is fine: that one really is replayable.
	evs := []events.Event{
		userEv(1, "old"), replyEv(2, "old answer"),
		logEv(3, events.TypeCleared, nil),
		userEv(4, "new"), replyEv(5, "new answer"),
	}
	if from, ok := lastTurnStart(evs); !ok || from != 4 {
		t.Errorf("the turn after the barrier anchors at (%d, %v), want (4, true)", from, ok)
	}
}

// A "/clear" barrier is the one with nothing behind it. Compaction leaves
// the summary; this leaves the model with no history at all, and it has
// to survive a restart, which is the only reason it is in the log.
func TestClearIsABarrierThatLeavesNothing(t *testing.T) {
	evs := []events.Event{
		userEv(1, "a long conversation"),
		replyEv(2, "a long answer"),
		logEv(3, events.TypeCleared, nil),
	}
	if msgs := rehydrateHistory(evs); len(msgs) != 0 {
		t.Fatalf("a cleared conversation rehydrated %d messages: %+v", len(msgs), msgs)
	}

	// And what comes after it is all the model gets.
	evs = append(evs, userEv(4, "a fresh start"), replyEv(5, "fresh answer"))
	msgs := rehydrateHistory(evs)
	if len(msgs) != 2 || msgs[0].Content[0].Text != "a fresh start" {
		t.Fatalf("after a clear the history is %+v, want only the messages since", msgs)
	}
}

// An empty summary is exactly what compaction's own barrier does not
// treat as one, which is why /clear has an event of its own. Pinned so
// the shortcut is not taken later.
func TestAnEmptyCompactionSummaryIsNotABarrier(t *testing.T) {
	evs := []events.Event{
		userEv(1, "question"),
		replyEv(2, "answer"),
		logEv(3, events.TypeCompacted, map[string]any{"summary": ""}),
	}
	if msgs := rehydrateHistory(evs); len(msgs) == 0 {
		t.Fatal("an empty summary now clears the history; /clear needs its own event because it does not")
	}
}

// A marker with no anchor cannot say what it undid. Skipping it wastes a
// rewind; acting on it would erase a conversation from a malformed line.
func TestARewindMarkerWithNoAnchorRemovesNothing(t *testing.T) {
	evs := []events.Event{
		userEv(1, "question"),
		replyEv(2, "answer"),
		logEv(3, events.TypeRewound, map[string]any{}),
	}
	if got := applyRewinds(evs); len(got) != len(evs) {
		t.Errorf("an anchorless marker dropped %d events", len(evs)-len(got))
	}
}

// A turn is more than its first and last event. This one carries a tool
// call and a message typed while it ran, and the whole of it has to leave
// together: a surviving tool_use with no tool_result is a request some
// providers reject outright.
func TestATurnWithToolsAndAnInjectedMessageRewindsAsOneUnit(t *testing.T) {
	evs := []events.Event{
		userEv(1, "first"),
		replyEv(2, "first answer"),
		userEv(3, "read the file and fix it"),
		logEv(4, events.TypeToolStart, map[string]any{"tool_use_id": "t1", "name": "read_file"}),
		logEv(5, events.TypeUserMessage, map[string]any{"text": "also check the tests", "injected": true}),
		logEv(6, events.TypeToolEnd, map[string]any{"tool_use_id": "t1", "name": "read_file", "input": "{}", "content": "the file"}),
		replyEv(7, "fixed"),
	}

	from, ok := lastTurnStart(evs)
	if !ok || from != 3 {
		t.Fatalf("the anchor is (%d, %v), want (3, true) — the injected message is not a turn opening", from, ok)
	}

	after := applyRewinds(append(evs, logEv(8, events.TypeRewound, map[string]any{"from_seq": float64(from)})))
	msgs := rehydrateHistory(after)
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == provider.BlockToolUse {
				t.Errorf("a tool_use survived the rewind of the turn that made it: %+v", b)
			}
		}
	}
	if len(msgs) != 2 {
		t.Fatalf("rewinding left %d messages, want the first exchange only: %+v", len(msgs), msgs)
	}
}

// The two commands end to end, through the router that answers them.
//
// The pure functions above prove the arithmetic; these prove the command
// does what it says to a real session — and, for /clear, that the promise
// it makes about the record is kept rather than merely stated.

func clearLoop(t *testing.T) (*Loop, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": nil}, cfg)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	return loop, sid
}

// The promise: the model forgets, the conversation does not.
func TestClearEmptiesTheModelAndKeepsTheRecord(t *testing.T) {
	loop, sid := clearLoop(t)
	loop.Store.Append(sid, events.TypeUserMessage, map[string]any{"text": "remember the marmalade"})
	loop.Store.Append(sid, events.TypeMessagePartEnd, map[string]any{"text": "noted"})
	loop.RehydrateSession(sid)
	if len(loop.history(sid)) == 0 {
		t.Fatal("the session has no history to clear, so this proves nothing")
	}

	handled, err := loop.routeClear(sid, "/clear")
	if !handled || err != nil {
		t.Fatalf("routeClear(handled=%v, err=%v)", handled, err)
	}
	if h := loop.history(sid); len(h) != 0 {
		t.Errorf("the model still holds %d message(s) after /clear: %+v", len(h), h)
	}

	// The half that is easy to break and hard to notice: the record.
	evs, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		if e.Type == events.TypeUserMessage && dataString(e.Data, "text") == "remember the marmalade" {
			found = true
		}
	}
	if !found {
		t.Error("/clear removed the conversation from the log; it must only stop sending it")
	}

	// And a restart agrees, which is the only reason the marker is in the
	// log at all: without it, replay would hand the whole conversation back.
	loop.setHistory(sid, nil)
	loop.RehydrateSession(sid)
	if h := loop.history(sid); len(h) != 0 {
		t.Errorf("after a restart a cleared conversation came back with %d message(s): %+v", len(h), h)
	}
}

// A rewind with no turn behind it says so rather than doing something
// approximate.
func TestRewindWithNothingToUndoSaysSo(t *testing.T) {
	loop, sid := clearLoop(t)
	handled, err := loop.routeRewind(context.Background(), sid, "/rewind")
	if !handled || err != nil {
		t.Fatalf("routeRewind(handled=%v, err=%v)", handled, err)
	}
	if got := lastAssistantText(loop.Store, sid); !strings.Contains(got, "nothing to undo") {
		t.Errorf("the reply was %q, want it to say there is nothing to undo", got)
	}
}

// Nobody is watching, and this one writes to a real project.
func TestRewindRefusesWhenNobodyIsWatching(t *testing.T) {
	loop, sid := clearLoop(t)
	loop.Store.Append(sid, events.TypeUserMessage, map[string]any{"text": "do the thing"})
	loop.Store.Append(sid, events.TypeMessagePartEnd, map[string]any{"text": "did it"})

	handled, err := loop.routeRewind(WithUnattended(context.Background()), sid, "/rewind")
	if !handled || err != nil {
		t.Fatalf("routeRewind(handled=%v, err=%v)", handled, err)
	}
	got := lastAssistantText(loop.Store, sid)
	if !strings.Contains(got, "conversation somebody is having") {
		t.Errorf("the reply was %q, want a refusal naming the reason", got)
	}
	// And it really did nothing: no marker, so a later attended /rewind
	// still has that turn to undo.
	evs, _ := loop.Store.Events(sid, 0)
	for _, e := range evs {
		if e.Type == events.TypeRewound {
			t.Error("a refused /rewind appended a marker anyway")
		}
	}
}

// The reply is where the limits are stated, and they have to be stated
// every time: somebody reading "rewound" and assuming their tree is back
// where it was has been misled by a message that was technically true.
func TestTheRewindReplyNamesWhatItDoesNotCover(t *testing.T) {
	loop, sid := clearLoop(t)
	loop.Store.Append(sid, events.TypeUserMessage, map[string]any{"text": "change some files"})
	loop.Store.Append(sid, events.TypeMessagePartEnd, map[string]any{"text": "done"})

	handled, err := loop.routeRewind(context.Background(), sid, "/rewind")
	if !handled || err != nil {
		t.Fatalf("routeRewind(handled=%v, err=%v)", handled, err)
	}
	got := lastAssistantText(loop.Store, sid)
	for _, want := range []string{"write_file", "shell command", "background sub-agent", "version control"} {
		if !strings.Contains(got, want) {
			t.Errorf("the reply never mentions %q:\n%s", want, got)
		}
	}
}
