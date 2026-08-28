package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type ReadFile struct{}

func (ReadFile) Name() string { return "read_file" }
func (ReadFile) Description() string {
	return "Read a file's contents. Returns text with 1-indexed line numbers."
}
func (ReadFile) InputSchema() json.RawMessage {
	return schema(`{"path":{"type":"string","description":"absolute or relative file path"}}`, "path")
}
func (ReadFile) RequiresPermission(json.RawMessage) bool { return false }

// OutsideClass: reading. See outside.go.
func (ReadFile) OutsideClass() OutsideClass { return OutsideRead }

// Subject is the path being read, so a permission rule can match on it.
//
// Reading needs no permission by default and still needs to be
// rule-matchable: the file that must not be read is a real category
// (a private key, a credential store, a .env), and without a subject
// there was no pattern for a rule to match and no way to write the rule
// at all.
func (ReadFile) Subject(input json.RawMessage) string {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return ""
	}
	return args.Path
}

func (ReadFile) Execute(ctx context.Context, input json.RawMessage) Result {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}

	data, err := os.ReadFile(resolve(ctx, args.Path))
	if err != nil {
		return Result{Content: fmt.Sprintf("read %s: %v", args.Path, err), IsError: true}
	}

	lines := strings.Split(string(data), "\n")
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, line)
	}
	return Result{Content: b.String()}
}
