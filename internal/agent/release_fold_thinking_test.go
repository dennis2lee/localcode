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

// Reasoning is still never logged: folding it is a way of drawing it,
// not a reason to keep it.
func TestFoldedReasoningIsStillNotLogged(t *testing.T) {
	server := reasoningServer(t)
	loop := foldLoop(t, server.URL, "muse-glimmer")
	streamedEvents(t, loop, "s1")
	logged, err := loop.Store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range logged {
		if ev.Type == events.TypeThinkingDelta || ev.Type == events.TypeThinkingEnd {
			t.Errorf("%s was written to the log", ev.Type)
		}
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
