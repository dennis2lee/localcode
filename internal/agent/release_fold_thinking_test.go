package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// reasoningServer answers every request the way LM Studio answers a muse
// model: the reasoning as reasoning_content, then the answer as content.
func reasoningServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"choices":[{"delta":{"reasoning_content":"The user asks 17 times 23. "}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"That is 391."}}]}`,
			`{"choices":[{"delta":{"content":"17 times 23 is 391."}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(server.Close)
	return server
}

// foldLoop is a loop whose one profile runs model against server.
func foldLoop(t *testing.T, serverURL, model string) *Loop {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: serverURL},
		},
		Profiles: map[string]config.Profile{
			"main": {Provider: "local", Model: model},
		},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "main"}},
		DefaultProfile: "main",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	return New(store, tools.NewRegistry(nil),
		map[string]provider.Provider{"local": provider.NewOpenAICompat(serverURL, "")}, cfg)
}

// streamedEvents runs one prompt and returns everything a watching
// client received, broadcasts included, in order.
func streamedEvents(t *testing.T, loop *Loop, sid string) []events.Event {
	t.Helper()
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ch, _, unsubscribe, err := loop.Store.Subscribe(sid)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	var seen []events.Event
	done := make(chan struct{})
	go func() {
		for ev := range ch {
			seen = append(seen, ev)
		}
		close(done)
	}()
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "what is 17 times 23?"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	unsubscribe()
	<-done
	return seen
}

// A muse model's reasoning arrives marked for folding, with the display
// switch beside it and the block's time on its end; the end comes before
// the answer's first fragment, which is what folds the block in time.
func TestAMuseModelsReasoningIsMarkedForFolding(t *testing.T) {
	server := reasoningServer(t)
	loop := foldLoop(t, server.URL, "meta/muse-glimmer-30b")

	var deltas, ends int
	endBeforeAnswer := false
	for _, ev := range streamedEvents(t, loop, "s1") {
		switch ev.Type {
		case events.TypeThinkingDelta:
			deltas++
			if ev.Data["fold"] != true {
				t.Errorf("thinking.delta fold = %v, want true for a muse model", ev.Data["fold"])
			}
			if ev.Data["show_thinking"] != true {
				t.Errorf("thinking.delta show_thinking = %v, want true", ev.Data["show_thinking"])
			}
		case events.TypeThinkingEnd:
			ends++
			if ev.Data["fold"] != true {
				t.Errorf("thinking.end fold = %v, want true", ev.Data["fold"])
			}
			ms, ok := ev.Data["elapsed_ms"].(int)
			if !ok || ms < 0 {
				t.Errorf("thinking.end elapsed_ms = %#v, want a non-negative int", ev.Data["elapsed_ms"])
			}
		case events.TypeMessagePartDelta:
			if ends == 1 {
				endBeforeAnswer = true
			}
		}
	}
	if deltas != 2 || ends != 1 {
		t.Fatalf("saw %d deltas and %d ends, want 2 and 1", deltas, ends)
	}
	if !endBeforeAnswer {
		t.Error("the reasoning's end did not arrive before the answer started")
	}
}

// The family rule and the switch each decide it: another model, or the
// switch off, and the reasoning is drawn the way it always was.
func TestReasoningIsNotFoldedOffTheFamilyOrWithTheSwitchOff(t *testing.T) {
	server := reasoningServer(t)
	cases := []struct {
		name  string
		model string
		fold  bool
		want  bool
	}{
		{"muse, switch on", "Muse-Glimmer-30B-GGUF", true, true},
		{"muse, switch off", "meta/muse-glimmer-30b", false, false},
		{"another family, switch on", "qwen3-30b-a3b", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loop := foldLoop(t, server.URL, c.model)
			loop.SetFoldThinkingEnabled(c.fold)
			saw := false
			for _, ev := range streamedEvents(t, loop, "s1") {
				if ev.Type != events.TypeThinkingDelta && ev.Type != events.TypeThinkingEnd {
					continue
				}
				saw = true
				if got, _ := ev.Data["fold"].(bool); got != c.want {
					t.Errorf("%s fold = %v, want %v", ev.Type, got, c.want)
				}
			}
			if !saw {
				t.Fatal("no reasoning reached the client at all")
			}
		})
	}
}

