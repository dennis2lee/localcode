package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// An image in the history is sent only to a model that is sent images.
//
// It was sent to every model: a conversation that pasted a screenshot on
// Claude and then moved to an on-prem model that takes no images had every
// request after the move refused, including ones about something else, so
// the conversation could not go on.

// imagesIn counts the image blocks a request carries.
func imagesIn(req provider.ChatRequest) int {
	return countImages(req.Messages)
}

// leftOutNotes counts the sentences that stand in for an image a request
// left out.
func leftOutNotes(req provider.ChatRequest) int {
	n := 0
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Type == provider.BlockText && strings.Contains(b.Text, "It was left out: this model is not sent images.") {
				n++
			}
		}
	}
	return n
}

// imageNotices is the notices in the session's log that say attached
// images did not reach the model. Each is a recovered error, which clients
// show as a note on a turn that went on rather than as its failure.
func imageNotices(t *testing.T, loop *Loop, sessionID string) []string {
	t.Helper()
	evs, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		msg, _ := ev.Data["error"].(string)
		if ev.Type == events.TypeError && strings.Contains(msg, "is not sent images") {
			if recovered, _ := ev.Data["recovered"].(bool); !recovered {
				t.Errorf("notice %q is not marked recovered", msg)
			}
			out = append(out, msg)
		}
	}
	return out
}

func TestMovingToAModelThatCannotSeeLeavesTheImagesOut(t *testing.T) {
	loop, p, sessionID := imageLoop(t, "claude-sonnet-5-5", nil,
		textReply("A bar chart."), textReply("Paris."), textReply("Still a bar chart."))
	loop.Config.Profiles["onprem"] = config.Profile{Provider: "local", Model: "deepseek-v4.1-flash"}
	ctx := context.Background()

	if err := loop.SendMessage(ctx, sessionID, "general-purpose", "what is this?", provider.ImageBlock("image/png", testPNG)); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.SetSessionModel(sessionID, "onprem"); err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(ctx, sessionID, "general-purpose", "capital of France?"); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.SetSessionModel(sessionID, ""); err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(ctx, sessionID, "general-purpose", "and the chart?"); err != nil {
		t.Fatal(err)
	}

	reqs := requestsOf(p)
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want 3", len(reqs))
	}
	if imagesIn(reqs[0]) != 1 {
		t.Fatalf("the model that can see was sent %d images, want the one pasted", imagesIn(reqs[0]))
	}
	if reqs[1].Model != "deepseek-v4.1-flash" {
		t.Fatalf("the second turn went to %q, not the model it was moved to", reqs[1].Model)
	}
	if n := imagesIn(reqs[1]); n != 0 {
		t.Errorf("the model that cannot see was sent %d images", n)
	}
	if n := leftOutNotes(reqs[1]); n != 1 {
		t.Errorf("%d notes where the image was, want 1", n)
	}
	// Back on the model that can see, it is sent again: the history kept it.
	if n := imagesIn(reqs[2]); n != 1 {
		t.Errorf("moved back to the model that can see, it was sent %d images, want the one pasted", n)
	} else {
		for _, m := range reqs[2].Messages {
			for _, b := range m.Content {
				if b.Type == provider.BlockImage && !bytes.Equal(b.Data, testPNG) {
					t.Error("the image sent after moving back is not the one pasted")
				}
			}
		}
	}
	if leftOutNotes(reqs[2]) != 0 {
		t.Error("the model that can see was told an image was left out")
	}
	// Nothing was attached on the turn that left it out, so there is
	// nothing to tell the person: the image they pasted was seen when they
	// pasted it.
	if got := imageNotices(t, loop, sessionID); len(got) != 0 {
		t.Errorf("notices %q on turns that attached nothing", got)
	}
}

