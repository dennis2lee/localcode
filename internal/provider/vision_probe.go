package provider

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strings"
	"sync"
)

// This file asks a model whether it can see, by showing it something.
//
// A model's name does not say whether it can. A server names what it
// serves whatever it likes, and the same family ships with and without
// vision. A server's model list says nothing about it either: vLLM, the
// most common thing behind an on-premises endpoint, reports no modality at
// all. What does answer is the model: shown a red square and asked its
// colour, a model that sees says red, a server that takes no images
// refuses the request, and a model that is handed only the text cannot
// say red except by chance.

// VisionProbe is what a model did with the test image.
type VisionProbe struct {
	// Sees is true when the model named the test image's colour.
	Sees bool
	// Answer is what the model said, cut short. Empty when the server
	// refused the image.
	Answer string
	// Refused is the server's refusal when it would not take the image.
	Refused error
}

// visionProbeQuestion is asked beside the image. One word, so a model
// that sees has nothing to say but the colour.
const visionProbeQuestion = "What colour is this image? Answer with one word."

// visionProbeMaxTokens is room for a reasoning model to think before it
// answers.
const visionProbeMaxTokens = 4096

// visionProbeColour matches the answer. A whole word, so "coloured" is not
// "red".
var visionProbeColour = regexp.MustCompile(`(?i)\b(red|crimson|scarlet)\b`)

// visionProbeBlind matches an answer that says it saw nothing. Such an
// answer can still name a colour ("I cannot see an image, so I cannot say
// whether it is red"), and that is not seeing.
var visionProbeBlind = regexp.MustCompile(`(?i)(can(no|')t|unable to|do(es)? not|don'?t) (see|view|access|process)|no image`)

// errNoVisionAnswer is a probe the model answered with nothing: a reasoning
// model that spent its tokens thinking, or a server that ended the stream
// early. It says nothing about vision either way.
var errNoVisionAnswer = errors.New("the model gave no answer")

// errVisionAnswerCut is a probe whose reply reached the token limit, which
// a one-word answer does not: what it said was cut short and is not its
// answer.
var errVisionAnswerCut = errors.New("the answer reached the token limit")

// visionProbeImage is the test image: a 64x64 red square, as PNG. Big
// enough for the smallest input a vision encoder takes.
var visionProbeImage = sync.OnceValue(func() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	red := color.RGBA{R: 220, G: 20, B: 20, A: 255}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.SetRGBA(x, y, red)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err) // an in-memory encode of a valid image does not fail
	}
	return buf.Bytes()
})

// ProbeVision shows model, through p, a red square and asks its colour.
//
// A nil error means the result is an answer: the model named the colour,
// named something else, or the server refused the image. Any other
// failure, such as a rate limit, a timeout, a model that is not found or
// an empty reply, is returned as the error, and says nothing about vision.
//
// The request goes through the same adapter as every other, so a probe
// that passes has also shown that this provider's wire form for an image
// reaches this server intact.
func ProbeVision(ctx context.Context, p Provider, model string) (VisionProbe, error) {
	stream, err := p.Chat(ctx, ChatRequest{
		Model:     model,
		MaxTokens: visionProbeMaxTokens,
		Messages: []Message{{Role: RoleUser, Content: []Block{
			ImageBlock("image/png", visionProbeImage()),
			TextBlock(visionProbeQuestion),
		}}},
	})
	if err != nil {
		if isVisionRefusal(err, model) {
			return VisionProbe{Refused: err}, nil
		}
		return VisionProbe{}, err
	}
	// The whole answer, read to its end. Judged as it arrived, a reply
	// was judged on where the server happened to split it: "red" with
	// "acted." still to come, or "Red" with "? I cannot see an image"
	// still to come, read as seeing.
	var answer strings.Builder
	var streamErr error
	cut := false
	for ev := range stream {
		switch ev.Type {
		case EventTextDelta:
			answer.WriteString(ev.TextDelta)
		case EventMessageStop:
			// Ended at the token limit: the answer was cut, so what it
			// says is not its answer, for the reason a cancel is not.
			cut = ev.StopReason == "max_tokens"
		case EventError:
			if streamErr == nil {
				streamErr = ev.Err
			}
		}
	}
	// A stream that ended because the probe's time ran out, or its
	// turn's, was cut short: what it had said is not its answer. An
	// adapter closes the stream on a cancel without always saying why.
	if err := ctx.Err(); err != nil {
		return VisionProbe{}, err
	}
	if streamErr != nil {
		if isVisionRefusal(streamErr, model) {
			return VisionProbe{Refused: streamErr}, nil
		}
		return VisionProbe{}, streamErr
	}
	said := strings.TrimSpace(answer.String())
	switch {
	case cut:
		return VisionProbe{}, errVisionAnswerCut
	case said == "":
		return VisionProbe{}, errNoVisionAnswer
	case seesColour(said):
		return VisionProbe{Sees: true, Answer: clipAnswer(said)}, nil
	}
	return VisionProbe{Answer: clipAnswer(said)}, nil
}

func seesColour(answer string) bool {
	return visionProbeColour.MatchString(answer) && !visionProbeBlind.MatchString(answer)
}

func clipAnswer(s string) string {
	const max = 80
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
