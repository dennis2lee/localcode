package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type WriteFile struct{}

func (WriteFile) Name() string { return "write_file" }
func (WriteFile) Description() string {
	return "Create a new file or overwrite an existing one with the given content."
}
func (WriteFile) smartDescription() string {
	return "Create a new file, or replace an existing one entirely with the given content. " +
		"Overwriting discards everything the file held — to change part of a file, use edit. " +
		"The result says whether the file was created or replaced, and how many lines it had before."
}
func (WriteFile) InputSchema() json.RawMessage {
	return schema(`{"path":{"type":"string"},"content":{"type":"string"}}`, "path", "content")
}
func (w WriteFile) DescriptionFor(ctx context.Context) string {
	if smartAgent(ctx) {
		return w.smartDescription()
	}
	return w.Description()
}
func (w WriteFile) InputSchemaFor(ctx context.Context) json.RawMessage { return w.InputSchema() }
func (WriteFile) RequiresPermission(json.RawMessage) bool              { return true }

// OutsideClass: writing, the half of the boundary worth asking about
// every time. See outside.go.
func (WriteFile) OutsideClass() OutsideClass { return OutsideWrite }

// Subject exposes the target file path as the permission-rule pattern
// subject, so config can e.g. allow writes under "dist/*" while asking
// for everything else.
func (WriteFile) Subject(input json.RawMessage) string {
	var args struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(input, &args)
	return args.Path
}

func (WriteFile) Execute(ctx context.Context, input json.RawMessage) Result {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}

	path := resolve(ctx, args.Path)
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Result{Content: fmt.Sprintf("mkdir %s: %v", dir, err), IsError: true}
		}
	}

	// Read before the write, so the report can say what was there. Under
	// Smart Agent only: this is a whole extra read of the file on a path
	// that did not need one, and it is worth it for the one thing it can
	// say — a model that reached for write_file when it meant edit has
	// just discarded a file it had read part of, and "wrote 412 bytes"
	// looks exactly like success. "replaced a 340-line file with 12 lines"
	// does not.
	replaced := -1
	if smartAgent(ctx) {
		if old, err := os.ReadFile(path); err == nil {
			replaced = len(splitLines(string(old)))
		}
	}

	if err := os.WriteFile(path, []byte(args.Content), 0o644); err != nil {
		return Result{Content: fmt.Sprintf("write %s: %v", args.Path, err), IsError: true}
	}
	if !smartAgent(ctx) {
		return Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path)}
	}
	now := len(splitLines(args.Content))
	if replaced < 0 {
		return Result{Content: fmt.Sprintf("created %s: %d line(s), %d bytes", args.Path, now, len(args.Content))}
	}
	return Result{Content: fmt.Sprintf("replaced %s entirely: it had %d line(s), it now has %d (%d bytes). "+
		"Everything the file previously held is gone.", args.Path, replaced, now, len(args.Content))}
}
