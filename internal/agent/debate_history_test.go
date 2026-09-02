package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"localcode/internal/events"
	"localcode/internal/provider"
)

// A debate is one turn from the conversation's point of view.
//
// It runs its rounds in this session, because the author needs the
// conversation it has been having — its history, its cached prefix and
// its tools — while the rounds are going. What it must not do is leave
// them there afterwards.
//
// Two things went wrong when it did. The briefs are localcode's own text
// in a user message ("Fix what you agree with ... Do not ask for another
// review: it happens on its own when your turn ends"), so every later
// message in that conversation carried an instruction from a loop that
// had ended. And a conversation whose recent history is three rounds of
// review is one where the model reaches for the Debate tool again on the
// next unrelated prompt, which is a debate nobody asked for.
//
// So the rounds leave the model's context when the debate ends. They stay
// in the transcript and in the log, where they are read by the person.

// debateHistoryScript is a recording model: it answers, and it keeps every
// request the author was sent so a later turn can be inspected.
type debateHistoryScript struct {
	mu sync.Mutex
	// authorReqs is the messages array of each request on the author's
	// model, oldest first.
	authorReqs [][]map[string]any
	// approve decides the reviewer's verdict.
	approve bool
	// authorSilent makes the author answer with nothing at all, which is
	// what a debate that produced no work looks like.
	authorSilent bool
	// book, when set and returning true, has the author call the Debate
	// tool instead of answering: the natural-language entrance.
	book func() bool
}

func (s *debateHistoryScript) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		author := body.Model == authorModel

		s.mu.Lock()
		if author {
			s.authorReqs = append(s.authorReqs, body.Messages)
		}
		approve, silent := s.approve, s.authorSilent
		booking := author && s.book != nil && s.book()
		s.mu.Unlock()

		var chunks []string
		switch {
		case booking:
			chunks = toolCallChunks("call_debate", debateToolName,
				`{\"reviewers\":[\"girl\"],\"rounds\":2,\"task\":\"write a sum function\"}`)
		case author && silent:
			chunks = textChunks("")
		case author:
			chunks = textChunks("the sum function, as revised")
		default:
			chunks = toolCallChunks("call_verdict", verdictToolName,
				fmt.Sprintf(`{\"approved\":%t,\"findings\":\"name the variables better\"}`, approve))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
}

// lastAuthorRequest is the messages array of the most recent request the
// author's model was sent, as role/content pairs.
func (s *debateHistoryScript) lastAuthorRequest(t *testing.T) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.authorReqs) == 0 {
		t.Fatal("the author was never called")
	}
	var out []string
	for _, m := range s.authorReqs[len(s.authorReqs)-1] {
		role, _ := m["role"].(string)
		if role == "system" {
			continue
		}
		content, _ := m["content"].(string)
		out = append(out, role+": "+content)
	}
	return out
}

