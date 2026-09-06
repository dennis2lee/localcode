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
	"localcode/internal/events"
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
	if _, err := loop.Store.SetEffort(sid, "m", "medium"); err != nil {
		t.Fatalf("SetEffort: %v", err)
	}
	sess, err := loop.Store.Get(sid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if sess.Efforts["m"] != "medium" {
		t.Errorf("session efforts = %v, want m:medium", sess.Efforts)
	}
	if got := loop.effortFor(sid, config.Profile{Model: "m", Effort: "low"}); got != provider.EffortMedium {
		t.Errorf("effortFor = %q, want the session's medium", got)
	}
}

// The answer is kept per model, because one conversation can change
// model — by the agent dropdown, or by a fallback nobody asked for — and
// the amount of reasoning that suits a model belongs to the model as
// much as to the work. Picking xhigh for a muse and then switching to a
// Claude used to carry a word that family has no step for, and switching
// back had lost the muse's answer.
func TestEachModelInAConversationKeepsItsOwnLevel(t *testing.T) {
	loop, sid, _ := effortLoop(t, "")
	if _, err := loop.Store.SetEffort(sid, "muse-glimmer-30b", "xhigh"); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Store.SetEffort(sid, "claude-sonnet-5", "off"); err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]provider.Effort{
		"muse-glimmer-30b": provider.EffortXHigh,
		"claude-sonnet-5":  provider.EffortOff,
	} {
		if got := loop.effortFor(sid, config.Profile{Model: model}); got != want {
			t.Errorf("effortFor(%s) = %q, want %q", model, got, want)
		}
	}
	// A model nobody has answered for falls through to the profile.
	if got := loop.effortFor(sid, config.Profile{Model: "gemma-3-27b-it", Effort: "low"}); got != provider.EffortLow {
		t.Errorf("effortFor(a model with no answer) = %q, want the profile's low", got)
	}
	// And clearing one leaves the other alone.
	if _, err := loop.Store.SetEffort(sid, "muse-glimmer-30b", ""); err != nil {
		t.Fatal(err)
	}
	if got := loop.effortFor(sid, config.Profile{Model: "muse-glimmer-30b", Effort: "low"}); got != provider.EffortLow {
		t.Errorf("after clearing, effortFor = %q, want the profile's low", got)
	}
	if got := loop.effortFor(sid, config.Profile{Model: "claude-sonnet-5"}); got != provider.EffortOff {
		t.Errorf("clearing one model's answer took another's: %q", got)
	}
}

// A conversation that set a level before this was per model keeps it,
// for every model, until it answers for one.
func TestTheAnswerFromBeforeThisWasPerModelStillCounts(t *testing.T) {
	loop, sid, _ := effortLoop(t, "")
	if _, err := loop.Store.SetEffort(sid, "", "high"); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"muse-glimmer-30b", "claude-sonnet-5"} {
		if got := loop.effortFor(sid, config.Profile{Model: model}); got != provider.EffortHigh {
			t.Errorf("effortFor(%s) = %q, want the conversation-wide high", model, got)
		}
	}
	if _, err := loop.Store.SetEffort(sid, "claude-sonnet-5", "off"); err != nil {
		t.Fatal(err)
	}
	if got := loop.effortFor(sid, config.Profile{Model: "claude-sonnet-5"}); got != provider.EffortOff {
		t.Errorf("the per-model answer did not win: %q", got)
	}
	if got := loop.effortFor(sid, config.Profile{Model: "muse-glimmer-30b"}); got != provider.EffortHigh {
		t.Errorf("answering for one model changed another: %q", got)
	}
}

