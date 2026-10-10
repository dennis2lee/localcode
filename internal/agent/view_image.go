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
// from its profile (config.Profile.ViewsImages), and read by the two
// places that act on it: hiddenTools, which offers view_image only to a
// model that can use it, and takeImages, which keeps an image out of a
// history that cannot carry one even when an agent's own tool list named
// the tool.
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