func joinMessages(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		for _, blk := range m.Content {
			b.WriteString(blk.Text)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// The next ordinary prompt is sent with the task and the answer, and with
// none of the machinery that produced the answer.
func TestADebateLeavesTheConversationItsResultAndNotItsRounds(t *testing.T) {
	script := &debateHistoryScript{}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/debate girl 2 write a sum function"); err != nil {
		t.Fatalf("debate: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "boy", "what is 2+2"); err != nil {
		t.Fatalf("plain prompt: %v", err)
	}

	got := script.lastAuthorRequest(t)
	want := []string{
		"user: write a sum function",
		"assistant: the sum function, as revised",
		"user: what is 2+2",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the turn after a debate was sent:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// The rounds are still readable. Collapsing the model's context is not a
// delete, and the same rule the archive and "/clear" follow applies here:
// a session's own record survives until the session does.
func TestADebateThatLeavesTheContextStaysInTheLog(t *testing.T) {
	script := &debateHistoryScript{}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/debate girl 2 write a sum function"); err != nil {
		t.Fatalf("debate: %v", err)
	}

	if n := len(debateEvents(t, loop, sid, events.TypeDebateReview)); n != 2 {
		t.Errorf("the log holds %d reviews, want both rounds", n)
	}
	all, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var briefs int
	for _, ev := range all {
		if ev.Type == events.TypeUserMessage && dataString(ev.Data, "source") == "reminder.debate" {
			briefs++
		}
	}
	if briefs == 0 {
		t.Error("the round briefs are gone from the log; they are the record of what the author was asked")
	}
}

// A restart has to agree with the daemon that wrote the log. The history
// is rebuilt from events, so a collapse that lives only in memory would
// come back undone the first time localcode was restarted.
func TestARestartRebuildsTheCollapsedDebate(t *testing.T) {
	script := &debateHistoryScript{}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/debate girl 2 write a sum function"); err != nil {
		t.Fatalf("debate: %v", err)
	}
	live := joinMessages(loop.history(sid))

	loop.setHistory(sid, nil)
	loop.RehydrateSession(sid)
	restored := joinMessages(loop.history(sid))

	if restored != live {
		t.Errorf("a restart rebuilt a different conversation:\n--- live ---\n%s\n--- restored ---\n%s", live, restored)
	}
	// And that the thing they agree on is the collapsed one, not two
	// copies of the rounds.
	if n := len(loop.history(sid)); n != 2 {
		t.Errorf("the rebuilt conversation holds %d messages, want the task and the answer:\n%s", n, restored)
	}
}

// The natural-language entrance books a debate from inside a turn, so the
// person's own sentence and the tool call that read it come before the
// debate's own first message. Those are the conversation; only what the
// debate appended after them is the debate's.
func TestCollapsingADebateKeepsWhatCameBeforeIt(t *testing.T) {
	script := &debateHistoryScript{}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	registerDebateTool(t, loop)
	sid := startDebateSession(t, loop)

	// The author books on its first turn and answers on every one after,
	// which is the shape the tool's own answer asks for.
	booked := false
	script.mu.Lock()
	script.book = func() bool {
		if booked {
			return false
		}
		booked = true
		return true
	}
	script.mu.Unlock()

	if err := loop.SendMessage(context.Background(), sid, "boy", "have girl review a sum function with you"); err != nil {
		t.Fatalf("send: %v", err)
	}

	if n := len(debateEvents(t, loop, sid, events.TypeDebateEnded)); n != 1 {
		t.Fatalf("%d debates ran from the sentence, want 1 — this test proves nothing otherwise", n)
	}

	h := joinMessages(loop.history(sid))
	t.Logf("history after a debate booked from a sentence:\n%s", h)
	if !strings.Contains(h, "have girl review a sum function with you") {
		t.Errorf("the collapse ate the sentence the person actually typed:\n%s", h)
	}
	if strings.Contains(h, "Fix what you agree with") {
		t.Errorf("a round brief survived the debate:\n%s", h)
	}

	// And a restart still agrees about where the debate began.
	live := h
	loop.setHistory(sid, nil)
	loop.RehydrateSession(sid)
	if restored := joinMessages(loop.history(sid)); restored != live {
		t.Errorf("a restart rebuilt a different conversation:\n--- live ---\n%s\n--- restored ---\n%s", live, restored)
	}
}

// A debate that produced nothing leaves nothing. The task was localcode's
// own message on the author's behalf, so keeping it alone would leave the
// history ending on a user message with no answer to it — a shape Bedrock
// rejects outright, and a claim that work was asked for and abandoned.
func TestADebateThatProducedNothingLeavesNothingBehind(t *testing.T) {
	script := &debateHistoryScript{authorSilent: true}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/debate girl 1 write a sum function"); err != nil {
		t.Fatalf("debate: %v", err)
	}
	if got := loop.history(sid); len(got) != 0 {
		t.Errorf("history after a debate that answered nothing:\n%s", joinMessages(got))
	}
}