// What a control should offer on each model: the levels that reach the
// wire as different requests, and no more.
func TestTheLevelsOfferedAreTheOnesThatDoSomething(t *testing.T) {
	all := []provider.Effort{provider.EffortOff, provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh}
	toHigh := []provider.Effort{provider.EffortOff, provider.EffortLow, provider.EffortMedium, provider.EffortHigh}
	onOff := []provider.Effort{provider.EffortOff, provider.EffortHigh}
	// Room for every budget: 16384 for high, plus what the adapter
	// reserves for the answer itself.
	const roomy = 64000
	for _, c := range []struct {
		name      string
		provider  config.ProviderType
		model     string
		maxTokens int
		want      []provider.Effort
	}{
		{"an adaptive Claude has one switch", config.ProviderAnthropic, "claude-sonnet-5", roomy, onOff},
		{"the same on Bedrock", config.ProviderBedrock, "claude-opus-5", roomy, onOff},
		{"an older Claude takes a budget per level", config.ProviderAnthropic, "claude-3-5-sonnet", roomy, toHigh},
		{"muse has a word for xhigh", config.ProviderOpenAICompat, "muse-glimmer-30b", roomy, all},
		{"another local model stops at high", config.ProviderOpenAICompat, "gemma-3-27b-it", roomy, toHigh},
	} {
		got := effortLevelsFor(c.provider, c.model, c.maxTokens)
		if len(got) != len(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %v, want %v", c.name, got, c.want)
				break
			}
		}
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

// On a model that takes a budget per level, the cap decides how many
// levels there are.
//
// Every budget is clamped to the room the reply cap leaves, so on an
// ordinary max_tokens medium and high arrive as the same number — and a
// control that offered both would be a dial over a switch, which is the
// thing this list exists to prevent. Built from what the wire would
// carry rather than from a literal.
func TestTheReplyCapDecidesHowManyLevelsAnOlderClaudeHas(t *testing.T) {
	const model = "claude-3-5-sonnet"
	for _, c := range []struct {
		name      string
		maxTokens int
		want      []provider.Effort
	}{
		{"room for every budget", 64000, []provider.Effort{provider.EffortOff, provider.EffortLow, provider.EffortMedium, provider.EffortHigh}},
		// 1024 of every cap is reserved for the answer, so the room a
		// budget is clamped to is max_tokens less that.
		{"high clamped onto medium", 8192 + 1024, []provider.Effort{provider.EffortOff, provider.EffortLow, provider.EffortMedium}},
		{"medium and high both clamped onto low", 2048 + 1024, []provider.Effort{provider.EffortOff, provider.EffortLow}},
		{"no room to think at all", 1200, []provider.Effort{provider.EffortOff}},
	} {
		got := effortLevelsFor(config.ProviderAnthropic, model, c.maxTokens)
		if len(got) != len(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

// Every path that changes the level says so, because two of them do:
// the HTTP route the controls use, and the "/effort" command. A route
// that announces beside a command that does not is a readout that is
// right half the time — the pill and the footer would go on naming the
// level somebody had just changed.
func TestChangingTheLevelIsAlwaysAnnounced(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*Loop, string)
	}{
		{"the control", func(l *Loop, sid string) { _, _ = l.SetSessionEffort(sid, "high") }},
		{"the command", func(l *Loop, sid string) { _, _ = l.routeEffort(sid, "general-purpose", "/effort high") }},
		{"the command, clearing", func(l *Loop, sid string) { _, _ = l.routeEffort(sid, "general-purpose", "/effort default") }},
	} {
		loop, sid, _ := effortLoop(t, "")
		before := countEffortEvents(t, loop, sid)
		c.set(loop, sid)
		if got := countEffortEvents(t, loop, sid); got <= before {
			t.Errorf("%s changed the level without announcing it", c.name)
		}
	}
}

func countEffortEvents(t *testing.T, loop *Loop, sessionID string) int {
	t.Helper()
	evs, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	n := 0
	for _, ev := range evs {
		if ev.Type == events.TypeEffortChanged {
			n++
		}
	}
	return n
}

// The same levels are refused whichever way one is asked for.
//
// "/effort xhigh" on a model with no such step used to store it, while
// the picker refused it — and the stored word showed in both readouts
// and was never true of a request.
func TestTheCommandRefusesWhatTheControlRefuses(t *testing.T) {
	loop, sid, _ := effortLoop(t, "")
	// Not muse: the reasoning_effort vocabulary stops at high, so xhigh
	// is a word this model does not tell apart.
	const model = "gemma-3-27b-it"
	loop.Config.Profiles["only"] = config.Profile{Provider: "local", Model: model}

	if _, err := loop.routeEffort(sid, "boy", "/effort xhigh"); err != nil {
		t.Fatalf("routeEffort: %v", err)
	}
	if got := loop.Store.EffortFor(sid, model); got == "xhigh" {
		t.Error("the command stored a level this model does not tell apart")
	}
	if got := loop.effortFor(sid, config.Profile{Model: model}); got == provider.EffortXHigh {
		t.Error("the refused level reached a request")
	}
	// And a level it does tell apart still lands.
	if _, err := loop.routeEffort(sid, "boy", "/effort high"); err != nil {
		t.Fatalf("routeEffort: %v", err)
	}
	if got := loop.Store.EffortFor(sid, model); got != "high" {
		t.Errorf("a level the model tells apart was not stored: %q", got)
	}
}