// An image that came from view_image is history like any other.
func TestAViewedImageIsLeftOutForTheModelThatComesNext(t *testing.T) {
	path := writePNG(t)
	input, _ := json.Marshal(map[string]string{"path": path})
	loop, p, sessionID := imageLoop(t, "claude-sonnet-5-5", nil,
		toolCall("c1", "view_image", string(input)), textReply("A bar chart."), textReply("ok"))
	loop.Config.Profiles["onprem"] = config.Profile{Provider: "local", Model: "deepseek-v4.1-flash"}
	ctx := context.Background()

	if err := loop.SendMessage(ctx, sessionID, "general-purpose", "what is in fig.png?"); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.SetSessionModel(sessionID, "onprem"); err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(ctx, sessionID, "general-purpose", "thanks"); err != nil {
		t.Fatal(err)
	}
	reqs := requestsOf(p)
	if len(reqs) != 3 || imagesIn(reqs[1]) != 1 {
		t.Fatalf("%d requests, the second with %d images: the image was never viewed", len(reqs), imagesIn(reqs[1]))
	}
	if n := imagesIn(reqs[2]); n != 0 {
		t.Errorf("the model that cannot see was sent %d images", n)
	}
	if res, ok := resultFor(reqs[2].Messages, "c1"); !ok || res.IsError {
		t.Errorf("the call's result = %+v (found %v), want it kept as it was", res, ok)
	}
	if leftOutNotes(reqs[2]) != 1 {
		t.Error("no note where the viewed image was")
	}
}

// A person who pastes an image for a model that cannot see is told so, with
// the setting that would send it, and the model is told where it was.
func TestImagesPastedForAModelThatCannotSeeAreLeftOutAndSaid(t *testing.T) {
	for _, c := range []struct {
		model  string
		notice bool
	}{
		{"deepseek-v4.1-flash", true},
		{"claude-sonnet-5-5", false},
	} {
		t.Run(c.model, func(t *testing.T) {
			loop, p, sessionID := imageLoop(t, c.model, nil, textReply("ok"))
			err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "what are these?",
				provider.ImageBlock("image/png", testPNG), provider.ImageBlock("image/png", testPNG))
			if err != nil {
				t.Fatal(err)
			}
			req := requestsOf(p)[0]
			notices := imageNotices(t, loop, sessionID)
			if !c.notice {
				if imagesIn(req) != 2 || len(notices) != 0 {
					t.Errorf("a model that can see: %d images sent, notices %q", imagesIn(req), notices)
				}
				return
			}
			if imagesIn(req) != 0 || leftOutNotes(req) != 2 {
				t.Errorf("%d images sent and %d notes, want none sent and a note for each", imagesIn(req), leftOutNotes(req))
			}
			if len(notices) != 1 {
				t.Fatalf("notices = %q, want one", notices)
			}
			for _, want := range []string{c.model, "the 2 images attached to this message were left out", `"vision": true on profile "balanced"`} {
				if !strings.Contains(notices[0], want) {
					t.Errorf("notice %q does not say %q", notices[0], want)
				}
			}
		})
	}
}

// A fallback within a turn moves it to another model, and what that model
// is sent is decided for that model. The person is told once.
func TestAFallbackToAModelThatCannotSeeIsSentNoImages(t *testing.T) {
	reg := tools.NewRegistry(nil)
	reg.Register(tools.ReadFile{})
	dead := []provider.StreamEvent{{Type: provider.EventError, Err: fmt.Errorf("openai-compat endpoint returned 404: model not found")}}
	p := &scriptedProvider{turns: [][]provider.StreamEvent{dead, dead, textReply("answered")}}
	loop, sessionID := scriptedLoop(t, p, reg)
	loop.Config.Profiles["primary"] = config.Profile{Provider: "local", Model: "claude-opus-5", Fallback: []string{"blind", "blind2"}}
	loop.Config.Profiles["blind"] = config.Profile{Provider: "local", Model: "qwen3-coder-30b"}
	loop.Config.Profiles["blind2"] = config.Profile{Provider: "local", Model: "deepseek-v4.1-flash"}
	loop.Config.Agents["general-purpose"] = config.AgentConfig{Profile: "primary"}
	loop.SetSmartAgentEnabled(true)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "look", provider.ImageBlock("image/png", testPNG)); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reqs := requestsOf(p)
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want three models in turn", len(reqs))
	}
	for i, want := range []int{1, 0, 0} {
		if got := imagesIn(reqs[i]); got != want {
			t.Errorf("request %d (%s): %d images, want %d", i, reqs[i].Model, got, want)
		}
	}
	notices := imageNotices(t, loop, sessionID)
	if len(notices) != 1 || !strings.Contains(notices[0], "qwen3-coder-30b") {
		t.Errorf("notices = %q, want one, naming the first model that was not sent it", notices)
	}
}

