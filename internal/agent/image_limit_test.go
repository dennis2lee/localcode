package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// A server that takes fewer images in a request than a conversation holds
// says how many, and the turn goes on with that many: asked again at once,
// and every later request to that model carries no more.

// cappedServer refuses, as vLLM does, a request carrying more images than
// cap, and otherwise answers as the scripted provider does. always makes
// it refuse every request, within the cap or not.
type cappedServer struct {
	*scriptedProvider
	cap    int
	always bool

	mu      sync.Mutex
	refused []int // the image count of each refused request
}

func (c *cappedServer) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	if n := countImages(req.Messages); n > c.cap || c.always {
		c.mu.Lock()
		c.refused = append(c.refused, n)
		c.mu.Unlock()
		return nil, errors.New(`openai-compat endpoint returned 400: {"object":"error","message":"At most 1 image(s) may be provided in one prompt.","type":"BadRequestError","code":400}`)
	}
	return c.scriptedProvider.Chat(ctx, req)
}

func (c *cappedServer) refusals() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.refused...)
}

func TestAServersImageCapIsLearnedAndTheTurnGoesOn(t *testing.T) {
	sees := true
	loop, p, sessionID := imageLoop(t, "Qwen/Qwen2.5-VL-7B-Instruct", &sees, textReply("one"), textReply("two"))
	server := &cappedServer{scriptedProvider: p, cap: 1}
	loop.Providers["local"] = server
	img := provider.ImageBlock("image/png", testPNG)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "compare these", img, img, img); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if got := server.refusals(); len(got) != 1 || got[0] != 3 {
		t.Fatalf("refused %v, want the one request carrying 3 images", got)
	}
	reqs := requestsOf(p)
	if len(reqs) != 1 || imagesIn(reqs[0]) != 1 {
		t.Fatalf("answered requests %d, images in the first %d; want one request with 1 image", len(reqs), imagesIn(reqs[0]))
	}
	notes := 0
	for _, m := range reqs[0].Messages {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "a request carries at most 1 image and") {
				notes++
			}
		}
	}
	if notes != 2 {
		t.Errorf("%d notes in place of the older images, want 2", notes)
	}
	evs, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	notices := 0
	for _, ev := range evs {
		if msg, _ := ev.Data["error"].(string); strings.Contains(msg, "takes at most 1 image in a request") {
			notices++
			if recovered, _ := ev.Data["recovered"].(bool); !recovered {
				t.Errorf("notice %q is not marked recovered", msg)
			}
		}
	}
	if notices != 1 {
		t.Errorf("%d notices about the cap, want 1", notices)
	}

	// The next turn sends within the cap from the start.
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "and now?"); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := server.refusals(); len(got) != 1 {
		t.Errorf("refused again: %v", got)
	}
	if reqs := requestsOf(p); len(reqs) != 2 || imagesIn(reqs[1]) != 1 {
		t.Errorf("the second turn: %d requests, %d images", len(reqs), imagesIn(reqs[len(reqs)-1]))
	}
}

// A refusal of a request already within the cap is not one sending fewer
// images can answer: the turn fails once, not in a loop.
func TestACapRefusalWithinTheCapIsNotAskedAgain(t *testing.T) {
	sees := true
	loop, p, sessionID := imageLoop(t, "qwen-vl", &sees, textReply("never"))
	server := &cappedServer{scriptedProvider: p, cap: 1, always: true}
	loop.Providers["local"] = server

	err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "look", provider.ImageBlock("image/png", testPNG))
	if err == nil || !strings.Contains(err.Error(), "At most 1 image(s)") {
		t.Fatalf("err = %v", err)
	}
	if got := server.refusals(); len(got) != 1 {
		t.Errorf("asked %d times, want once", len(got))
	}
}

// The cap is the model's server's, kept per provider and model: another
// model is sent the images it takes, and a model override is the model
// the cap belongs to.
func TestAnImageCapBelongsToOneModel(t *testing.T) {
	loop := &Loop{}
	capped := config.Profile{Provider: "onprem", Model: "qwen-vl"}
	refusal := errors.New("openai-compat endpoint returned 400: At most 2 image(s) may be provided in one prompt.")
	if n, ok := loop.learnImageLimit(capped, refusal, 5); !ok || n != 2 {
		t.Fatalf("learned %d, %v", n, ok)
	}
	if got := loop.imageLimitFor(capped); got != 2 {
		t.Errorf("cap = %d", got)
	}
	for _, other := range []config.Profile{{Provider: "onprem", Model: "other-vl"}, {Provider: "elsewhere", Model: "qwen-vl"}} {
		if got := loop.imageLimitFor(other); got != 0 {
			t.Errorf("%+v has cap %d", other, got)
		}
	}
	if got := loop.imageLimitFor(profileWithModel(config.Profile{Provider: "onprem", Model: "other-vl"}, "qwen-vl")); got != 2 {
		t.Errorf("through an override: cap %d", got)
	}
	// A higher number later does not raise it; a lower one lowers it.
	loop.learnImageLimit(capped, errors.New("At most 4 image(s) may be provided in one prompt."), 9)
	if got := loop.imageLimitFor(capped); got != 2 {
		t.Errorf("cap = %d after a higher one was said, want 2", got)
	}
	loop.learnImageLimit(capped, errors.New("At most 1 image(s) may be provided in one prompt."), 9)
	if got := loop.imageLimitFor(capped); got != 1 {
		t.Errorf("cap = %d, want the lowest said", got)
	}
	// A request within the cap, and an error that is no cap, teach nothing.
	if _, ok := loop.learnImageLimit(capped, refusal, 2); ok {
		t.Error("a request within the cap was to be asked again")
	}
	if _, ok := loop.learnImageLimit(capped, errors.New("this model does not support image input"), 9); ok {
		t.Error("a refusal read as a cap")
	}
	// asSent reads the cap from the turn.
	msgs := []provider.Message{{Role: provider.RoleUser, Content: []provider.Block{
		provider.ImageBlock("image/png", testPNG), provider.ImageBlock("image/png", testPNG), provider.ImageBlock("image/png", testPNG),
	}}}
	ctx := withImageLimit(withViewsImages(context.Background(), true), 2)
	if got := countImages(asSent(ctx, msgs)); got != 2 {
		t.Errorf("asSent with a cap of 2 sent %d images", got)
	}
	if got := countImages(asSent(withImageLimit(withViewsImages(context.Background(), true), 0), msgs)); got != 3 {
		t.Errorf("asSent with no cap sent %d images", got)
	}
}

