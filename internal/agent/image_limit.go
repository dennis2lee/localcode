package agent

import (
	"context"
	"fmt"

	"localcode/internal/config"
	"localcode/internal/provider"
)

// How many images a request to one model may carry, when its server takes
// fewer than provider.MaxImagesPerRequest.
//
// vLLM caps the images in one prompt with --limit-mm-per-prompt, and
// refuses a request past the cap: "At most 1 image(s) may be provided in
// one prompt." The images stay in the conversation, so every request after
// it carried the same count and was refused the same way. The cap is read
// from the refusal, kept per provider and model for as long as this
// process runs, and a request to that model carries the newest images
// within it, with a note in place of each older one (see newestImages).

type imageLimitKey struct{}

// maxImageCapRetries bounds how often a turn asks one model again for its
// server's cap. The count starts over on each fallback.
const maxImageCapRetries = 1

// withImageLimit is ctx carrying the most images a request may carry, for
// asSent to read. Zero means no cap of the server's own.
func withImageLimit(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, imageLimitKey{}, n)
}

// imagesPerRequest is the most images a request of ctx's turn carries:
// provider.MaxImagesPerRequest, or the server's own cap when it is lower.
func imagesPerRequest(ctx context.Context) int {
	if n, _ := ctx.Value(imageLimitKey{}).(int); n > 0 && n < provider.MaxImagesPerRequest {
		return n
	}
	return provider.MaxImagesPerRequest
}

// imageLimitFor is the cap learned for the model of profile, or zero.
func (l *Loop) imageLimitFor(profile config.Profile) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.imageLimits[profile.Provider+"\x00"+profile.Model]
}

// learnImageLimit reads err for a server's cap on images and keeps it for
// the model of profile. It reports whether the request that was refused
// carried more images than the cap, which is when sending it again with
// fewer can succeed; a refusal of a request already within its cap is not
// one this can do anything about, and saying no to it is what keeps the
// retry from repeating.
func (l *Loop) learnImageLimit(profile config.Profile, err error, carried int) (int, bool) {
	n, ok := provider.ImageCountLimit(err)
	if !ok || carried <= n {
		return 0, false
	}
	l.mu.Lock()
	if l.imageLimits == nil {
		l.imageLimits = map[string]int{}
	}
	key := profile.Provider + "\x00" + profile.Model
	if old, had := l.imageLimits[key]; !had || n < old {
		l.imageLimits[key] = n
	}
	l.mu.Unlock()
	return n, true
}

// imageLimitNotice tells the person why older images stopped reaching the
// model. Said once, when the cap is learned.
func imageLimitNotice(run modelRun, n int) string {
	what := "1 image"
	if n != 1 {
		what = fmt.Sprintf("%d images", n)
	}
	return fmt.Sprintf("%s's server takes at most %s in a request, so it is sent the newest %s and a note in place of each older one; asking again. Raise the server's limit (vLLM: --limit-mm-per-prompt) and restart localcode to send more.",
		describeRun(run), what, what)
}

// capLeftOutNotice tells the person that a cap learned earlier left some
// of the images they attached out of the request. The model is told by a
// note in place of each; a person should not have to learn it from the
// answer.
func capLeftOutNotice(run modelRun, cap, attached int) string {
	what := "1 image"
	if cap != 1 {
		what = fmt.Sprintf("%d images", cap)
	}
	verb := "were"
	if attached-cap == 1 {
		verb = "was"
	}
	return fmt.Sprintf("%s's server takes at most %s in a request, so %d of the %d images attached to this message %s left out. Raise the server's limit (vLLM: --limit-mm-per-prompt) and restart localcode to send more.",
		describeRun(run), what, attached-cap, attached, verb)
}
