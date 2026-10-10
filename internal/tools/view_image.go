package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"localcode/internal/provider"
)

// ViewImage puts an image file in front of the model.
//
// A model that can see was only ever shown the images a person pasted or
// dropped into the prompt. Handed a path instead, which is what every
// delegation hands over, it had read_file, which returns text, and bash,
// which can count pixels and cannot show them. A "vision" agent asked to
// look at a figure did the only thing left to it: it delegated the request
// to "vision" again.
//
// Offered only to a model that can take images: see config.Profile.Vision.
// An image in the history of a model that cannot is a request its server
// refuses, on every turn after, so the tool is hidden rather than left to
// fail.
type ViewImage struct{}

// ViewImageName is the name the tool is registered and hidden under.
const ViewImageName = "view_image"

// MaxViewImageBytes is the largest file the tool attaches. Bedrock's
// Converse API refuses an image over 3.75 MB and the Anthropic API one over
// 5 MB, and an image a server refuses stays in the history, so the limit is
// the smallest of the three wires rather than the 10 MB a person may paste
// across several images at once.
const MaxViewImageBytes = 3_750_000

func (ViewImage) Name() string { return ViewImageName }

func (ViewImage) Description() string {
	return "Look at an image file (PNG, JPEG, GIF or WebP, up to 3.75 MB). The image is attached after " +
		"this call's result, so you see it the way you see an image the person pasted. Use it for " +
		"screenshots, figures, plots and diagrams; use read_file for text."
}

func (ViewImage) InputSchema() json.RawMessage {
	return schema(`{"path":{"type":"string","description":"absolute or relative path to the image file"}}`, "path")
}

func (ViewImage) RequiresPermission(json.RawMessage) bool { return false }

// OutsideClass: reading, as read_file. See outside.go.
func (ViewImage) OutsideClass() OutsideClass { return OutsideRead }

// Subject is the path, so a rule that keeps read_file away from a file can
// be written for this tool too.
func (ViewImage) Subject(input json.RawMessage) string {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return ""
	}
	return args.Path
}

func (ViewImage) Execute(ctx context.Context, input json.RawMessage) Result {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}
	if args.Path == "" {
		return Result{Content: "path is required", IsError: true}
	}
	path := resolve(ctx, args.Path)
	info, err := os.Stat(path)
	if err != nil {
		return Result{Content: fmt.Sprintf("view %s: %v", args.Path, err), IsError: true}
	}
	if info.IsDir() {
		return Result{Content: fmt.Sprintf("%s is a directory, not an image file", args.Path), IsError: true}
	}
	if info.Size() > MaxViewImageBytes {
		return Result{
			Content: fmt.Sprintf("%s is %d bytes, over the %d byte limit for an image. "+
				"Make a smaller copy (scale it down, or save it as JPEG) and view that.",
				args.Path, info.Size(), MaxViewImageBytes),
			IsError: true,
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Content: fmt.Sprintf("view %s: %v", args.Path, err), IsError: true}
	}
	// By content, not by name: a screenshot saved as .png by a tool that
	// wrote JPEG is common, and the media type sent has to be what the
	// bytes are or the server refuses the image.
	mediaType := http.DetectContentType(data)
	if !provider.SupportedImageMediaType(mediaType) {
		return Result{
			Content: fmt.Sprintf("%s is %s, not an image this can show (PNG, JPEG, GIF or WebP). "+
				"Convert it to PNG and view that.", args.Path, mediaType),
			IsError: true,
		}
	}
	return Result{
		Content: fmt.Sprintf("%s: %s, %d bytes. The image follows.", args.Path, mediaType, len(data)),
		Images:  []provider.Block{provider.ImageBlock(mediaType, data)},
	}
}
