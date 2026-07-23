package agent

import (
	"context"
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
	"localcode/internal/skills"
	"localcode/internal/tools"
)

func newSkillTestLoop(t *testing.T, modelURL string) (*Loop, *session.Store) {
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
			"balanced": {Provider: "local", Model: "test-model"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "balanced"},
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
	loop.Skills = []skills.Skill{
		{Name: "pdf-tools", Description: "Work with PDF files", Body: "# PDF Tools\nMerge and split PDFs."},
	}
	return loop, store
}

// TestSkillCommandList confirms "/skill" answers locally (no model call —
// modelURL is empty and would error if Chat were ever invoked) with the
// name/description index.
func TestSkillCommandList(t *testing.T) {
	loop, store := newSkillTestLoop(t, "")
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/skill"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	var sawUserMsg bool
	var listing string
	for _, ev := range all {
		switch ev.Type {
		case events.TypeUserMessage:
			sawUserMsg = true
			if text, _ := ev.Data["text"].(string); text != "/skill" {
				t.Errorf("message.user text = %q, want %q", text, "/skill")
			}
		case events.TypeMessagePartEnd:
			listing, _ = ev.Data["text"].(string)
		}
	}
	if !sawUserMsg {
		t.Error("expected a message.user event for \"/skill\"")
	}
	if !strings.Contains(listing, "pdf-tools") || !strings.Contains(listing, "Work with PDF files") {
		t.Errorf("listing = %q, want it to mention the registered skill", listing)
	}
}

// TestSkillCommandUnknown confirms "/skill <bad name>" reports an error
// locally without ever calling the model.
func TestSkillCommandUnknown(t *testing.T) {
	loop, store := newSkillTestLoop(t, "")
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/skill nonexistent"); err != nil {
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
			msg, _ := ev.Data["error"].(string)
			if !strings.Contains(msg, "nonexistent") {
				t.Errorf("error message = %q, want it to mention the bad skill name", msg)
			}
		}
	}
	if !sawError {
		t.Error("expected an error event for an unknown skill name")
	}
}

// TestSkillCommandLoad confirms "/skill <name>" keeps the short command as
// the displayed message.user text but sends the full skill body to the
// model, which then answers normally.
func TestSkillCommandLoad(t *testing.T) {
	var lastRequestBody string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		lastRequestBody = string(raw)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok, using pdf-tools.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer model.Close()

	loop, store := newSkillTestLoop(t, model.URL)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/skill pdf-tools"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	var displayedText string
	var finalText strings.Builder
	for _, ev := range all {
		switch ev.Type {
		case events.TypeUserMessage:
			displayedText, _ = ev.Data["text"].(string)
		case events.TypeMessagePartDelta:
			if text, ok := ev.Data["text"].(string); ok {
				finalText.WriteString(text)
			}
		}
	}

	if displayedText != "/skill pdf-tools" {
		t.Errorf("displayed message.user text = %q, want %q", displayedText, "/skill pdf-tools")
	}
	if got := finalText.String(); got != "ok, using pdf-tools." {
		t.Errorf("final text = %q, want %q", got, "ok, using pdf-tools.")
	}
	if !strings.Contains(lastRequestBody, "Merge and split PDFs.") {
		t.Errorf("expected the skill body to be sent to the model; request was: %s", lastRequestBody)
	}
	if strings.Contains(lastRequestBody, "/skill pdf-tools") {
		t.Error("the raw \"/skill pdf-tools\" command text should not leak into the model request")
	}
}

// newSkillEchoServer returns a stub model endpoint that records the request
// body it received, so a test can assert what the model was actually sent.
func newSkillEchoServer(t *testing.T, body *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		*body = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// runSkillTurn drives one SendMessage and returns the message.user text
// that was recorded for display.
func runSkillTurn(t *testing.T, loop *Loop, store *session.Store, text string) string {
	t.Helper()
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", text); err != nil {
		t.Fatalf("SendMessage(%q): %v", text, err)
	}
	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var displayed string
	for _, ev := range all {
		if ev.Type == events.TypeUserMessage {
			displayed, _ = ev.Data["text"].(string)
		}
	}
	return displayed
}

