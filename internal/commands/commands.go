// Package commands implements opencode-style custom slash commands: a
// Markdown file with optional YAML frontmatter (description/agent/model)
// whose body is a prompt template, invoked as "/<filename>". The body
// supports "$ARGUMENTS" (the whole argument string), "$1".."$9"
// (positional arguments), "!`shell command`" (inlines the command's
// stdout), and "@path" (inlines a file's contents) — the same expansion
// primitives opencode's commands use.
package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"localcode/internal/shell"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Command struct {
	Name        string
	Description string
	Agent       string
	Model       string
	Body        string
	Path        string
}

type frontmatter struct {
	Description string `yaml:"description"`
	Agent       string `yaml:"agent"`
	Model       string `yaml:"model"`
}

// LoadAll scans each directory in dirs for "*.md" files, one command per
// file (the filename minus its extension becomes the command name).
// Directories are scanned in order and a name seen in an earlier directory
// wins over the same name in a later one — list project-local command
// dirs before the global one so a project can override a global command.
func LoadAll(dirs ...string) ([]Command, error) {
	var out []Command
	seen := map[string]bool{}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read commands dir %s: %w", dir, err)
		}

		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if seen[name] {
				continue
			}

			path := filepath.Join(dir, e.Name())
			cmd, err := parseCommandFile(path)
			if err != nil {
				continue // skip malformed commands rather than failing startup
			}
			cmd.Name = name
			seen[name] = true
			out = append(out, cmd)
		}
	}
	return out, nil
}

func parseCommandFile(path string) (Command, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Command{}, err
	}
	content := string(data)

	var fm frontmatter
	body := content
	if strings.HasPrefix(content, "---\n") {
		rest := content[len("---\n"):]
		if end := strings.Index(rest, "\n---"); end >= 0 {
			if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
				return Command{}, fmt.Errorf("%s: parse frontmatter: %w", path, err)
			}
			body = strings.TrimPrefix(rest[end+len("\n---"):], "\n")
		}
	}

	return Command{
		Description: fm.Description,
		Agent:       fm.Agent,
		Model:       fm.Model,
		Body:        body,
		Path:        path,
	}, nil
}

// expandPattern matches, as whole tokens, each construct Expand knows:
// $ARGUMENTS, a positional $1-$9, a !`shell command`, or an @file
// reference. Matching all four in one alternation lets Expand run a single
// left-to-right pass — so substituted content (a shell command's output,
// or an argument value) is never itself re-scanned for further directives.
// That matters for safety: without it, `!`echo @/etc/passwd“ would read
// /etc/passwd, and an argument like "@/secret" spliced via $ARGUMENTS
// would too.
var expandPattern = regexp.MustCompile("\\$ARGUMENTS|\\$[1-9]|!`[^`]*`|@\\S+")

// Expand renders cmd's body against the given raw argument string and
// working directory in a single pass: $ARGUMENTS is replaced with args
// verbatim, $1-$9 with whitespace-split positional fields (empty string if
// not supplied), !`cmd` with the stdout of running cmd via the shell
// (cwd-relative, with $ARGUMENTS/$N substituted into the command first),
// and @path with the contents of the file at path (resolved against cwd).
func Expand(cmd Command, args, cwd string) (string, error) {
	fields := strings.Fields(args)
	var expandErr error

	out := expandPattern.ReplaceAllStringFunc(cmd.Body, func(tok string) string {
		if expandErr != nil {
			return tok
		}
		switch {
		case tok == "$ARGUMENTS":
			return args
		case len(tok) == 2 && tok[0] == '$': // $1-$9
			if i := int(tok[1] - '0'); i >= 1 && i <= len(fields) {
				return fields[i-1]
			}
			return ""
		case strings.HasPrefix(tok, "!`"):
			cmdStr := substituteArgs(tok[2:len(tok)-1], args, fields)
			out, err := runShell(cmdStr, cwd)
			if err != nil {
				expandErr = err
				return tok
			}
			return out
		case strings.HasPrefix(tok, "@"):
			ref := tok[1:]
			path := ref
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				expandErr = fmt.Errorf("read @%s: %w", ref, err)
				return tok
			}
			return fmt.Sprintf("\n--- %s ---\n%s\n---\n", ref, string(data))
		}
		return tok
	})

	if expandErr != nil {
		return "", expandErr
	}
	return out, nil
}

// substituteArgs replaces $ARGUMENTS and $1-$9 in a shell command string,
// so a command template can pass its arguments through to the shell (e.g.
// !`grep $1 somefile`). Only used for the shell-command text itself, not
// re-applied to that command's output.
func substituteArgs(s, args string, fields []string) string {
	s = strings.ReplaceAll(s, "$ARGUMENTS", args)
	for i := 1; i <= 9; i++ {
		val := ""
		if i <= len(fields) {
			val = fields[i-1]
		}
		s = strings.ReplaceAll(s, fmt.Sprintf("$%d", i), val)
	}
	return s
}

func runShell(cmdStr, cwd string) (string, error) {
	c := shell.Command(context.Background(), cmdStr)
	c.Dir = cwd
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("shell command %q: %w", cmdStr, err)
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}
