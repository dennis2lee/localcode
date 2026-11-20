package agent

import (
	"context"
	"fmt"
	"time"

	"localcode/internal/config"
	"localcode/internal/provider"
)

// Whether a model is sent images, for a model its profile says nothing
// about.
//
// The profile's "vision" is the answer when it is set, and a Claude model
// sees. Every other model is shown a test image the first time a turn runs
// on it (provider.ProbeVision), and what it does with the image is the
// answer for as long as this process runs. That is what lets a model
// served on-premises under any name receive images without anyone having
// to know it can.

// visionVerdict is whether a model is sent images, and why, in words a
// notice can carry.
type visionVerdict struct {
	sees bool
	why  string
}

// visionEntry is one cached verdict. A zero until is a verdict that lasts
// for the process: the model answered. A probe that failed for some other
// reason is kept until a set time, so a server that is down is not asked
// again on every turn, and is asked again once it may be back.
type visionEntry struct {
	verdict visionVerdict
	until   time.Time
}

// visionProbeTimeout bounds one probe. Long enough for a large model's
// first token on a busy server, which is most of what a probe waits on.
const visionProbeTimeout = 30 * time.Second

// visionRetryAfter is how long a probe that got no answer is remembered
// before a later turn asks again.
const visionRetryAfter = 5 * time.Minute

// visionAskAgainAfter is how long a "no" is remembered: a wrong answer, or
// a refusal. Not for the process, as a "yes" is, because a refusal is read
// from the error's words, and a server that failed for another reason in
// words that mention an image would otherwise keep images from a model
// that can see until localcode restarts. One small request an hour is the
// price.
const visionAskAgainAfter = time.Hour

// seesImages is whether the model profile runs, with modelOverride in
// place of its model, is sent images. It asks the model when nothing else
// can say, and waits for that answer.
func (l *Loop) seesImages(ctx context.Context, profile config.Profile, modelOverride string) visionVerdict {
	return l.visionFor(ctx, profile, modelOverride, true)
}

// knownSeesImages is seesImages without asking: an unasked model is not
// sent images. For the places that describe a model rather than send to
// it, such as the delegation tools' agent list, where a probe per agent
// would put a request on the wire for every description rendered.
func (l *Loop) knownSeesImages(profile config.Profile, modelOverride string) visionVerdict {
	return l.visionFor(context.Background(), profile, modelOverride, false)
}

// profileWithModel is profile with modelOverride in place of its model,
// when there is one: what a command's model override sends to.
func profileWithModel(profile config.Profile, modelOverride string) config.Profile {
	if modelOverride != "" {
		profile.Model = modelOverride
	}
	return profile
}

func (l *Loop) visionFor(ctx context.Context, profile config.Profile, modelOverride string, ask bool) visionVerdict {
	profile = profileWithModel(profile, modelOverride)
	if sees, known := profile.VisionSetting(); known {
		if profile.Vision != nil {
			return visionVerdict{sees: sees, why: fmt.Sprintf(`its profile sets "vision": %t`, sees)}
		}
		return visionVerdict{sees: sees, why: "it is a Claude model"}
	}
	if l.ProbeVision == nil {
		return visionVerdict{why: `its profile does not set "vision"`}
	}
	key := profile.Provider + "\x00" + profile.Model
	for {
		l.mu.Lock()
		entry, cached := l.visionCache[key]
		if cached && (entry.until.IsZero() || time.Now().Before(entry.until)) {
			l.mu.Unlock()
			return entry.verdict
		}
		if !ask {
			l.mu.Unlock()
			return visionVerdict{why: "it has not been shown a test image yet"}
		}
		// One probe per model at a time: two sessions starting on the
		// same model wait for the same answer rather than asking twice.
		if wait, running := l.visionInflight[key]; running {
			l.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return visionVerdict{why: "the turn ended before its test image was answered"}
			}
		}
		done := make(chan struct{})
		if l.visionInflight == nil {
			l.visionInflight = map[string]chan struct{}{}
		}
		l.visionInflight[key] = done
		l.mu.Unlock()

		entry = l.probeVision(ctx, profile)

		l.mu.Lock()
		if ctx.Err() == nil {
			if l.visionCache == nil {
				l.visionCache = map[string]visionEntry{}
			}
			l.visionCache[key] = entry
		}
		delete(l.visionInflight, key)
		close(done)
		l.mu.Unlock()
		return entry.verdict
	}
}

// probeVision shows the model the test image and turns what it did into a
// verdict.
func (l *Loop) probeVision(ctx context.Context, profile config.Profile) visionEntry {
	pctx, cancel := context.WithTimeout(ctx, visionProbeTimeout)
	defer cancel()
	probe, err := l.ProbeVision(pctx, profile.Provider, profile.Model)
	switch {
	case err != nil:
		return visionEntry{
			verdict: visionVerdict{why: "its test image got no answer (" + firstLine(err.Error()) + ")"},
			until:   time.Now().Add(visionRetryAfter),
		}
	case probe.Sees:
		return visionEntry{verdict: visionVerdict{sees: true, why: fmt.Sprintf("it named the colour of a test image (%q)", probe.Answer)}}
	case probe.Refused != nil:
		return visionEntry{
			verdict: visionVerdict{why: "its server refused a test image (" + firstLine(probe.Refused.Error()) + ")"},
			until:   time.Now().Add(visionAskAgainAfter),
		}
	default:
		return visionEntry{
			verdict: visionVerdict{why: fmt.Sprintf("it did not name the colour of a test image (it said %q)", probe.Answer)},
			until:   time.Now().Add(visionAskAgainAfter),
		}
	}
}

// forgetVisionOnRefusal drops what the test image said about the model of
// profile when a turn's request to it was refused for carrying an image.
// The server is the authority, and a server can stop taking images under
// a model name that took them, as a local one does when another model is
// loaded under the same name. The next turn asks again, and a model that
// now refuses is sent notes in place of images, so the conversation goes
// on rather than failing until localcode restarts.
func (l *Loop) forgetVisionOnRefusal(profile config.Profile, err error) {
	if !provider.IsVisionRefusal(err, profile.Model) {
		return
	}
	l.mu.Lock()
	delete(l.visionCache, profile.Provider+"\x00"+profile.Model)
	l.mu.Unlock()
}

// ProbeVisionWith is the ProbeVision hook for a set of providers: the
// test image goes to the provider the profile names, through the same
// adapter its turns use.
func ProbeVisionWith(providers map[string]provider.Provider) func(context.Context, string, string) (provider.VisionProbe, error) {
	return func(ctx context.Context, providerKey, model string) (provider.VisionProbe, error) {
		p, ok := providers[providerKey]
		if !ok {
			return provider.VisionProbe{}, fmt.Errorf("no provider %q", providerKey)
		}
		return provider.ProbeVision(ctx, p, model)
	}
}