// What the report on the next request describes is what that request
// sends: a model that is not sent images is not charged for them.
func TestTheContextReportCountsNoImagesForAModelThatCannotSee(t *testing.T) {
	report := func(model string) string {
		loop, _, sessionID := imageLoop(t, model, nil)
		loop.appendHistory(sessionID, provider.Message{Role: provider.RoleUser, Content: []provider.Block{
			provider.TextBlock("what is this?"), provider.ImageBlock("image/png", testPNG),
		}})
		loop.appendHistory(sessionID, provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{
			provider.TextBlock("A bar chart."),
		}})
		if err := loop.handleContextCommand(context.Background(), sessionID, "general-purpose", "/context", false); err != nil {
			t.Fatal(err)
		}
		evs, err := loop.Store.Events(sessionID, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := len(evs) - 1; i >= 0; i-- {
			if text, _ := evs[i].Data["text"].(string); strings.Contains(text, "conversation so far") {
				for _, line := range strings.Split(text, "\n") {
					if strings.Contains(line, "conversation so far") {
						return line
					}
				}
			}
		}
		t.Fatal("no context report")
		return ""
	}
	tokens := func(line string) int {
		var n int
		fields := strings.Fields(line[strings.Index(line, "~"):])
		fmt.Sscanf(fields[0], "~%d", &n)
		return n
	}
	seeing, blind := tokens(report("claude-sonnet-5-5")), tokens(report("deepseek-v4.1-flash"))
	if seeing < imageTokenEstimate || blind >= imageTokenEstimate {
		t.Errorf("conversation so far: %d tokens on a model that can see, %d on one that cannot; "+
			"want the image counted only on the first", seeing, blind)
	}
}

// On the wire, against a server that refuses images for the model it was
// asked for, as LiteLLM does for a model registered without vision: the
// conversation goes on after the move.
func TestAServerThatRefusesImagesAnswersAfterTheMove(t *testing.T) {
	var mu sync.Mutex
	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		models = append(models, req.Model)
		mu.Unlock()
		if req.Model == "deepseek-v4.1-flash" && bytes.Contains(body, []byte("image_url")) {
			http.Error(w, `{"error":{"message":"deepseek-v4.1-flash does not support image input"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answered\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL}},
		Profiles: map[string]config.Profile{
			"bedrock": {Provider: "local", Model: "claude-sonnet-5-5"},
			"onprem":  {Provider: "local", Model: "deepseek-v4.1-flash"},
		},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "bedrock"}},
		DefaultProfile: "bedrock",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": provider.NewOpenAICompat(srv.URL, "")}, cfg)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := loop.SendMessage(ctx, "s1", "general-purpose", "what is this?", provider.ImageBlock("image/png", testPNG)); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, err := loop.SetSessionModel("s1", "onprem"); err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(ctx, "s1", "general-purpose", "capital of France?"); err != nil {
		t.Fatalf("after the move: %v", err)
	}
	evs, err := store.Events("s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if msg, _ := ev.Data["error"].(string); ev.Type == events.TypeError && strings.Contains(msg, "does not support image input") {
			t.Fatalf("the server refused a request after the move: %s", msg)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(models) != 2 || models[1] != "deepseek-v4.1-flash" {
		t.Errorf("requests went to %v, want the Claude model and then the on-prem one", models)
	}
}
