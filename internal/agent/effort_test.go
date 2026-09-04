package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// effortLoop is one session on one OpenAI-compatible model, recording the
// request bodies so a test can assert on what actually went out.
func effortLoop(t *testing.T, profileEffort string) (*Loop, string, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range textChunks("answered") {
			w.Write([]byte("data: " + c + "\n\n"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(srv.Close)

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL}},
		Profiles:       map[string]config.Profile{"only": {Provider: "local", Model: "muse-glimmer-30b", Effort: profileEffort}},
		Agents:         map[string]config.AgentConfig{"boy": {Profile: "only"}},
		DefaultProfile: "only",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": provider.NewOpenAICompat(srv.URL, "")}, cfg)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "boy", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return loop, sid, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), bodies...)
	}
}

// The safety property, end to end: a configuration that says nothing
// about effort sends a request with no such field in it.
func TestNothingIsSentUntilSomebodyAsks(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "")
	if err := loop.SendMessage(context.Background(), sid, "boy", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	sent := bodies()
	if len(sent) != 1 {
		t.Fatalf("got %d requests, want 1", len(sent))
	}
	if _, present := sent[0]["reasoning_effort"]; present {
		t.Errorf("an unconfigured session sent reasoning_effort: %v", sent[0])
	}
}

func TestAProfilesEffortReachesTheRequest(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "high")
	if err := loop.SendMessage(context.Background(), sid, "boy", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := bodies()[0]["reasoning_effort"]; got != "high" {
		t.Errorf("reasoning_effort = %v, want high", got)
	}
}

