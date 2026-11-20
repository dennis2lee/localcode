package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// A model its profile says nothing about is shown a test image, and what
// it does with it decides whether it is sent images. A profile that says,
// and a Claude model, are never asked.

// visionProbe is a ProbeVision hook that answers as told and counts how
// often it was asked, by model.
type visionProbe struct {
	mu    sync.Mutex
	asked map[string]int
	reply func(model string) (provider.VisionProbe, error)
}

func (v *visionProbe) hook(ctx context.Context, providerKey, model string) (provider.VisionProbe, error) {
	v.mu.Lock()
	if v.asked == nil {
		v.asked = map[string]int{}
	}
	v.asked[providerKey+"/"+model]++
	v.mu.Unlock()
	return v.reply(model)
}

func (v *visionProbe) count(key string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.asked[key]
}

func sees(answer string) func(string) (provider.VisionProbe, error) {
	return func(string) (provider.VisionProbe, error) {
		return provider.VisionProbe{Sees: true, Answer: answer}, nil
	}
}

func TestOnlyAModelNobodyHasDescribedIsAsked(t *testing.T) {
	yes, no := true, false
	probe := &visionProbe{reply: sees("Red")}
	loop := &Loop{ProbeVision: probe.hook}
	cases := []struct {
		profile config.Profile
		sees    bool
		asked   int
		why     string
	}{
		{config.Profile{Provider: "onprem", Model: "qwen3-vl-235b", Vision: &no}, false, 0, `sets "vision": false`},
		{config.Profile{Provider: "onprem", Model: "gpt-oss-120b", Vision: &yes}, true, 0, `sets "vision": true`},
		{config.Profile{Provider: "bedrock", Model: "us.anthropic.claude-sonnet-5-5"}, true, 0, "a Claude model"},
		{config.Profile{Provider: "bedrock", Model: "us.anthropic.claude-sonnet-5-5", Vision: &no}, false, 0, `sets "vision": false`},
		{config.Profile{Provider: "onprem", Model: "qwen3-vl-235b"}, true, 1, `named the colour of a test image ("Red")`},
	}
	for _, c := range cases {
		got := loop.seesImages(context.Background(), c.profile, "")
		if got.sees != c.sees || !strings.Contains(got.why, c.why) {
			t.Errorf("%s vision=%v: got %+v", c.profile.Model, c.profile.Vision, got)
		}
		if n := probe.count(c.profile.Provider + "/" + c.profile.Model); n != c.asked {
			t.Errorf("%s vision=%v: asked %d times, want %d", c.profile.Model, c.profile.Vision, n, c.asked)
		}
	}
	// A model override is the model asked about.
	loop.seesImages(context.Background(), config.Profile{Provider: "onprem", Model: "gpt-oss-120b"}, "gemma-4-27b")
	if probe.count("onprem/gemma-4-27b") != 1 || probe.count("onprem/gpt-oss-120b") != 0 {
		t.Errorf("asked %v, want the override's model", probe.asked)
	}
	// Without the hook nothing is asked and nothing is sent, as before.
	if got := (&Loop{}).seesImages(context.Background(), config.Profile{Provider: "onprem", Model: "qwen3-vl"}, ""); got.sees {
		t.Errorf("no hook: %+v", got)
	}
}

// Asked once per provider and model, whatever it answered, while it
// answered. The same model through another provider is another server.
func TestAModelIsAskedOncePerProcess(t *testing.T) {
	probe := &visionProbe{reply: func(model string) (provider.VisionProbe, error) {
		if model == "blind" {
			return provider.VisionProbe{Answer: "Blue"}, nil
		}
		return provider.VisionProbe{Refused: errors.New("this model does not support image input")}, nil
	}}
	loop := &Loop{ProbeVision: probe.hook}
	for i := 0; i < 3; i++ {
		for _, p := range []config.Profile{
			{Provider: "a", Model: "blind"},
			{Provider: "a", Model: "text-only"},
			{Provider: "b", Model: "blind"},
		} {
			got := loop.seesImages(context.Background(), p, "")
			if got.sees {
				t.Errorf("%+v: %+v", p, got)
			}
		}
	}
	for _, key := range []string{"a/blind", "a/text-only", "b/blind"} {
		if n := probe.count(key); n != 1 {
			t.Errorf("%s asked %d times, want once", key, n)
		}
	}
	if got := loop.knownSeesImages(config.Profile{Provider: "a", Model: "text-only"}, ""); !strings.Contains(got.why, "refused a test image") {
		t.Errorf("why = %q", got.why)
	}
	if got := loop.knownSeesImages(config.Profile{Provider: "a", Model: "blind"}, ""); !strings.Contains(got.why, `it said "Blue"`) {
		t.Errorf("why = %q", got.why)
	}
}

