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
	for _, m := range reqs[1].Messages {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "left out") && !strings.Contains(b.Text, "image/png") {
				t.Errorf("the note %q does not say what kind of image was there", b.Text)
			}
		}
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

// Once a turn, however many steps it takes, and only for a reply that
// says something: a turn cancelled before its first token reads as an
// empty reply.
func TestTheNoticeIsSaidOnceForATurnThatAnswered(t *testing.T) {
	img := provider.ImageBlock("image/png", testPNG)
	loop, _, sessionID := imageLoop(t, "deepseek-v4.1-flash", nil,
		toolCall("c1", "read_file", `{"path":"missing.txt"}`),
		toolCall("c2", "read_file", `{"path":"also-missing.txt"}`),
		textReply("No such files."),
	)
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "read these", img); err != nil {
		t.Fatal(err)
	}
	if got := imageNotices(t, loop, sessionID); len(got) != 1 {
		t.Errorf("a turn of three steps wrote %d notices, want 1: %q", len(got), got)
	}

	empty := []provider.StreamEvent{{Type: provider.EventMessageStop, StopReason: "end_turn"}}
	loop, _, sessionID = imageLoop(t, "deepseek-v4.1-flash", nil, empty)
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "look", img); err != nil {
		t.Fatal(err)
	}
	if got := imageNotices(t, loop, sessionID); len(got) != 0 {
		t.Errorf("a reply with nothing in it wrote notices %q", got)
	}
}

// A fallback within a turn moves it to another model, and what that model
// is sent is decided for that model. The person is told once, about the
// model that answered.
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
	if len(notices) != 1 || !strings.Contains(notices[0], "deepseek-v4.1-flash") {
		t.Errorf("notices = %q, want one, naming the model that answered", notices)
	}
}

// The other way round: a model that is not sent images fails, and the
// turn falls back to one that is. The images reached the model that
// answered, so there is nothing to tell the person. (Found by review: the
// notice was written before the request, and named a model a fallback
// then replaced.)
func TestAFallbackToAModelThatCanSeeSaysNothingWasLeftOut(t *testing.T) {
	reg := tools.NewRegistry(nil)
	reg.Register(tools.ReadFile{})
	dead := []provider.StreamEvent{{Type: provider.EventError, Err: fmt.Errorf("openai-compat endpoint returned 404: model not found")}}
	p := &scriptedProvider{turns: [][]provider.StreamEvent{dead, textReply("A bar chart.")}}
	loop, sessionID := scriptedLoop(t, p, reg)
	loop.Config.Profiles["blind"] = config.Profile{Provider: "local", Model: "deepseek-v4.1-flash", Fallback: []string{"seeing"}}
	loop.Config.Profiles["seeing"] = config.Profile{Provider: "local", Model: "claude-sonnet-5-5"}
	loop.Config.Agents["general-purpose"] = config.AgentConfig{Profile: "blind"}
	loop.SetSmartAgentEnabled(true)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "look", provider.ImageBlock("image/png", testPNG)); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reqs := requestsOf(p)
	if len(reqs) != 2 || imagesIn(reqs[0]) != 0 || imagesIn(reqs[1]) != 1 {
		t.Fatalf("%d requests; want the first without the image and the fallback with it", len(reqs))
	}
	if notices := imageNotices(t, loop, sessionID); len(notices) != 0 {
		t.Errorf("notices %q, though the model that answered was sent the image", notices)
	}
}

// The forced trim, the rescue of last resort for a conversation that no
// longer fits, cuts the history as the model is sent it. A model that is
// not sent images was charged 1,600 tokens for each one still in the
// history, and when they were in the newest message, which the trim never
// drops, it dropped that much more of the conversation before it.
func TestAForcedTrimMeasuresTheHistoryAsTheModelIsSentIt(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		n := len(bodies)
		bodies = append(bodies, string(body))
		mu.Unlock()
		switch n {
		case 0:
			http.Error(w, `{"error":{"message":"This model's maximum context length is 32768 tokens"}}`, http.StatusBadRequest)
		case 1:
			// The summary fails too, so the same rescue goes on to trim.
			http.Error(w, `{"error":{"message":"summarizer unavailable"}}`, http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answered\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL}},
		Profiles:       map[string]config.Profile{"onprem": {Provider: "local", Model: "deepseek-v4.1-flash", ContextWindow: 32768}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "onprem"}},
		DefaultProfile: "onprem",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": provider.NewOpenAICompat(srv.URL, "")}, cfg)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		loop.appendHistory("s1", provider.Message{Role: provider.RoleUser, Content: []provider.Block{
			provider.TextBlock(fmt.Sprintf("message %d: %s", i, strings.Repeat("x", 4000))),
		}})
		loop.appendHistory("s1", provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.TextBlock("ok")}})
	}
	img := provider.ImageBlock("image/png", testPNG)
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "carry on", img, img, img); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("%d requests, want the refused one, the failed summary and the one after the trim", len(bodies))
	}
	last := bodies[2]
	// The trim cuts about a third of what is sent. Of five long messages,
	// the newest two are well inside the two thirds kept; charged for the
	// three images, only the newest one was.
	for _, want := range []string{"message 4:", "message 3:", "carry on"} {
		if !strings.Contains(last, want) {
			t.Errorf("the request after the trim lost %q", want)
		}
	}
	if strings.Contains(last, "image_url") {
		t.Error("the trimmed request carries an image")
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