// TestSkillRunsByItsOwnName is the headline behavior: a registered skill is
// invoked as "/<skill-name>", the same shape custom commands use, instead
// of requiring the "/skill <name>" prefix.
func TestSkillRunsByItsOwnName(t *testing.T) {
	var requestBody string
	model := newSkillEchoServer(t, &requestBody)
	defer model.Close()

	loop, store := newSkillTestLoop(t, model.URL)
	displayed := runSkillTurn(t, loop, store, "/pdf-tools")

	if displayed != "/pdf-tools" {
		t.Errorf("displayed message.user text = %q, want the short command the user typed", displayed)
	}
	if !strings.Contains(requestBody, "Merge and split PDFs.") {
		t.Errorf("skill body was not sent to the model; request was: %s", requestBody)
	}
	if strings.Contains(requestBody, `"/pdf-tools"`) {
		t.Error("the raw \"/pdf-tools\" command text should not leak into the model request")
	}
}

// TestSkillByNameForwardsTrailingArgs checks that anything typed after the
// skill name reaches the model as the actual request, so "/pdf-tools merge
// a.pdf b.pdf" is usable rather than silently dropping the arguments.
func TestSkillByNameForwardsTrailingArgs(t *testing.T) {
	var requestBody string
	model := newSkillEchoServer(t, &requestBody)
	defer model.Close()

	loop, store := newSkillTestLoop(t, model.URL)
	runSkillTurn(t, loop, store, "/pdf-tools merge a.pdf and b.pdf")

	if !strings.Contains(requestBody, "Merge and split PDFs.") {
		t.Errorf("skill body missing from the model request: %s", requestBody)
	}
	if !strings.Contains(requestBody, "merge a.pdf and b.pdf") {
		t.Errorf("trailing arguments were dropped instead of reaching the model: %s", requestBody)
	}
}

// TestUnknownSlashCommandStillGoesToModel guards the fallthrough: only a
// name that actually matches a registered skill is intercepted. Anything
// else keeps its old behavior of being sent to the model verbatim, rather
// than erroring out as an unknown command.
func TestUnknownSlashCommandStillGoesToModel(t *testing.T) {
	var requestBody string
	model := newSkillEchoServer(t, &requestBody)
	defer model.Close()

	loop, store := newSkillTestLoop(t, model.URL)
	runSkillTurn(t, loop, store, "/not-a-skill")

	if !strings.Contains(requestBody, "/not-a-skill") {
		t.Errorf("an unmatched slash command should reach the model verbatim: %s", requestBody)
	}
	if strings.Contains(requestBody, "Merge and split PDFs.") {
		t.Error("an unmatched slash command must not load a skill body")
	}
}

// TestCustomCommandWinsOverSameNamedSkill pins the precedence rule: custom
// commands are checked before skills, so a project that defines both keeps
// getting its own command.
func TestCustomCommandWinsOverSameNamedSkill(t *testing.T) {
	var requestBody string
	model := newSkillEchoServer(t, &requestBody)
	defer model.Close()

	loop, store := newSkillTestLoop(t, model.URL)
	loop.Commands = []commands.Command{
		{Name: "pdf-tools", Description: "project command", Body: "PROJECT COMMAND BODY"},
	}

	runSkillTurn(t, loop, store, "/pdf-tools")

	if !strings.Contains(requestBody, "PROJECT COMMAND BODY") {
		t.Errorf("custom command should win over a same-named skill: %s", requestBody)
	}
	if strings.Contains(requestBody, "Merge and split PDFs.") {
		t.Error("the skill body should not be used when a custom command has the same name")
	}
}
