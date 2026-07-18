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
func (WriteFile) InputSchema() json.RawMessage {
	return schema(`{"path":{"type":"string"},"content":{"type":"string"}}`, "path", "content")
}
func (WriteFile) RequiresPermission(json.RawMessage) bool { return true }

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

func (WriteFile) Execute(_ context.Context, input json.RawMessage) Result {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}

	if dir := filepath.Dir(args.Path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Result{Content: fmt.Sprintf("mkdir %s: %v", dir, err), IsError: true}
		}
	}

	if err := os.WriteFile(args.Path, []byte(args.Content), 0o644); err != nil {
		return Result{Content: fmt.Sprintf("write %s: %v", args.Path, err), IsError: true}
	}
	return Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path)}
}
