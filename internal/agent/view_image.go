package agent

import (
	"context"
	"fmt"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// Whether the model a turn runs on is sent images. Decided once per turn
// from its profile (config.Profile.ViewsImages), and read by the places
// that act on it: hiddenTools, which offers view_image only to a model
// that can use it; takeImages, which keeps an image out of a history that
// cannot carry one even when an agent's own tool list named the tool; and
// requestHistory, which leaves the images already in the history out of a
// request to a model that cannot take them.
type viewsImagesKey struct{}

func withViewsImages(ctx context.Context, yes bool) context.Context {
	return context.WithValue(ctx, viewsImagesKey{}, yes)
}

func viewsImages(ctx context.Context) bool {
	yes, _ := ctx.Value(viewsImagesKey{}).(bool)
	return yes
}

// profileViewsImages is ViewsImages for the model a turn actually sends
// to: a command's model override replaces the profile's model and nothing
// else, so the default is judged on the model that replaced it.
func profileViewsImages(profile config.Profile, modelOverride string) bool {
	if modelOverride != "" {
		profile.Model = modelOverride
	}
	return profile.ViewsImages()
}

// agentViewsImages answers for a delegation target, so the agent list the
// delegation tools show can say which agents can be handed an image.
func (l *Loop) agentViewsImages(ctx context.Context, agentName string) bool {
	sessionID, _ := SessionIDFromContext(ctx)
	_, profile, err := l.profileFor(ctx, sessionID, agentName)
	return err == nil && profile.ViewsImages()
}

// takeImages decides which of a tool call's images go to the model, says
// in the result what happened to the rest, and returns the ones that go.
//
// used is the bytes already attached in this step. The images of one step
// travel in one message, and a message over the limit is refused whole,
// so an image that would cross it is left out with a sentence saying so
// rather than taking every result of the step down with it.
func takeImages(ctx context.Context, res *tools.Result, used *int) []provider.Block {
	if len(res.Images) == 0 {
		return nil
	}
	if !viewsImages(ctx) {
		res.Content += "\n\nThe image was not attached: this model is not sent images. " +
			"If it can see them, set \"vision\": true on its profile; otherwise hand the file's path to an agent that can."
		res.IsError = true
		res.Images = nil
		return nil
	}
	var kept []provider.Block
	for _, img := range res.Images {
		if img.Type != provider.BlockImage || !provider.SupportedImageMediaType(img.MediaType) {
			continue
		}
		if *used+len(img.Data) > provider.MaxImageBytesPerMessage {
			res.Content += fmt.Sprintf("\n\nAn image was not attached: the images of this step would pass %d bytes. "+
				"View it on its own, in a later step.", provider.MaxImageBytesPerMessage)
			res.IsError = true
			continue
		}
		*used += len(img.Data)
		kept = append(kept, img)
	}
	res.Images = kept
	return kept
}

// requestHistory is the session's history as a request carries it: in a
// shape every provider accepts (sendableHistory), and with its images only
// for a model that is sent them.
//
// An image stays in the history for the rest of a session. A conversation
// that pasted a screenshot on a model that can see and then moved to one
// that cannot sent that screenshot on every request after the move, and a
// server that takes no images refuses the whole request, so the session
// could not go on even about something else. The model is told where each
// image was instead, and the history keeps the image itself, so moving
// back to a model that can see sends it again.
func (l *Loop) requestHistory(ctx context.Context, sessionID string) []provider.Message {
	return asSent(ctx, sendableHistory(l.history(sessionID)))
}

// asSent is msgs as the model of ctx's turn is sent them: as they are, or
// with a note in place of each image.
func asSent(ctx context.Context, msgs []provider.Message) []provider.Message {
	if viewsImages(ctx) {
		return msgs
	}
	return imagesAsText(msgs)
}

// imagesAsText is msgs with each image replaced by a sentence saying it was
// left out. A message holding an image gets a new block slice: the old one
// is the stored history's.
func imagesAsText(msgs []provider.Message) []provider.Message {
	out := make([]provider.Message, len(msgs))
	for i, m := range msgs {
		out[i] = m
		var content []provider.Block
		for j, b := range m.Content {
			if b.Type != provider.BlockImage {
				continue
			}
			if content == nil {
				content = append([]provider.Block(nil), m.Content...)
			}
			content[j] = provider.TextBlock(fmt.Sprintf(
				"[An image (%s) was here. It was left out: this model is not sent images.]", b.MediaType))
		}
		if content != nil {
			out[i].Content = content
		}
	}
	return out
}

// imagesLeftOutNotice tells the person that the images they attached did
// not reach the model, and what makes them reach it. The model is told too,
// but a person who pasted a screenshot should not have to learn from the
// answer that it was never seen.
func imagesLeftOutNotice(run modelRun, n int) string {
	what := "the image attached to this message was"
	if n > 1 {
		what = fmt.Sprintf("the %d images attached to this message were", n)
	}
	fix := `set "vision": true on its profile`
	if run.profileName != "" {
		fix = fmt.Sprintf(`set "vision": true on profile %q`, run.profileName)
	}
	return fmt.Sprintf("%s is not sent images, so %s left out of the request. If the model can see images, %s.",
		describeRun(run), what, fix)
}

// eventImages is images in the form the log records them, which is the
// form a user message's images already take, so rehydrateHistory reads
// both with one function.
func eventImages(blocks []provider.Block) []events.Image {
	out := make([]events.Image, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, events.Image{MediaType: b.MediaType, Data: b.Data})
	}
	return out
}

// fallbackTools is the tool list, and the names advertised from it, for a
// fallback within a turn: what the turn started with, less view_image when
// the model it moves to cannot see. Derived from the turn's own list each
// time, so a second fallback to a model that can see has the tool back.
func (l *Loop) fallbackTools(ctx context.Context, turnTools []string, sees bool) ([]string, []string) {
	allowed := turnTools
	if !sees {
		allowed = withoutViewImage(l.Tools, turnTools)
	}
	return allowed, l.Tools.NamesFor(ctx, allowed)
}

// withoutViewImage is a tool allowlist with view_image taken out. nil means
// every registered tool, so it is spelled out before one is removed.
func withoutViewImage(reg *tools.Registry, allowed []string) []string {
	if allowed == nil && reg != nil {
		allowed = reg.Names()
	}
	out := make([]string, 0, len(allowed))
	for _, n := range allowed {
		if n != tools.ViewImageName {
			out = append(out, n)
		}
	}
	return out
}
