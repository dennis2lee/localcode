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

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// debateScript is a two-model mock: an author that may or may not do any
// work, and a reviewer that approves on a chosen round.
//
// It records what each side was actually sent, because most of what is
// worth asserting about a debate is not in the final text — it is which
// tools the reviewer was offered, whether the second round re-sent the
// task, and whether the author was handed the review at all.
type debateScript struct {
	mu sync.Mutex

	// approveAt is the round the reviewer approves on. 0 never approves.
	approveAt int
	// authorWorks makes the author call a tool before answering, which is
	// what the stall check counts.
	authorWorks bool

	authorPrompts   []string
	reviewPrompts   []string
	reviewerTools   []map[string]bool
	reviewerSystems []string
	rounds          int
}

const (
	authorModel = "author-model"
	reviewModel = "review-model"
)

func (s *debateScript) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
			Tools    []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}

		system, lastUser, lastRole := "", "", ""
		for _, m := range body.Messages {
			content, _ := m["content"].(string)
			role, _ := m["role"].(string)
			switch role {
			case "system":
				system = content
			case "user":
				lastUser = content
			}
			if role != "system" {
				lastRole = role
			}
		}
		// Whether this request is the continuation of a turn that just ran
		// a tool, rather than the opening of a new one. It has to be the
		// *last* message rather than "is there a tool result anywhere":
		// the reviewer keeps one session across rounds, so from round two
		// its history always contains the previous round's verdict call,
		// and a mock that looked for one anywhere answered every later
		// round as if it were already mid-turn.
		hasToolResult := lastRole == "tool"
		toolset := map[string]bool{}
		for _, tl := range body.Tools {
			if fn, ok := tl["function"].(map[string]any); ok {
				if name, _ := fn["name"].(string); name != "" {
					toolset[name] = true
				}
			}
		}

		s.mu.Lock()
		round := s.rounds
		switch body.Model {
		case authorModel:
			if !hasToolResult {
				s.authorPrompts = append(s.authorPrompts, lastUser)
			}
		case reviewModel:
			if !hasToolResult {
				s.rounds++
				round = s.rounds
				s.reviewPrompts = append(s.reviewPrompts, lastUser)
			}
			s.reviewerTools = append(s.reviewerTools, toolset)
			s.reviewerSystems = append(s.reviewerSystems, system)
		}
		approve := s.approveAt != 0 && round >= s.approveAt
		works := s.authorWorks
		s.mu.Unlock()

		var chunks []string
		switch {
		case body.Model == authorModel && works && !hasToolResult:
			chunks = toolCallChunks("call_glob", "glob", `{\"pattern\":\"*.go\"}`)
		case body.Model == authorModel:
			chunks = textChunks("the author's answer")
		case body.Model == reviewModel && !hasToolResult:
			args := fmt.Sprintf(`{\"approved\":%t,\"findings\":\"round %d finding\"}`, approve, round)
			chunks = toolCallChunks("call_verdict", verdictToolName, args)
		default:
			chunks = textChunks(fmt.Sprintf("review of round %d", round))
		}

		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
}

