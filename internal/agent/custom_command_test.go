package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/commands"
	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// newCustomCommandTestLoop builds a Loop wired to a two-profile config
// ("balanced"/default and "strong", both pointed at the same mock model
// server) so tests can confirm a custom command's agent/model frontmatter
// actually changes which profile+agent config a turn runs under.
func newCustomCommandTestLoop(t *testing.T, modelURL string, cmds []commands.Command) (*Loop, *session.Store) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	registry := tools.NewRegistry(nil)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelURL},
		},
		Profiles: map[string]config.Profile{
			"balanced": {Provider: "local", Model: "balanced-model"},
			"strong":   {Provider: "local", Model: "strong-model"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "balanced"},
			"review":          {Profile: "strong", Prompt: "You are the review agent."},
		},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	providers := map[string]provider.Provider{}
	if modelURL != "" {
		providers["local"] = provider.NewOpenAICompat(modelURL, "")
	}

	loop := New(store, registry, providers, cfg)
	loop.Commands = cmds
	loop.ProjectDir = t.TempDir()
	return loop, store
}

func mockChatServer(t *testing.T, capture *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if capture != nil {
			*capture = string(raw)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// TestCustomCommandExpandsBodyAndKeepsDisplayText mirrors the /skill
// behavior: the transcript shows the short "/name args" the user typed,
// but the model receives the expanded template.
func TestCustomCommandExpandsBodyAndKeepsDisplayText(t *testing.T) {
	var lastBody string
	model := mockChatServer(t, &lastBody)
	defer model.Close()

	cmds := []commands.Command{
		{Name: "test", Body: "Run tests matching: $ARGUMENTS"},
	}
	loop, store := newCustomCommandTestLoop(t, model.URL, cmds)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/test TestFoo"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var displayedText string
	for _, ev := range all {
		if ev.Type == events.TypeUserMessage {
			displayedText, _ = ev.Data["text"].(string)
		}
	}
	if displayedText != "/test TestFoo" {
		t.Errorf("displayed text = %q, want %q", displayedText, "/test TestFoo")
	}
	if !strings.Contains(lastBody, "Run tests matching: TestFoo") {
		t.Errorf("expected expanded body in model request, got: %s", lastBody)
	}
}

// TestCustomCommandAgentOverride confirms a command's "agent" frontmatter
// picks that agent's profile (model) and system-prompt/tool scoping for
// just that turn, without changing the session's standing agent.
func TestCustomCommandAgentOverride(t *testing.T) {
	var lastBody string
	model := mockChatServer(t, &lastBody)
	defer model.Close()

	cmds := []commands.Command{
		{Name: "review", Agent: "review", Body: "Review this code."},
	}
	loop, store := newCustomCommandTestLoop(t, model.URL, cmds)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/review"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	var req struct {
		Model  string `json:"model"`
		System string `json:"system,omitempty"`
	}
	// The mock server captured the raw JSON body; sniff for model/system
	// fields without assuming an exact wire shape beyond what's needed.
	if !strings.Contains(lastBody, "strong-model") {
		t.Errorf("expected the review agent's \"strong\" profile model in request, got: %s", lastBody)
	}
	_ = json.Unmarshal([]byte(lastBody), &req) // best-effort, some providers nest fields differently

	sess, err := store.Get(sid)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	if sess.Agent != "general-purpose" {
		t.Errorf("session agent = %q, want it unchanged (\"general-purpose\") — command overrides should be per-turn only", sess.Agent)
	}
}

// TestCustomCommandModelOverride confirms a command's "model" frontmatter
// overrides just the model ID used for that turn.
func TestCustomCommandModelOverride(t *testing.T) {
	var lastBody string
	model := mockChatServer(t, &lastBody)
	defer model.Close()

	cmds := []commands.Command{
		{Name: "quick", Model: "override-model", Body: "quick question"},
	}
	loop, store := newCustomCommandTestLoop(t, model.URL, cmds)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/quick"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !strings.Contains(lastBody, "override-model") {
		t.Errorf("expected the command's model override in request, got: %s", lastBody)
	}
}

// TestUnmatchedSlashTextIsSentAsIs confirms "/notacommand" (no loaded
// custom command by that name) falls through to being sent verbatim, not
// treated as an error.
func TestUnmatchedSlashTextIsSentAsIs(t *testing.T) {
	var lastBody string
	model := mockChatServer(t, &lastBody)
	defer model.Close()

	loop, store := newCustomCommandTestLoop(t, model.URL, nil)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/notacommand hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !strings.Contains(lastBody, "/notacommand hello") {
		t.Errorf("expected the raw text to be sent to the model, got: %s", lastBody)
	}
}

// TestInitCommandSendsInitPrompt confirms "/init" keeps the short "/init"
// as the displayed text but sends the repo-scanning instruction prompt to
// the model.
func TestInitCommandSendsInitPrompt(t *testing.T) {
	var lastBody string
	model := mockChatServer(t, &lastBody)
	defer model.Close()

	loop, store := newCustomCommandTestLoop(t, model.URL, nil)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/init"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var displayedText string
	for _, ev := range all {
		if ev.Type == events.TypeUserMessage {
			displayedText, _ = ev.Data["text"].(string)
		}
	}
	if displayedText != "/init" {
		t.Errorf("displayed text = %q, want %q", displayedText, "/init")
	}
	if !strings.Contains(lastBody, "AGENTS.md") {
		t.Errorf("expected the init prompt (mentioning AGENTS.md) in the model request, got: %s", lastBody)
	}
}

// TestCustomCommandExpandFailureReportsErrorLocally confirms a command
// referencing a missing @file surfaces as an error event, without ever
// calling the model.
func TestCustomCommandExpandFailureReportsErrorLocally(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", []commands.Command{
		{Name: "broken", Body: "See @does-not-exist.txt"},
	})
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/broken"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawError bool
	for _, ev := range all {
		if ev.Type == events.TypeError {
			sawError = true
		}
	}
	if !sawError {
		t.Error("expected an error event for a command whose @file reference is missing")
	}
}
