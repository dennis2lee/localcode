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

// smartDescription and the paging schema below are the Smart Agent file
// viewer. See aci.go for why a viewer that pages beats one that does not:
// a whole file is the one tool result whose size nobody chose, and the
// model pays for every line of it on every later step of the turn.
func (ReadFile) smartDescription() string {
	return fmt.Sprintf("Read a file's contents. Returns text with 1-indexed line numbers. "+
		"Long files come back one window at a time (%d lines by default) — pass offset to start "+
		"somewhere else and limit to change the size. The result says how many lines the file has, "+
		"so you always know whether you have seen all of it.", defaultReadLines)
}

func (ReadFile) InputSchema() json.RawMessage {
	return schema(`{"path":{"type":"string","description":"absolute or relative file path"}}`, "path")
}

func (ReadFile) smartSchema() json.RawMessage {
	return schema(`{`+
		`"path":{"type":"string","description":"absolute or relative file path"},`+
		`"offset":{"type":"integer","description":"1-indexed line to start at; defaults to the first line"},`+
		`"limit":{"type":"integer","description":"how many lines to return; defaults to 800"}`+
		`}`, "path")
}

func (r ReadFile) DescriptionFor(ctx context.Context) string {
	if smartAgent(ctx) {
		return r.smartDescription()
	}
	return r.Description()
}

func (r ReadFile) InputSchemaFor(ctx context.Context) json.RawMessage {
	if smartAgent(ctx) {
		return r.smartSchema()
	}
	return r.InputSchema()
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

// defaultReadLines is how much of a file one Smart Agent read returns when
// the call did not say.
//
// The interface work that found paging mattered used a hundred lines, on
// models two generations older than the ones this runs against; a hundred
// turns an ordinary source file into four round trips, and a round trip
// costs the whole conversation resent. Eight hundred covers most source
// files whole and still refuses to put a generated table or a lockfile
// into the context in one call. The number is in the tool description, so
// a model that wants more can say so.
const defaultReadLines = 800

func (ReadFile) Execute(ctx context.Context, input json.RawMessage) Result {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}

	path := resolve(ctx, args.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Content: fmt.Sprintf("read %s: %v", args.Path, err), IsError: true}
	}

	if !smartAgent(ctx) {
		lines := strings.Split(string(data), "\n")
		var b strings.Builder
		for i, line := range lines {
			fmt.Fprintf(&b, "%6d\t%s\n", i+1, line)
		}
		return Result{Content: b.String()}
	}

	// A binary file rendered as numbered text is thousands of lines of
	// mojibake that mean nothing to anybody and cannot be un-read: it is in
	// the history for the rest of the session. Naming the file's size is
	// more use than its bytes.
	if looksBinary(data[:min(len(data), binarySniff)]) {
		return Result{
			Content: fmt.Sprintf("%s looks like a binary file (%d bytes) — not shown as text. "+
				"Use bash with a tool that understands the format if you need its contents.",
				args.Path, len(data)),
			IsError: true,
		}
	}

	lines := splitLines(string(data))
	total := len(lines)

	offset := args.Offset
	if offset <= 0 {
		offset = 1
	}
	if offset > total {
		return Result{
			Content: fmt.Sprintf("%s has %d line(s); offset %d is past the end", args.Path, total, offset),
			IsError: true,
		}
	}
	limit := args.Limit
	if limit <= 0 {
		limit = defaultReadLines
	}

	start := offset - 1
	end := min(start+limit, total)
	body := numbered(lines[start:end], offset)

	// The footer is the whole point of the window: without it a model that
	// receives 800 lines cannot tell a file that is 800 lines long from one
	// that is eight thousand, and it answers about the part it was given as
	// though that were the file.
	if end < total || offset > 1 {
		body += fmt.Sprintf("\n[lines %d-%d of %d in %s", offset, end, total, args.Path)
		if end < total {
			body += fmt.Sprintf("; read on with offset=%d", end+1)
		}
		body += "]\n"
	}
	return Result{Content: body}
}
