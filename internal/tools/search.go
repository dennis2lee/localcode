package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Glob struct{}

func (Glob) Name() string        { return "glob" }
func (Glob) Description() string { return "List files matching a glob pattern, e.g. \"src/**/*.go\"." }
func (Glob) InputSchema() json.RawMessage {
	return schema(`{"pattern":{"type":"string"}}`, "pattern")
}
func (Glob) RequiresPermission(json.RawMessage) bool { return false }

func (Glob) Execute(_ context.Context, input json.RawMessage) Result {
	var args struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}

	matches, err := doubleStarGlob(args.Pattern)
	if err != nil {
		return Result{Content: fmt.Sprintf("glob %s: %v", args.Pattern, err), IsError: true}
	}
	sort.Strings(matches)
	return Result{Content: strings.Join(matches, "\n")}
}

// doubleStarGlob supports "**" (recursive) in addition to filepath.Glob's
// single-level "*", since that's the pattern models reach for by default.
func doubleStarGlob(pattern string) ([]string, error) {
	if !strings.Contains(pattern, "**") {
		return filepath.Glob(pattern)
	}

	parts := strings.SplitN(pattern, "**", 2)
	root := strings.TrimSuffix(parts[0], "/")
	if root == "" {
		root = "."
	}
	suffix := strings.TrimPrefix(parts[1], "/")

	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if suffix == "" {
			out = append(out, path)
			return nil
		}
		ok, _ := filepath.Match(suffix, filepath.Base(path))
		if ok {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

type Grep struct{}

func (Grep) Name() string { return "grep" }
func (Grep) Description() string {
	return "Search file contents for a regex pattern under a path (recursive)."
}
func (Grep) InputSchema() json.RawMessage {
	return schema(`{"pattern":{"type":"string"},"path":{"type":"string","description":"file or directory to search; defaults to \".\""}}`, "pattern")
}
func (Grep) RequiresPermission(json.RawMessage) bool { return false }

func (Grep) Execute(_ context.Context, input json.RawMessage) Result {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}
	if args.Path == "" {
		args.Path = "."
	}

	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return Result{Content: fmt.Sprintf("invalid regex: %v", err), IsError: true}
	}

	var b strings.Builder
	matches := 0
	const maxMatches = 200

	walkErr := filepath.WalkDir(args.Path, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || matches >= maxMatches {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil // best-effort: skip unreadable files
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			if re.MatchString(scanner.Text()) {
				fmt.Fprintf(&b, "%s:%d:%s\n", path, lineNo, scanner.Text())
				matches++
				if matches >= maxMatches {
					break
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return Result{Content: fmt.Sprintf("grep %s: %v", args.Path, walkErr), IsError: true}
	}
	if matches == 0 {
		return Result{Content: "no matches"}
	}
	return Result{Content: b.String()}
}
