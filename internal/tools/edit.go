package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Edit struct{}

func (Edit) Name() string { return "edit" }
func (Edit) Description() string {
	return "Replace an exact substring in a file with new text. old_string must match exactly once unless replace_all is set."
}
func (Edit) InputSchema() json.RawMessage {
	return schema(`{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}}`, "path", "old_string", "new_string")
}
func (Edit) RequiresPermission(json.RawMessage) bool { return true }

// Subject exposes the target file path as the permission-rule pattern
// subject (see WriteFile.Subject).
func (Edit) Subject(input json.RawMessage) string {
	var args struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(input, &args)
	return args.Path
}

func (Edit) Execute(_ context.Context, input json.RawMessage) Result {
	var args struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}

	data, err := os.ReadFile(args.Path)
	if err != nil {
		return Result{Content: fmt.Sprintf("read %s: %v", args.Path, err), IsError: true}
	}
	content := string(data)

	count := strings.Count(content, args.OldString)
	if count == 0 {
		return Result{Content: "old_string not found in file", IsError: true}
	}
	if count > 1 && !args.ReplaceAll {
		return Result{Content: fmt.Sprintf("old_string is not unique (%d matches); pass replace_all or add more context", count), IsError: true}
	}

	var updated string
	if args.ReplaceAll {
		updated = strings.ReplaceAll(content, args.OldString, args.NewString)
	} else {
		updated = strings.Replace(content, args.OldString, args.NewString, 1)
	}

	if err := os.WriteFile(args.Path, []byte(updated), 0o644); err != nil {
		return Result{Content: fmt.Sprintf("write %s: %v", args.Path, err), IsError: true}
	}
	return Result{Content: fmt.Sprintf("replaced %d occurrence(s) in %s", count, args.Path)}
}