// A probe that got no answer is not an answer: kept for a while, so a
// server that is down is not asked on every turn, then asked again.
func TestAProbeWithoutAnAnswerIsAskedAgainLater(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	probe := &visionProbe{reply: func(string) (provider.VisionProbe, error) {
		if fail.Load() {
			return provider.VisionProbe{}, errors.New("openai-compat endpoint returned 503: loading model\nmore")
		}
		return provider.VisionProbe{Sees: true, Answer: "red"}, nil
	}}
	loop := &Loop{ProbeVision: probe.hook}
	p := config.Profile{Provider: "onprem", Model: "qwen3-vl"}
	got := loop.seesImages(context.Background(), p, "")
	if got.sees || !strings.Contains(got.why, "got no answer (openai-compat endpoint returned 503: loading model)") {
		t.Fatalf("got %+v", got)
	}
	fail.Store(false)
	if got := loop.seesImages(context.Background(), p, ""); got.sees || probe.count("onprem/qwen3-vl") != 1 {
		t.Fatalf("asked again at once: %+v, %d", got, probe.count("onprem/qwen3-vl"))
	}
	// The wait is over.
	loop.mu.Lock()
	entry := loop.visionCache["onprem\x00qwen3-vl"]
	if entry.until.IsZero() || time.Until(entry.until) > visionRetryAfter {
		t.Errorf("until = %v", entry.until)
	}
	entry.until = time.Now().Add(-time.Second)
	loop.visionCache["onprem\x00qwen3-vl"] = entry
	loop.mu.Unlock()
	if got := loop.seesImages(context.Background(), p, ""); !got.sees || probe.count("onprem/qwen3-vl") != 2 {
		t.Errorf("after the wait: %+v, asked %d times", got, probe.count("onprem/qwen3-vl"))
	}
}

// Two turns starting on one model wait for one probe. A turn that ends
// while it waits is not sent images, and leaves nothing remembered.
func TestTurnsOnOneModelShareOneProbe(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	probe := &visionProbe{reply: func(string) (provider.VisionProbe, error) {
		started <- struct{}{}
		<-release
		return provider.VisionProbe{Sees: true, Answer: "red"}, nil
	}}
	loop := &Loop{ProbeVision: probe.hook}
	p := config.Profile{Provider: "onprem", Model: "qwen3-vl"}

	results := make(chan visionVerdict, 3)
	for i := 0; i < 3; i++ {
		go func() { results <- loop.seesImages(context.Background(), p, "") }()
	}
	<-started
	cancelled, cancel := context.WithCancel(context.Background())
	waiting := make(chan visionVerdict, 1)
	go func() { waiting <- loop.seesImages(cancelled, p, "") }()
	cancel()
	if got := <-waiting; got.sees || !strings.Contains(got.why, "turn ended") {
		t.Errorf("a cancelled wait: %+v", got)
	}
	close(release)
	for i := 0; i < 3; i++ {
		if got := <-results; !got.sees {
			t.Errorf("turn %d: %+v", i, got)
		}
	}
	if n := probe.count("onprem/qwen3-vl"); n != 1 {
		t.Errorf("asked %d times, want once", n)
	}
}

// A probe the turn's own cancel cut short is not remembered: the next
// turn asks.
func TestAProbeCutShortByItsTurnIsNotRemembered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	probe := &visionProbe{reply: func(string) (provider.VisionProbe, error) {
		cancel()
		return provider.VisionProbe{}, context.Canceled
	}}
	loop := &Loop{ProbeVision: probe.hook}
	p := config.Profile{Provider: "onprem", Model: "qwen3-vl"}
	loop.seesImages(ctx, p, "")
	probe.reply = sees("red")
	if got := loop.seesImages(context.Background(), p, ""); !got.sees || probe.count("onprem/qwen3-vl") != 2 {
		t.Errorf("got %+v after %d probes", got, probe.count("onprem/qwen3-vl"))
	}
}

// The whole way through a turn: a model served under a name nobody knows
// is offered view_image and sent the pasted image once it has named the
// test image's colour, and is told nothing about images otherwise.
func TestAModelThatPassesTheTestImageIsSentImages(t *testing.T) {
	for _, c := range []struct {
		name  string
		reply func(string) (provider.VisionProbe, error)
		sees  bool
	}{
		{"sees", sees("Red."), true},
		{"blind", func(string) (provider.VisionProbe, error) { return provider.VisionProbe{Answer: "Blue"}, nil }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			loop, p, sessionID := imageLoop(t, "onprem-vlm-2026", nil, textReply("ok"))
			probe := &visionProbe{reply: c.reply}
			loop.ProbeVision = probe.hook
			err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "what is this?",
				provider.ImageBlock("image/png", testPNG))
			if err != nil {
				t.Fatal(err)
			}
			req := requestsOf(p)[0]
			if offersTool(req, tools.ViewImageName) != c.sees || (imagesIn(req) == 1) != c.sees {
				t.Errorf("view_image offered %v, %d images sent", offersTool(req, tools.ViewImageName), imagesIn(req))
			}
			notices := imageNotices(t, loop, sessionID)
			if c.sees && len(notices) != 0 {
				t.Errorf("notices = %q", notices)
			}
			if !c.sees && (len(notices) != 1 || !strings.Contains(notices[0], `did not name the colour of a test image (it said "Blue")`)) {
				t.Errorf("notices = %q", notices)
			}
			if n := probe.count("local/onprem-vlm-2026"); n != 1 {
				t.Errorf("asked %d times", n)
			}
		})
	}
}