// show_thinking rides on each delta, so a client that keeps no copy of
// the settings (the TUI) still knows not to draw.
func TestReasoningCarriesTheDisplaySwitch(t *testing.T) {
	server := reasoningServer(t)
	loop := foldLoop(t, server.URL, "muse-glimmer")
	loop.SetShowThinking(false)
	for _, ev := range streamedEvents(t, loop, "s1") {
		if ev.Type == events.TypeThinkingDelta && ev.Data["show_thinking"] != false {
			t.Errorf("thinking.delta show_thinking = %v with the switch off", ev.Data["show_thinking"])
		}
	}
}

// A fold block is logged once, whole, as one thinking.block with its
// time, ahead of the answer it came before; the deltas and the end stay
// broadcast. Other models' reasoning is not logged at all.
func TestAFoldBlockIsLoggedWholeAndOnce(t *testing.T) {
	server := reasoningServer(t)
	loop := foldLoop(t, server.URL, "muse-glimmer")
	streamedEvents(t, loop, "s1")
	logged, err := loop.Store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	blocks, blockAt, answerAt := 0, -1, -1
	for i, ev := range logged {
		switch ev.Type {
		case events.TypeThinkingDelta, events.TypeThinkingEnd:
			t.Errorf("%s was written to the log", ev.Type)
		case events.TypeThinkingBlock:
			blocks++
			blockAt = i
			if got := ev.Data["text"]; got != "The user asks 17 times 23. That is 391." {
				t.Errorf("logged text = %q, want the whole reasoning", got)
			}
			if ms, ok := ev.Data["elapsed_ms"].(int); !ok || ms < 0 {
				t.Errorf("elapsed_ms = %#v", ev.Data["elapsed_ms"])
			}
		case events.TypeMessagePartEnd:
			answerAt = i
		}
	}
	if blocks != 1 || blockAt > answerAt {
		t.Errorf("logged %d blocks at %d with the answer at %d, want one before the answer", blocks, blockAt, answerAt)
	}

	plain := foldLoop(t, server.URL, "qwen3-30b-a3b")
	streamedEvents(t, plain, "s1")
	logged, _ = plain.Store.Events("s1", 0)
	for _, ev := range logged {
		if ev.Type == events.TypeThinkingBlock {
			t.Error("another model's reasoning was logged")
		}
	}
}

// foldScriptProvider streams exactly the events it is given, for the
// endings a real server does not produce on demand: a stream that dies
// mid-thought, one that closes with a block open, a block of whitespace.
type foldScriptProvider struct{ evs []provider.StreamEvent }

func (p foldScriptProvider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, len(p.evs))
	for _, ev := range p.evs {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func foldScriptLoop(t *testing.T, model string, evs ...provider.StreamEvent) *Loop {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"main": {Provider: "local", Model: model}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "main"}},
		DefaultProfile: "main",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": foldScriptProvider{evs: evs}}, cfg)
}