// capNotices is the notices of sessionID that say a cap left images out.
func capNotices(t *testing.T, loop *Loop, sessionID string) []string {
	t.Helper()
	evs, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		if msg, _ := ev.Data["error"].(string); strings.Contains(msg, "takes at most") {
			out = append(out, msg)
		}
	}
	return out
}

// Found in review (Opus): a cap learned in one conversation left the
// images a person attached past it out of every later request, in that
// conversation or another, and no request was refused to say so.
func TestImagesACapLeavesOutAreSaid(t *testing.T) {
	sees := true
	loop, p, first := imageLoop(t, "Qwen/Qwen2.5-VL-7B-Instruct", &sees, textReply("one"), textReply("two"), textReply("three"))
	loop.Providers["local"] = &cappedServer{scriptedProvider: p, cap: 1}
	img := provider.ImageBlock("image/png", testPNG)
	if err := loop.SendMessage(context.Background(), first, "general-purpose", "look", img, img); err != nil {
		t.Fatal(err)
	}
	if got := capNotices(t, loop, first); len(got) != 1 || !strings.Contains(got[0], "asking again") {
		t.Fatalf("the turn that learned the cap: notices %q, want the one about asking again", got)
	}

	second, err := loop.Store.CreateSession("second", "", "general-purpose", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(context.Background(), second.ID, "general-purpose", "and these", img, img, img); err != nil {
		t.Fatal(err)
	}
	got := capNotices(t, loop, second.ID)
	if len(got) != 1 || !strings.Contains(got[0], "so 2 of the 3 images attached to this message were left out") {
		t.Errorf("another conversation: notices %q", got)
	}
	// Within the cap, nothing to say.
	if err := loop.SendMessage(context.Background(), second.ID, "general-purpose", "one more", img); err != nil {
		t.Fatal(err)
	}
	if again := capNotices(t, loop, second.ID); len(again) != 1 {
		t.Errorf("a message within the cap added a notice: %q", again)
	}
}

// chainServer is three capped endpoints in one: a request past one image
// is refused as vLLM refuses it, a request within it to a model named in
// gone is "model not found", which moves a turn down its fallbacks, and
// any other is answered.
type chainServer struct {
	mu    sync.Mutex
	gone  map[string]bool
	calls []string
}

func (c *chainServer) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	n := countImages(req.Messages)
	c.mu.Lock()
	c.calls = append(c.calls, fmt.Sprintf("%s:%d", req.Model, n))
	c.mu.Unlock()
	if n > 1 {
		return nil, errors.New(`openai-compat endpoint returned 400: {"object":"error","message":"At most 1 image(s) may be provided in one prompt.","type":"BadRequestError","code":400}`)
	}
	if c.gone[req.Model] {
		return nil, errors.New(`openai-compat endpoint returned 404: {"error":{"message":"model not found"}}`)
	}
	ch := make(chan provider.StreamEvent, 2)
	for _, ev := range textReply("answered") {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

// Found in review (Opus): the retry was counted per turn, so a turn whose
// first two models each spent one ended on the third model's refusal.
// Each model of the turn gets its own.
func TestEachModelOfATurnGetsItsOwnCapRetry(t *testing.T) {
	sees := true
	reg := tools.NewRegistry(nil)
	reg.Register(tools.ReadFile{})
	server := &chainServer{gone: map[string]bool{"vl-a": true, "vl-b": true}}
	loop, sessionID := scriptedLoop(t, server, reg)
	loop.Config.Profiles["balanced"] = config.Profile{Provider: "local", Model: "vl-a", Vision: &sees, Fallback: []string{"b", "c"}}
	loop.Config.Profiles["b"] = config.Profile{Provider: "local", Model: "vl-b", Vision: &sees}
	loop.Config.Profiles["c"] = config.Profile{Provider: "local", Model: "vl-c", Vision: &sees}
	loop.SetSmartAgentEnabled(true)

	img := provider.ImageBlock("image/png", testPNG)
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "compare these", img, img); err != nil {
		t.Fatalf("turn: %v", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if got := strings.Join(server.calls, " "); got != "vl-a:2 vl-a:1 vl-b:2 vl-b:1 vl-c:2 vl-c:1" {
		t.Errorf("requests (model:images) %s", got)
	}
}