func toolCallChunks(id, name, escapedArgs string) []string {
	return []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"` + id + `","function":{"name":"` + name + `","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"` + escapedArgs + `"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}
}

func textChunks(text string) []string {
	return []string{
		`{"choices":[{"delta":{"content":"` + text + `"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
}

// newDebateLoop wires a loop with two agents on two models, and every
// file tool registered — the write tools have to exist for their absence
// from a reviewer's turn to mean anything.
func newDebateLoop(t *testing.T, modelURL string) *Loop {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	registry := tools.NewRegistry(nil)
	registry.Register(tools.ReadFile{})
	registry.Register(tools.WriteFile{})
	registry.Register(tools.Edit{})
	registry.Register(tools.Bash{})
	registry.Register(tools.Glob{})
	registry.Register(tools.Grep{})
	registry.Register(VerdictTool{})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelURL},
		},
		Profiles: map[string]config.Profile{
			"strong": {Provider: "local", Model: authorModel},
			"cheap":  {Provider: "local", Model: reviewModel},
		},
		Agents: map[string]config.AgentConfig{
			"boy":  {Profile: "strong", Description: "writes code"},
			"girl": {Profile: "cheap", Description: "reviews code"},
		},
		DefaultProfile: "strong",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, registry, map[string]provider.Provider{"local": provider.NewOpenAICompat(modelURL, "")}, cfg)
	NewTaskManager(context.Background(), loop, 5)
	return loop
}

func startDebateSession(t *testing.T, loop *Loop) string {
	t.Helper()
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "boy", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sid
}

func debateEvents(t *testing.T, loop *Loop, sid string, typ events.Type) []events.Event {
	t.Helper()
	all, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var out []events.Event
	for _, ev := range all {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

// TestADebateEndsWhenTheReviewerApproves is the whole feature end to end:
// two agents on two models, alternating, stopping on the approval rather
// than on the round budget.
func TestADebateEndsWhenTheReviewerApproves(t *testing.T) {
	script := &debateScript{approveAt: 2, authorWorks: true}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/debate girl 5 write a sum function"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	reviews := debateEvents(t, loop, sid, events.TypeDebateReview)
	if len(reviews) != 2 {
		t.Fatalf("got %d reviews, want 2 (approval is on round 2)", len(reviews))
	}
	if approved, _ := reviews[0].Data["approved"].(bool); approved {
		t.Error("round 1 was recorded as approved")
	}
	if approved, _ := reviews[1].Data["approved"].(bool); !approved {
		t.Error("round 2 was not recorded as approved")
	}

	ended := debateEvents(t, loop, sid, events.TypeDebateEnded)
	if len(ended) != 1 {
		t.Fatalf("got %d debate.ended events, want 1", len(ended))
	}
	if reason, _ := ended[0].Data["reason"].(string); reason != "approved" {
		t.Errorf("ended reason = %q, want \"approved\"", reason)
	}
	// The count, not just the word: plural() answers which word to use
	// and nothing about how many, and "girl approved after rounds" is
	// what forgetting that produces.
	if note, _ := ended[0].Data["note"].(string); !strings.Contains(note, "girl approved after 2 rounds") {
		t.Errorf("closing note = %q, want it to say girl approved after 2 rounds", note)
	}

	// The author ran twice: once on the task, once on the review.
	script.mu.Lock()
	prompts := append([]string(nil), script.authorPrompts...)
	script.mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("author ran %d times, want 2", len(prompts))
	}
	if !strings.Contains(prompts[0], "write a sum function") {
		t.Errorf("author's first prompt = %q, want the task without the command around it", prompts[0])
	}
	if strings.Contains(prompts[0], "/debate") {
		t.Errorf("the author was given the command line: %q", prompts[0])
	}
	// The protocol must not reach the author, or it runs a loop of its
	// own inside the one already running it.
	if strings.Contains(prompts[1], "5 rounds") || strings.Contains(prompts[1], "repeat") {
		t.Errorf("the author's second prompt carries the protocol: %q", prompts[1])
	}
	if !strings.Contains(prompts[1], "round 1 finding") {
		t.Errorf("the author's second prompt does not carry the review: %q", prompts[1])
	}
}

// TestADebateStopsAtTheRoundLimit is the other ending, and the one that
// bounds the bill: a reviewer that never approves does not run forever.
func TestADebateStopsAtTheRoundLimit(t *testing.T) {
	script := &debateScript{authorWorks: true}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/debate girl 3 write a sum function"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if reviews := debateEvents(t, loop, sid, events.TypeDebateReview); len(reviews) != 3 {
		t.Fatalf("got %d reviews, want exactly 3", len(reviews))
	}
	ended := debateEvents(t, loop, sid, events.TypeDebateEnded)
	if len(ended) != 1 {
		t.Fatalf("got %d debate.ended events, want 1", len(ended))
	}
	if reason, _ := ended[0].Data["reason"].(string); reason != "rounds" {
		t.Errorf("ended reason = %q, want \"rounds\"", reason)
	}
	if note, _ := ended[0].Data["note"].(string); !strings.Contains(note, "all 3 rounds used and girl has not approved") {
		t.Errorf("closing note = %q, want it to count the rounds and say it was not approved", note)
	}
}

// TestADebateStopsWhenTheAuthorStopsWorking covers the third ending. Two
// rounds in which the author calls no tool at all is a standoff, and
// spending the remaining rounds restating it helps nobody.
func TestADebateStopsWhenTheAuthorStopsWorking(t *testing.T) {
	script := &debateScript{authorWorks: false}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/debate girl 9 write a sum function"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if reviews := debateEvents(t, loop, sid, events.TypeDebateReview); len(reviews) != debateStallLimit {
		t.Fatalf("got %d reviews, want %d — a standoff should not use the whole budget", len(reviews), debateStallLimit)
	}
	ended := debateEvents(t, loop, sid, events.TypeDebateEnded)
	if len(ended) != 1 {
		t.Fatalf("got %d debate.ended events, want 1", len(ended))
	}
	if reason, _ := ended[0].Data["reason"].(string); reason != "stalled" {
		t.Errorf("ended reason = %q, want \"stalled\"", reason)
	}
}

// TestTheReviewerCanReadButNotWrite is the promise the feature is sold
// on. The reviewer's turn is offered reading tools and the verdict, and
// nothing that can change a file — not even the shell, which is the one
// that would otherwise walk straight through this.
func TestTheReviewerCanReadButNotWrite(t *testing.T) {
	script := &debateScript{approveAt: 1, authorWorks: true}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/debate girl 2 write a sum function"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	script.mu.Lock()
	seen := append([]map[string]bool(nil), script.reviewerTools...)
	script.mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("the reviewer was never called")
	}
	for i, toolset := range seen {
		for _, banned := range []string{"write_file", "edit", "bash"} {
			if toolset[banned] {
				t.Errorf("reviewer request %d was offered %q", i, banned)
			}
		}
		for _, wanted := range []string{"read_file", "grep", verdictToolName} {
			if !toolset[wanted] {
				t.Errorf("reviewer request %d was not offered %q", i, wanted)
			}
		}
	}
}

// TestTheVerdictIsHiddenFromEveryTurnThatIsNotAReview is the other half
// of that: nobody else is even shown the tool, so no model is ever in a
// position to approve its own work.
func TestTheVerdictIsHiddenFromEveryTurnThatIsNotAReview(t *testing.T) {
	script := &debateScript{approveAt: 1, authorWorks: true}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	ctx := context.Background()

	if hidden := loop.hiddenTools(ctx); !hidden[verdictToolName] {
		t.Error("hiddenTools does not hide the verdict from an ordinary turn")
	}
	offered := loop.toolsForTurn(ctx, config.AgentConfig{})
	for _, name := range offered {
		if name == verdictToolName {
			t.Fatalf("an ordinary turn is offered %q", verdictToolName)
		}
	}

	res := VerdictTool{}.Execute(ctx, json.RawMessage(`{"approved":true}`))
	if !res.IsError || !res.Refused {
		t.Errorf("Verdict outside a debate returned %+v, want a refusal", res)
	}
}

// TestAReviewArrivesAsAnotherAgentsWordsNotTheUsers pins the provenance.
//
// The review reaches the author inside a user-role message, which is the
// most instruction-authoritative position a request has, and it was
// written by a different model. The framing around it is localcode's; the
// review itself is external material, and the span is what says where one
// stops and the other starts.
func TestAReviewArrivesAsAnotherAgentsWordsNotTheUsers(t *testing.T) {
	script := &debateScript{approveAt: 2, authorWorks: true}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/debate girl 5 write a sum function"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	msgs := debateEvents(t, loop, sid, events.TypeUserMessage)
	if len(msgs) != 2 {
		t.Fatalf("got %d user messages, want 2 (the command, then the review going back)", len(msgs))
	}
	if isTrue(msgs[0].Data["auto"]) {
		t.Error("the command the person typed was marked auto")
	}
	if got := dataString(msgs[0].Data, "text"); !strings.HasPrefix(got, "/debate ") {
		t.Errorf("the transcript shows %q for round 1, want the command as typed", got)
	}

	second := msgs[1].Data
	if !isTrue(second["auto"]) {
		t.Error("the round-2 message is not marked auto, so a client paints it as something the person typed")
	}
	if got := dataString(second, "source"); got != "reminder.debate" {
		t.Errorf("round-2 source = %q, want the framing to be localcode's own", got)
	}
	spans, ok := second["sources"].([]provider.BlockSource)
	if !ok || len(spans) != 1 {
		t.Fatalf("round-2 sources = %#v, want one span over the review", second["sources"])
	}
	if spans[0].ID != "debate.review.girl" {
		t.Errorf("span id = %q, want debate.review.girl", spans[0].ID)
	}
	model := dataString(second, "model_text")
	if got := model[spans[0].From:spans[0].To]; got != "round 1 finding" {
		t.Errorf("the span covers %q, want exactly the reviewer's words", got)
	}

	entry, ok := entryForSource("debate.review.girl", "round 1 finding", false)
	if !ok {
		t.Fatal("a review span has no prompt-surface entry, so no manifest can describe it")
	}
	if entry.Trust != "external" {
		t.Errorf("a review is recorded as %q, want external — it is another model's output", entry.Trust)
	}
}

// TestTheReviewerKeepsOneSessionAcrossRounds is what makes this a debate
// rather than a series of unrelated reviews: in round two the reviewer
// still has round one, so the task is not re-sent and it can check its
// own earlier findings.
func TestTheReviewerKeepsOneSessionAcrossRounds(t *testing.T) {
	script := &debateScript{approveAt: 3, authorWorks: true}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/debate girl 5 write a sum function"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	reviews := debateEvents(t, loop, sid, events.TypeDebateReview)
	if len(reviews) != 3 {
		t.Fatalf("got %d reviews, want 3", len(reviews))
	}
	first := dataString(reviews[0].Data, "session")
	if first == "" {
		t.Fatal("the review event does not name the reviewer's session, so it cannot be opened")
	}
	for i, ev := range reviews {
		if got := dataString(ev.Data, "session"); got != first {
			t.Errorf("round %d ran in session %q, want the same one as round 1 (%q)", i+1, got, first)
		}
	}
	// One sub-agent, so one row: a second task.spawned would show the
	// same reviewer twice in the panel.
	if spawned := debateEvents(t, loop, sid, events.TypeTaskSpawned); len(spawned) != 1 {
		t.Errorf("got %d task.spawned events, want 1 for one reviewer", len(spawned))
	}

	script.mu.Lock()
	prompts := append([]string(nil), script.reviewPrompts...)
	script.mu.Unlock()
	if len(prompts) != 3 {
		t.Fatalf("reviewer opened %d rounds, want 3", len(prompts))
	}
	if !strings.Contains(prompts[0], "write a sum function") {
		t.Errorf("round 1 brief does not carry the task: %q", prompts[0])
	}
	if strings.Contains(prompts[1], "write a sum function") {
		t.Errorf("round 2 re-sent the task to a reviewer that already has it: %q", prompts[1])
	}
	if !strings.Contains(prompts[1], "findings you raised") {
		t.Errorf("round 2 does not ask the reviewer to check its own earlier findings: %q", prompts[1])
	}
}

// TestParseDebateCommand covers the one real ambiguity in the syntax:
// the rounds are positional and optional, and a task can start with a
// number.
func TestParseDebateCommand(t *testing.T) {
	cases := []struct {
		name     string
		arg      string
		reviewer string
		rounds   int
		task     string
		wantErr  bool
	}{
		{name: "reviewer, rounds and task", arg: "girl 10 write a sum function",
			reviewer: "girl", rounds: 10, task: "write a sum function"},
		{name: "no rounds falls back to the default", arg: "girl write a sum function",
			reviewer: "girl", rounds: debateDefaultRounds, task: "write a sum function"},
		{name: "a task that starts with a number is a task", arg: "girl 10페이지 문서를 써라",
			reviewer: "girl", rounds: debateDefaultRounds, task: "10페이지 문서를 써라"},
		{name: "a number glued to a word is not a count", arg: "girl 3rd draft, tidy it up",
			reviewer: "girl", rounds: debateDefaultRounds, task: "3rd draft, tidy it up"},
		{name: "no task", arg: "girl 5", wantErr: true},
		{name: "reviewer only", arg: "girl", wantErr: true},
		{name: "rounds above the ceiling", arg: "girl 99 do something", wantErr: true},
		{name: "zero rounds", arg: "girl 0 do something", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reviewer, rounds, task, err := parseDebateCommand(tc.arg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parsed %q as %q/%d/%q, want an error", tc.arg, reviewer, rounds, task)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse %q: %v", tc.arg, err)
			}
			if reviewer != tc.reviewer || rounds != tc.rounds || task != tc.task {
				t.Errorf("parsed %q as %q/%d/%q, want %q/%d/%q",
					tc.arg, reviewer, rounds, task, tc.reviewer, tc.rounds, tc.task)
			}
		})
	}
}

// TestAnApprovalHasToBeTheWholeLastLine is the prose fallback, and the
// case it exists to get right is the sentence that contains the word
// while saying the opposite.
func TestAnApprovalHasToBeTheWholeLastLine(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"looks right to me\n\nAPPROVED", true},
		{"looks right to me\nApproved.", true},
		{"I would have approved this if the loop were bounded.", false},
		{"APPROVED, but fix the naming first", false},
		{"CHANGES REQUESTED", false},
		{"", false},
		{"here is what to fix\nAPPROVED\nactually, one more thing", false},
	}
	for _, tc := range cases {
		if got := approvedByLastLine(tc.text); got != tc.want {
			t.Errorf("approvedByLastLine(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// TestADebateRefusesWhereNobodyIsWatching: a sub-agent's turn cannot open
// one. Nobody is there to read it, and a tree of debates is a bill with
// no ceiling on it.
func TestADebateRefusesWhereNobodyIsWatching(t *testing.T) {
	script := &debateScript{approveAt: 1}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	parent := startDebateSession(t, loop)
	const child = "child-1"
	if _, err := loop.Store.CreateSessionIn(child, parent, "boy", "", false); err != nil {
		t.Fatalf("create child session: %v", err)
	}

	handled, err := loop.routeDebate(context.Background(), child, "boy", "/debate girl 3 do the thing")
	if !handled || err != nil {
		t.Fatalf("routeDebate handled=%v err=%v", handled, err)
	}
	if got := debateEvents(t, loop, child, events.TypeDebateStarted); len(got) != 0 {
		t.Fatal("a sub-agent started a debate")
	}
	all, _ := loop.Store.Events(child, 0)
	last := all[len(all)-1]
	if reply := dataString(last.Data, "text"); !strings.Contains(reply, "sub-agent") {
		t.Errorf("refusal said %q, want it to name the reason", reply)
	}
}

// TestADebateRefusesAnAgentReviewingItself: naming the agent already
// running is a config mistake with an expensive failure mode, and the
// message says which names would work instead.
func TestADebateRefusesAnAgentReviewingItself(t *testing.T) {
	script := &debateScript{}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)

	handled, err := loop.routeDebate(context.Background(), sid, "boy", "/debate boy 3 do the thing")
	if !handled || err != nil {
		t.Fatalf("routeDebate handled=%v err=%v", handled, err)
	}
	if got := debateEvents(t, loop, sid, events.TypeDebateStarted); len(got) != 0 {
		t.Fatal("an agent was allowed to review itself")
	}
	all, _ := loop.Store.Events(sid, 0)
	reply := dataString(all[len(all)-1].Data, "text")
	if !strings.Contains(reply, "reviewing itself") || !strings.Contains(reply, "girl") {
		t.Errorf("refusal said %q, want the reason and the alternatives", reply)
	}
	// And not the name it just refused: "name a different one: boy, girl"
	// reads as a sentence nobody checked.
	if strings.Contains(reply, "boy, girl") || strings.Contains(reply, "girl, boy") {
		t.Errorf("refusal offered the refused agent as an alternative: %q", reply)
	}
}

// TestDebateProgressSeparatesWhatWasWrittenFromWhatWasRun: the file list
// goes to the reviewer and the call count decides a stall, and they are
// not the same question — a round spent running tests changed no file and
// is not a round where nothing happened.
func TestDebateProgressSeparatesWhatWasWrittenFromWhatWasRun(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), nil, &config.Config{})
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "boy", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	mark := loop.lastSeq(sid)

	for _, call := range []struct{ name, input string }{
		{"grep", `{"pattern":"TODO"}`},
		{"write_file", `{"path":"sum.py","content":"..."}`},
		{"edit", `{"path":"sum.py","old_string":"a","new_string":"b"}`},
		{"write_file", `{"path":"sum.py","content":"..."}`},
		{"bash", `{"command":"pytest"}`},
	} {
		store.Append(sid, events.TypeToolStart, map[string]any{"name": call.name, "input": call.input})
	}

	files, calls := loop.debateProgress(sid, mark)
	if calls != 5 {
		t.Errorf("counted %d tool calls, want 5 — every call counts for the stall check", calls)
	}
	if len(files) != 1 || files[0] != "sum.py" {
		t.Errorf("files = %v, want [sum.py] once, however many times it was written", files)
	}
}

// TestReviewerToolsKeepTheVerdictWhateverTheAgentAllows: an agent
// configured down to one tool is still a reviewer that can report, or
// the debate could never end early.
func TestReviewerToolsKeepTheVerdictWhateverTheAgentAllows(t *testing.T) {
	got := reviewerToolNames(config.AgentConfig{Tools: []string{"read_file", "bash"}})
	want := map[string]bool{"read_file": true, verdictToolName: true}
	if len(got) != len(want) {
		t.Fatalf("reviewer tools = %v, want exactly %v", got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("reviewer was given %q, which its own config does not allow or a reviewer must not have", name)
		}
	}
}

// TestStoppingADebateIsNotAFailure: a debate cancelled by the person who
// started it says so. Reporting a deliberate stop as a failure puts a red
// line under an act somebody chose.
func TestStoppingADebateIsNotAFailure(t *testing.T) {
	script := &debateScript{authorWorks: true}
	srv := script.server(t)
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancelled from under the first author turn, the way Stop does it.
	go func() {
		for {
			if len(debateEventsQuiet(loop, sid, events.TypeDebateStarted)) > 0 {
				cancel()
				return
			}
		}
	}()
	_ = loop.SendMessage(ctx, sid, "boy", "/debate girl 5 write a sum function")

	ended := debateEvents(t, loop, sid, events.TypeDebateEnded)
	if len(ended) != 1 {
		t.Fatalf("got %d debate.ended events, want 1", len(ended))
	}
	if reason, _ := ended[0].Data["reason"].(string); reason != "stopped" {
		t.Errorf("ended reason = %q, want \"stopped\"", reason)
	}
}

// debateEventsQuiet is debateEvents without a *testing.T, for a goroutine
// that must not call t.Fatalf.
func debateEventsQuiet(loop *Loop, sid string, typ events.Type) []events.Event {
	all, err := loop.Store.Events(sid, 0)
	if err != nil {
		return nil
	}
	var out []events.Event
	for _, ev := range all {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}