func loggedBlocks(t *testing.T, l *Loop, sid string) []events.Event {
	t.Helper()
	logged, err := l.Store.Events(sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []events.Event
	for _, ev := range logged {
		if ev.Type == events.TypeThinkingBlock {
			out = append(out, ev)
		}
	}
	return out
}

func thinkingDeltaEv(s string) provider.StreamEvent {
	return provider.StreamEvent{Type: provider.EventThinkingDelta, ThinkingDelta: s}
}

// A stream that dies mid-thought records the block as far as it got,
// before the failure, as it was shown live.
func TestABlockCutOffByAnErrorIsLoggedAsFarAsItGot(t *testing.T) {
	l := foldScriptLoop(t, "muse-glimmer",
		thinkingDeltaEv("half a "),
		thinkingDeltaEv("thought"),
		provider.StreamEvent{Type: provider.EventError, Err: fmt.Errorf("connection reset")},
	)
	if _, err := l.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	_ = l.SendMessage(context.Background(), "s1", "general-purpose", "go")
	b := loggedBlocks(t, l, "s1")
	if len(b) != 1 || b[0].Data["text"] != "half a thought" {
		t.Fatalf("blocks = %#v, want the partial block", b)
	}
	logged, _ := l.Store.Events("s1", 0)
	blockAt, errAt := -1, -1
	for i, ev := range logged {
		switch ev.Type {
		case events.TypeThinkingBlock:
			blockAt = i
		case events.TypeError:
			if errAt < 0 {
				errAt = i
			}
		}
	}
	if blockAt < 0 || errAt < 0 || blockAt > errAt {
		t.Errorf("the block at %d is not ahead of the error at %d", blockAt, errAt)
	}
}

// A stream that closes with a block open, as a stop mid-thought leaves
// it, records the block too.
func TestABlockLeftOpenWhenTheStreamClosesIsLogged(t *testing.T) {
	l := foldScriptLoop(t, "muse-glimmer",
		thinkingDeltaEv("still thinking when it stopped"),
		provider.StreamEvent{Type: provider.EventMessageStop, StopReason: "end_turn"},
	)
	if _, err := l.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	_ = l.SendMessage(context.Background(), "s1", "general-purpose", "go")
	if b := loggedBlocks(t, l, "s1"); len(b) != 1 || b[0].Data["text"] != "still thinking when it stopped" {
		t.Fatalf("blocks = %#v", b)
	}
}

// Whitespace alone is not a block: neither client draws one, so none is
// kept.
func TestWhitespaceReasoningIsNotLogged(t *testing.T) {
	l := foldScriptLoop(t, "muse-glimmer",
		thinkingDeltaEv("\n\n"),
		provider.StreamEvent{Type: provider.EventThinkingEnd},
		provider.StreamEvent{Type: provider.EventTextDelta, TextDelta: "ok"},
		provider.StreamEvent{Type: provider.EventMessageStop, StopReason: "end_turn"},
	)
	if _, err := l.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	_ = l.SendMessage(context.Background(), "s1", "general-purpose", "go")
	if b := loggedBlocks(t, l, "s1"); len(b) != 0 {
		t.Fatalf("whitespace was logged: %#v", b)
	}
}

// Reasoning kept in the record never reaches the model: a history rebuilt
// from the log after a restart holds none, the same as the live one.
func TestALoggedBlockIsNotRebuiltIntoTheHistory(t *testing.T) {
	server := reasoningServer(t)
	loop := foldLoop(t, server.URL, "muse-glimmer")
	streamedEvents(t, loop, "s1")
	logged, err := loop.Store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rehydrateHistory(logged) {
		for _, b := range m.Content {
			if b.Type == provider.BlockThinking || strings.Contains(b.Text, "The user asks 17 times 23") {
				t.Errorf("the rebuilt history carries reasoning: %#v", b)
			}
		}
	}
}

// /export sets the block folded, as the clients draw it.
func TestExportFoldsALoggedBlock(t *testing.T) {
	server := reasoningServer(t)
	loop := foldLoop(t, server.URL, "muse-glimmer")
	streamedEvents(t, loop, "s1")
	logged, err := loop.Store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := range logged {
		if logged[i].Type == events.TypeThinkingBlock {
			logged[i].Data["elapsed_ms"] = 65000
		}
	}
	out := renderTranscript("t", "s1", logged)
	if !strings.Contains(out, "<details><summary>Thought for 1m5s</summary>") ||
		!strings.Contains(out, "> The user asks 17 times 23. That is 391.") {
		t.Errorf("the export does not fold the reasoning:\n%s", out)
	}
	if strings.Index(out, "Thought for") > strings.Index(out, "17 times 23 is 391.") {
		t.Errorf("the reasoning is exported after the answer:\n%s", out)
	}
}

// "/fold-thinking" flips the switch, says its scope and whether this
// conversation is on a model it reaches, saves it, and tells every
// client.
func TestFoldThinkingCommandTogglesSavesAndAnnounces(t *testing.T) {
	server := reasoningServer(t)
	loop := foldLoop(t, server.URL, "meta/muse-glimmer-30b")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"show_tps": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loop.ConfigPath = path
	announced := 0
	loop.OnSettingsChanged = func() { announced++ }
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	out := replyTo(t, loop, sid, "/fold-thinking off")
	if !strings.Contains(out, "fold_thinking: off") {
		t.Errorf("reply = %q", out)
	}
	if loop.FoldThinkingEnabled() {
		t.Error("the switch did not turn off")
	}
	if !strings.Contains(out, `"muse"`) {
		t.Errorf("the reply does not state the muse-only scope: %q", out)
	}
	if !strings.Contains(out, "meta/muse-glimmer-30b, which the switch applies to") {
		t.Errorf("the reply does not say this conversation's model is reached: %q", out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"fold_thinking": false`) || !strings.Contains(string(raw), `"show_tps": true`) {
		t.Errorf("config.json after the toggle:\n%s", raw)
	}
	if announced != 1 {
		t.Errorf("settings announced %d times, want 1", announced)
	}

	if out := replyTo(t, loop, sid, "/fold-thinking"); !strings.Contains(out, "fold_thinking: on") {
		t.Errorf("a bare toggle said %q", out)
	}
	if out := replyTo(t, loop, sid, "/fold-thinking sideways"); !strings.Contains(out, "usage: /fold-thinking [on|off]") {
		t.Errorf("a bad argument said %q", out)
	}
}

// On a conversation whose model is not a muse, the reply says the switch
// changes nothing there, and with no muse profile at all, that nothing
// changes until one exists.
func TestFoldThinkingCommandSaysWhenItReachesNothing(t *testing.T) {
	server := reasoningServer(t)
	loop := foldLoop(t, server.URL, "qwen3-30b-a3b")
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	loop.SetShowThinking(false)
	out := replyTo(t, loop, sid, "/fold-thinking on")
	for _, want := range []string{
		"qwen3-30b-a3b, which is not a muse model",
		"no configured profile currently runs a muse model",
		"show_thinking is off",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("reply lacks %q:\n%s", want, out)
		}
	}
}

// MuseProfiles is what the settings window's Muse tab names, by the same
// rule the switches are applied with, in a stable order.
func TestMuseProfilesNamesTheFamilyInOrder(t *testing.T) {
	loop := foldLoop(t, "http://127.0.0.1:1", "qwen3")
	loop.Config.Profiles["zeta"] = config.Profile{Provider: "local", Model: "Muse-Spark"}
	loop.Config.Profiles["alpha"] = config.Profile{Provider: "local", Model: "meta/muse-glimmer-30b"}
	if got, want := loop.MuseProfiles(), []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MuseProfiles = %v, want %v", got, want)
	}
}

// The reply names the model the next turn will run, resolved the way the
// turn resolves it. A Smart Agent specialist runs on its lane's profile,
// which is not the default profile, and the reply read the default and
// said the opposite of what the turn then did.
func TestFoldThinkingReplyNamesASpecialistsLaneModel(t *testing.T) {
	server := reasoningServer(t)
	loop := foldLoop(t, server.URL, "qwen3-30b-a3b")
	loop.Config.Profiles["smart-deep"] = config.Profile{Provider: "local", Model: "meta/muse-glimmer-30b"}
	loop.SetSmartAgentEnabled(true)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "oracle", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "oracle", "/fold-thinking on"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	evs, err := loop.Store.Events(sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out string
	for _, ev := range evs {
		if ev.Type == events.TypeMessagePartEnd {
			out, _ = ev.Data["text"].(string)
		}
	}
	if !strings.Contains(out, "meta/muse-glimmer-30b, which the switch applies to") {
		t.Errorf("the reply does not name the specialist's lane model:\n%s", out)
	}
}

// /thinking and /timestamps tell every client once. announceConfig does,
// and a second call beside it sent the same snapshot twice, which the TUI
// answers with a roster request each time.
func TestTheDisplaySwitchesAnnounceOnce(t *testing.T) {
	for _, cmd := range []string{"/thinking off", "/timestamps on"} {
		loop := foldLoop(t, "http://127.0.0.1:1", "qwen3")
		announced := 0
		loop.OnSettingsChanged = func() { announced++ }
		if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
		replyTo(t, loop, "s1", cmd)
		if announced != 1 {
			t.Errorf("%s announced %d times, want 1", cmd, announced)
		}
	}
}

// A server that puts a muse model's reasoning inside the answer, as
// <think>…</think> (LM Studio with its separate-reasoning setting off):
// the reasoning is folded and kept like any other, and the answer that is
// logged, and later sent back to the model, carries none of it.
func TestInlineReasoningFromAMuseServerIsFolded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"choices":[{"delta":{"content":"<think>How to make lookup function with binary search?"}}]}`,
			`{"choices":[{"delta":{"content":" We need to answer.</think>\n\n"}}]}`,
			`{"choices":[{"delta":{"content":"Use bisect."}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(server.Close)
	loop := foldLoop(t, server.URL, "Muse-Glimmer-30B")
	folded := false
	for _, ev := range streamedEvents(t, loop, "s1") {
		if ev.Type == events.TypeThinkingDelta && ev.Data["fold"] == true {
			folded = true
		}
	}
	if !folded {
		t.Error("the inline reasoning was not streamed as a fold block")
	}
	b := loggedBlocks(t, loop, "s1")
	if len(b) != 1 || b[0].Data["text"] != "How to make lookup function with binary search? We need to answer." {
		t.Errorf("logged blocks = %#v", b)
	}
	logged, _ := loop.Store.Events("s1", 0)
	for _, ev := range logged {
		if ev.Type == events.TypeMessagePartEnd && ev.Data["text"] != "Use bisect." {
			t.Errorf("the logged answer is %q, want the answer alone", ev.Data["text"])
		}
	}
	for _, m := range loop.history("s1") {
		for _, blk := range m.Content {
			if blk.Type == provider.BlockText && strings.Contains(blk.Text, "<think>") {
				t.Errorf("the history the model is sent carries the reasoning: %q", blk.Text)
			}
		}
	}
}

// /llm-doctor says where the server puts the reasoning when it puts it in
// the answer, and a baseline taken the other way is a difference; a
// baseline that never recorded it, or a run that saw no reasoning, is
// not.
func TestTheDoctorSaysWhenReasoningIsInline(t *testing.T) {
	run := doctorRun{Model: "muse", BaseURL: "http://127.0.0.1:1234/v1"}
	run.Server.ReasoningPlacement = "inline"
	if out := doctorReport(run, nil, "", nil, false); !strings.Contains(out, "reasoning: inside the answer as <think>") {
		t.Errorf("the report does not say where the reasoning was:\n%s", out)
	}
	moved := func(base doctorRun) bool {
		for _, d := range doctorDiff(run, base) {
			if strings.HasPrefix(d, "reasoning ") {
				return true
			}
		}
		return false
	}
	field := doctorRun{Model: "muse"}
	field.Server.ReasoningPlacement = "field"
	if !moved(field) {
		t.Errorf("a move from the field into the answer is not a difference: %v", doctorDiff(run, field))
	}
	if moved(doctorRun{Model: "muse"}) {
		t.Error("a baseline that never recorded the placement reads as a move")
	}
	quiet := doctorRun{Model: "muse"}
	if placementChanged(field.Server, quiet.Server) {
		t.Error("a run that saw no reasoning reads as a move")
	}
}

// The verdict's wording follows where the reasoning was: a <think> block
// localcode split off is not reasoning_content, and a model that stopped
// inside its block is not a server whose output channel closed.
func TestTheDoctorWordsInlineReasoningAsTheBlock(t *testing.T) {
	var exact doctorCanary
	for _, c := range doctorCanaries {
		if c.name == "exact_reply" {
			exact = c
		}
	}
	if exact.name == "" {
		t.Fatal("no exact_reply canary")
	}
	inline := provider.RawReply{Reasoning: "the word is OK", ReasoningInline: true, FinishReason: "stop"}
	_, _, why := doctorJudgeReply(exact, inline)
	if strings.Contains(why, "reasoning_content") || strings.Contains(why, "output channel") {
		t.Errorf("inline reasoning worded as the server's field: %q", why)
	}
	inline.FinishReason = "length"
	if _, inc, why := doctorJudgeReply(exact, inline); !inc || !strings.Contains(why, "<think> block") {
		t.Errorf("an exhausted inline budget: inconclusive=%v %q", inc, why)
	}
	field := provider.RawReply{Reasoning: "the word is OK", ReasoningInField: true, FinishReason: "stop"}
	if _, _, why := doctorJudgeReply(exact, field); !strings.Contains(why, "reasoning_content") {
		t.Errorf("field reasoning lost its wording: %q", why)
	}
}

// /thinking says a muse model's blocks stay in the log only while that
// is true: with fold_thinking off nothing is logged, and saying it is
// would be telling somebody their reasoning is kept when it is not.
func TestTheThinkingReplySaysWhatIsKept(t *testing.T) {
	for _, fold := range []bool{true, false} {
		loop := foldLoop(t, "http://127.0.0.1:1", "muse-glimmer")
		loop.SetFoldThinkingEnabled(fold)
		if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
		out := replyTo(t, loop, "s1", "/thinking off")
		if kept := strings.Contains(out, "kept in the log"); kept != fold {
			t.Errorf("fold_thinking %v: the reply says the log keeps the blocks = %v:\n%s", fold, kept, out)
		}
	}
}
