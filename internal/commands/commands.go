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
	"fmt"
	"os"
	"os/exec"
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

var (
	shellPattern = regexp.MustCompile("!`([^`]*)`")
	filePattern  = regexp.MustCompile(`@(\S+)`)
)

// Expand renders cmd's body against the given raw argument string and
// working directory: $ARGUMENTS is replaced with args verbatim, $1-$9
// with whitespace-split positional fields (empty string if not supplied),
// !`cmd` with the stdout of running cmd via the shell (cwd-relative), and
// @path with the contents of the file at path (resolved against cwd).
func Expand(cmd Command, args, cwd string) (string, error) {
	fields := strings.Fields(args)

	out := strings.ReplaceAll(cmd.Body, "$ARGUMENTS", args)
	for i := 1; i <= 9; i++ {
		val := ""
		if i <= len(fields) {
			val = fields[i-1]
		}
		out = strings.ReplaceAll(out, fmt.Sprintf("$%d", i), val)
	}

	out, err := expandShell(out, cwd)
	if err != nil {
		return "", err
	}
	return expandFiles(out, cwd)
}

func expandShell(text, cwd string) (string, error) {
	var runErr error
	result := shellPattern.ReplaceAllStringFunc(text, func(match string) string {
		if runErr != nil {
			return match
		}
		sub := shellPattern.FindStringSubmatch(match)
		out, err := runShell(sub[1], cwd)
		if err != nil {
			runErr = err
			return match
		}
		return out
	})
	if runErr != nil {
		return "", runErr
	}
	return result, nil
}

func runShell(cmdStr, cwd string) (string, error) {
	c := exec.Command("sh", "-c", cmdStr)
	c.Dir = cwd
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("shell command %q: %w", cmdStr, err)
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

func expandFiles(text, cwd string) (string, error) {
	var readErr error
	result := filePattern.ReplaceAllStringFunc(text, func(match string) string {
		if readErr != nil {
			return match
		}
		relPath := filePattern.FindStringSubmatch(match)[1]
		path := relPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			readErr = fmt.Errorf("read @%s: %w", relPath, err)
			return match
		}
		return fmt.Sprintf("\n--- %s ---\n%s\n---\n", relPath, string(data))
	})
	if readErr != nil {
		return "", readErr
	}
	return result, nil
}
