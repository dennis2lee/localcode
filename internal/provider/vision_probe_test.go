package provider

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"testing"
	"time"
)

// probeScript is a Provider that answers the probe as told: an error from
// Chat itself, or a stream of text deltas and an optional stream error.
type probeScript struct {
	chatErr   error
	deltas    []string
	streamErr error
	// endless keeps sending after the deltas until the request is
	// cancelled, as a model in the middle of a long answer does.
	endless bool
	// stop is the stop reason the stream ends with; "end_turn" when empty.
	stop string

	got ChatRequest
}

func (p *probeScript) Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	p.got = req
	if p.chatErr != nil {
		return nil, p.chatErr
	}
	ch := make(chan StreamEvent)
	go func() {
		defer close(ch)
		send := func(ev StreamEvent) bool {
			select {
			case ch <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for _, d := range p.deltas {
			if !send(StreamEvent{Type: EventTextDelta, TextDelta: d}) {
				return
			}
		}
		for p.endless {
			if !send(StreamEvent{Type: EventTextDelta, TextDelta: " and so on"}) {
				return
			}
		}
		if p.streamErr != nil {
			send(StreamEvent{Type: EventError, Err: p.streamErr})
			return
		}
		stop := p.stop
		if stop == "" {
			stop = "end_turn"
		}
		send(StreamEvent{Type: EventMessageStop, StopReason: stop})
	}()
	return ch, nil
}

func TestProbeVisionReadsWhatTheModelDid(t *testing.T) {
	refusal := errors.New("openai-compat endpoint returned 400: this model does not support image input")
	cases := []struct {
		name     string
		script   probeScript
		sees     bool
		refused  bool
		definite bool
	}{
		{"names the colour", probeScript{deltas: []string{"Re", "d."}}, true, false, true},
		{"in a sentence", probeScript{deltas: []string{"The image is a solid crimson square."}}, true, false, true},
		{"a different colour", probeScript{deltas: []string{"Blue"}}, false, false, true},
		{"red inside another word", probeScript{deltas: []string{"It is coloured."}}, false, false, true},
		{"says it cannot see", probeScript{deltas: []string{"I cannot see an image, so I cannot say whether it is red."}}, false, false, true},
		{"says there is no image", probeScript{deltas: []string{"There is no image here; red is my guess."}}, false, false, true},
		{"refused before the stream", probeScript{chatErr: refusal}, false, true, true},
		{"refused in the stream", probeScript{streamErr: refusal}, false, true, true},
		{"rate limited", probeScript{chatErr: errors.New("openai-compat endpoint returned 429: rate limit reached for image requests")}, false, false, false},
		{"model not found", probeScript{chatErr: errors.New("openai-compat endpoint returned 404: model not found")}, false, false, false},
		{"stream died", probeScript{streamErr: errors.New("connection reset by peer")}, false, false, false},
		{"answered nothing", probeScript{}, false, false, false},
		{"answered whitespace", probeScript{deltas: []string{"  \n"}}, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.script
			got, err := ProbeVision(context.Background(), &p, "m")
			if (err == nil) != c.definite {
				t.Fatalf("err = %v, want an answer: %v", err, c.definite)
			}
			if got.Sees != c.sees || (got.Refused != nil) != c.refused {
				t.Errorf("got %+v", got)
			}
		})
	}
}

// What the probe sends: one user message with the image and the question,
// to the model asked about, and an image any adapter carries.
func TestProbeVisionAsksWithOneSmallImage(t *testing.T) {
	p := &probeScript{deltas: []string{"red"}}
	if _, err := ProbeVision(context.Background(), p, "qwen3-vl-235b"); err != nil {
		t.Fatal(err)
	}
	if p.got.Model != "qwen3-vl-235b" || len(p.got.Tools) != 0 || len(p.got.Messages) != 1 {
		t.Fatalf("request: %+v", p.got)
	}
	m := p.got.Messages[0]
	if m.Role != RoleUser || len(m.Content) != 2 || m.Content[0].Type != BlockImage || m.Content[1].Text != visionProbeQuestion {
		t.Fatalf("message: %+v", m)
	}
	img := m.Content[0]
	if err := ValidateMessageImages(m); err != nil || img.MediaType != "image/png" {
		t.Fatalf("image: %s, %v", img.MediaType, err)
	}
	decoded, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decoded.At(32, 32).RGBA()
	if r>>8 < 200 || g>>8 > 40 || b>>8 > 40 {
		t.Errorf("the test image is not red: %d %d %d", r>>8, g>>8, b>>8)
	}
	if why := ImageTooBig(img.Data); why != "" {
		t.Errorf("the test image is too big: %s", why)
	}
}

// The answer is judged whole, however the server splits it: a word cut
// at "red" is not the colour, and a denial that follows the colour is
// still a denial.
func TestProbeVisionJudgesTheWholeAnswer(t *testing.T) {
	cases := []struct {
		deltas []string
		sees   bool
	}{
		{[]string{"The attachment was red", "acted."}, false},
		{[]string{"Red", "? I cannot see an image, so I cannot tell."}, false},
		{[]string{"R", "e", "d"}, true},
		{[]string{"It is ", "red", "."}, true},
	}
	for _, c := range cases {
		got, err := ProbeVision(context.Background(), &probeScript{deltas: c.deltas}, "m")
		if err != nil || got.Sees != c.sees {
			t.Errorf("%q: %+v, %v; want sees=%v", c.deltas, got, err, c.sees)
		}
	}
}

// A probe whose time ran out mid-answer has no answer: what it had said
// is returned as the probe's error, not as a "no".
func TestProbeVisionCutShortIsNoAnswer(t *testing.T) {
	for _, deltas := range [][]string{{"The image is"}, {"Red"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		got, err := ProbeVision(ctx, &probeScript{deltas: deltas, endless: true}, "m")
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || got.Sees || got.Answer != "" {
			t.Errorf("%q then nothing: %+v, %v; want the deadline as the error", deltas, got, err)
		}
	}
}

// Found in review (agy): a reply that reached the token limit was judged
// as if it were whole, so "The attachment was red", cut before "acted.",
// read as seeing for the life of the process.
func TestProbeVisionCutAtTheTokenLimitIsNoAnswer(t *testing.T) {
	for _, deltas := range [][]string{{"The image is"}, {"The attachment was red"}} {
		got, err := ProbeVision(context.Background(), &probeScript{deltas: deltas, stop: "max_tokens"}, "m")
		if !errors.Is(err, errVisionAnswerCut) || got.Sees {
			t.Errorf("%q at the token limit: %+v, %v", deltas, got, err)
		}
	}
}