// The conversation wins over the profile: the same model answering "which
// file is this in" and "why does this deadlock" wants different amounts,
// and without this the only way to have both is two profiles on one model.
func TestTheConversationOverridesTheProfile(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "low")
	if handled, err := loop.routeEffort(sid, "boy", "/effort high"); !handled || err != nil {
		t.Fatalf("routeEffort handled=%v err=%v", handled, err)
	}
	if err := loop.SendMessage(context.Background(), sid, "boy", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := bodies()[0]["reasoning_effort"]; got != "high" {
		t.Errorf("reasoning_effort = %v, want the conversation's high, not the profile's low", got)
	}

	// And back again, which is the half that gets forgotten: an override
	// with no way off is a setting somebody has to restart to undo.
	if handled, err := loop.routeEffort(sid, "boy", "/effort default"); !handled || err != nil {
		t.Fatalf("routeEffort handled=%v err=%v", handled, err)
	}
	if err := loop.SendMessage(context.Background(), sid, "boy", "again"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := bodies()[1]["reasoning_effort"]; got != "low" {
		t.Errorf("reasoning_effort = %v after clearing, want the profile's low", got)
	}
}

// It survives a restart, because a session's own answer is session state
// like the four permission switches, not a runtime flag.
func TestTheConversationsEffortIsPartOfTheSession(t *testing.T) {
	loop, sid, _ := effortLoop(t, "")
	if _, err := loop.Store.SetEffort(sid, "medium"); err != nil {
		t.Fatalf("SetEffort: %v", err)
	}
	sess, err := loop.Store.Get(sid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sess.Effort != "medium" {
		t.Errorf("session effort = %q, want medium", sess.Effort)
	}
	if got := loop.effortFor(sid, config.Profile{Effort: "low"}); got != provider.EffortMedium {
		t.Errorf("effortFor = %q, want the session's medium", got)
	}
}

// A level that is not a level is refused by name rather than stored and
// sent to a server that will reject the request.
func TestAnUnknownLevelIsRefused(t *testing.T) {
	loop, sid, _ := effortLoop(t, "")
	if handled, err := loop.routeEffort(sid, "boy", "/effort maximum"); !handled || err != nil {
		t.Fatalf("routeEffort handled=%v err=%v", handled, err)
	}
	sess, _ := loop.Store.Get(sid)
	if sess.Effort != "" {
		t.Errorf("an unknown level was stored: %q", sess.Effort)
	}
	all, _ := loop.Store.Events(sid, 0)
	if reply := dataString(all[len(all)-1].Data, "text"); !strings.Contains(reply, "off|low|medium|high") {
		t.Errorf("refusal said %q, want the levels that would work", reply)
	}
}

// A configuration with a level nobody can send should fail at load, where
// there is a person to read the message, not at the first request.
func TestAnInvalidProfileEffortFailsValidation(t *testing.T) {
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://x"}},
		Profiles:       map[string]config.Profile{"only": {Provider: "local", Model: "m", Effort: "maximum"}},
		DefaultProfile: "only",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("a profile with an unknown effort was accepted")
	}
	if !strings.Contains(err.Error(), "off, low, medium, high") {
		t.Errorf("error = %q, want it to name the levels", err)
	}
}

// The command says what the level actually reaches, because the wires
// differ and a setting that looks as though it took effect and did not is
// the failure worth spending a sentence on.
func TestTheCommandSaysWhatTheLevelReaches(t *testing.T) {
	cases := []struct {
		providerType config.ProviderType
		model        string
		level        provider.Effort
		want         string
	}{
		{config.ProviderAnthropic, "claude-opus-5", provider.EffortHigh, "on or off rather than a dial"},
		{config.ProviderAnthropic, "claude-3-5-sonnet-20241022", provider.EffortHigh, "token budget"},
		{config.ProviderBedrock, "us.anthropic.claude-opus-5-v1:0", provider.EffortHigh, "on or off rather than a dial"},
		{config.ProviderBedrock, "us.anthropic.claude-sonnet-4-5-20250929-v1:0", provider.EffortHigh, "token budget"},
		{config.ProviderOpenAICompat, "muse-glimmer-30b", provider.EffortHigh, "reasoning_effort"},
		{config.ProviderOpenAICompat, "muse-glimmer-30b", provider.EffortOff, "does whatever it does by default"},
	}
	for _, tc := range cases {
		got := effortReach(tc.providerType, tc.model, tc.level)
		if !strings.Contains(got, tc.want) {
			t.Errorf("effortReach(%s, %s, %s) = %q, want it to mention %q",
				tc.providerType, tc.model, tc.level, got, tc.want)
		}
	}
}

// Muse reads its reasoning strength from the system prompt, not from
// reasoning_effort. Until this existed "/effort high" on a muse profile
// changed a field the model ignores.
func TestMuseGetsItsReasoningStrengthInTheSystemPrompt(t *testing.T) {
	systemOf := func(body map[string]any) string {
		msgs, _ := body["messages"].([]any)
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			if mm["role"] == "system" {
				s, _ := mm["content"].(string)
				return s
			}
		}
		return ""
	}

	loop, sid, bodies := effortLoop(t, "")
	if err := loop.SendMessage(context.Background(), sid, "boy", "hello"); err != nil {
		t.Fatal(err)
	}
	if got := systemOf(bodies()[0]); strings.Contains(got, "Reasoning strength") {
		t.Errorf("an unset effort still wrote a reasoning line: %q", got)
	}

	if _, err := loop.routeEffort(sid, "boy", "/effort high"); err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(context.Background(), sid, "boy", "again"); err != nil {
		t.Fatal(err)
	}
	if got := systemOf(bodies()[1]); !strings.Contains(got, "Reasoning strength: high") {
		t.Errorf("system prompt lacks the muse line: %q", got)
	}

	// xhigh is a muse word. It reaches the system prompt as itself and the
	// wire as high, the top of that field's vocabulary.
	if _, err := loop.routeEffort(sid, "boy", "/effort xhigh"); err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(context.Background(), sid, "boy", "once more"); err != nil {
		t.Fatal(err)
	}
	b := bodies()[2]
	if got := systemOf(b); !strings.Contains(got, "Reasoning strength: xhigh") {
		t.Errorf("system prompt lacks xhigh: %q", got)
	}
	if got := b["reasoning_effort"]; got != "high" {
		t.Errorf("reasoning_effort = %v on the wire, want high (the field has no xhigh)", got)
	}
}

// The line is muse's alone, and it sits ahead of the family note.
func TestTheReasoningLineIsMusesAlone(t *testing.T) {
	if got := modelNoteFor("google/gemma-3-27b-it", provider.EffortHigh); strings.Contains(got, "Reasoning strength") {
		t.Errorf("gemma got a muse line: %q", got)
	}
	got := modelNoteFor("Muse-Glimmer-30B", provider.EffortXHigh)
	if !strings.HasPrefix(got, "Reasoning strength: xhigh") {
		t.Errorf("the line is not first: %q", got)
	}
	if !strings.Contains(got, "Working style") {
		t.Errorf("the family note went missing: %q", got)
	}
	if got := modelNoteFor("Muse-Glimmer-30B", provider.EffortOff); strings.Contains(got, "Reasoning strength") {
		t.Errorf("off wrote a line: %q", got)
	}
}
