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
	"time"

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

// SegmentKind says where a piece of an expanded command came from.
//
// The kinds exist because an expansion is not one thing. A command body
// can splice a file off the disk and the output of a shell command into
// the middle of its own text, and the result used to leave here as one
// string with nothing to say about which half the person wrote. What
// reads that string is a model, and a model deciding whether to follow
// an instruction needs to know that this sentence came from the command
// and that one came out of a file.
type SegmentKind string

const (
	// SegmentTemplate is the command's own body: what the person who
	// installed the command wrote.
	SegmentTemplate SegmentKind = "template"
	// SegmentArguments is what the person typed after the command name.
	SegmentArguments SegmentKind = "arguments"
	// SegmentFile is the contents of an @path reference.
	SegmentFile SegmentKind = "file"
	// SegmentShell is the stdout of a !`cmd` reference.
	SegmentShell SegmentKind = "shell"
)

// Segment is one piece of an expanded command, with where it came from
// and, for a file or a shell command, what it was.
type Segment struct {
	Kind SegmentKind
	// Ref is the path for a file segment and the command line for a
	// shell segment. Empty for the template and for arguments.
	Ref  string
	Text string
}

// Expand renders cmd's body against the given raw argument string and
// working directory, returning the text the model receives. See
// ExpandSegments for the same expansion with its seams intact.
func Expand(cmd Command, args, cwd string) (string, error) {
	segs, err := ExpandSegments(cmd, args, cwd)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, seg := range segs {
		b.WriteString(seg.Text)
	}
	return b.String(), nil
}

// ExpandSegments renders cmd's body in a single pass and returns it as
// the pieces it was made of: $ARGUMENTS is replaced with args verbatim,
// $1-$9 with whitespace-split positional fields (empty string if not
// supplied), !`cmd` with the stdout of running cmd via the shell
// (cwd-relative, with $ARGUMENTS/$N substituted into the command first),
// and @path with the contents of the file at path (resolved against cwd).
//
// Concatenating the segments reproduces exactly what Expand returns, so
// the wire format is unchanged and the seams are additional information
// rather than a different request.
func ExpandSegments(cmd Command, args, cwd string) ([]Segment, error) {
	fields := strings.Fields(args)
	var expandErr error
	var segs []Segment

	// The template text between matches, accumulated so consecutive
	// literal stretches are one segment rather than one per gap.
	var pending strings.Builder
	flush := func() {
		if pending.Len() > 0 {
			segs = append(segs, Segment{Kind: SegmentTemplate, Text: pending.String()})
			pending.Reset()
		}
	}
	add := func(kind SegmentKind, ref, text string) {
		flush()
		segs = append(segs, Segment{Kind: kind, Ref: ref, Text: text})
	}

	body := cmd.Body
	last := 0
	for _, loc := range expandPattern.FindAllStringIndex(body, -1) {
		if expandErr != nil {
			break
		}
		pending.WriteString(body[last:loc[0]])
		last = loc[1]
		tok := body[loc[0]:loc[1]]
		switch {
		case tok == "$ARGUMENTS":
			add(SegmentArguments, "", args)
		case len(tok) == 2 && tok[0] == '$': // $1-$9
			value := ""
			if i := int(tok[1] - '0'); i >= 1 && i <= len(fields) {
				value = fields[i-1]
			}
			add(SegmentArguments, tok, value)
		case strings.HasPrefix(tok, "!`"):
			cmdStr := substituteArgs(tok[2:len(tok)-1], args, fields)
			out, err := runShell(cmdStr, cwd)
			if err != nil {
				expandErr = err
				break
			}
			add(SegmentShell, cmdStr, out)
		case strings.HasPrefix(tok, "@"):
			ref := tok[1:]
			path := ref
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwd, path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				expandErr = fmt.Errorf("read @%s: %w", ref, err)
				break
			}
			// The framing is localcode's; only the file's own bytes are
			// the file's, which is what the segment boundary says.
			pending.WriteString("\n--- " + ref + " ---\n")
			add(SegmentFile, ref, string(data))
			pending.WriteString("\n---\n")
		}
	}
	if expandErr != nil {
		return nil, expandErr
	}
	pending.WriteString(body[last:])
	flush()
	return segs, nil
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

// embeddedShellTimeout bounds a `!...` expansion inside a slash command.
//
// It had none, while the bash tool has two minutes and hooks have thirty
// seconds — so one command that never returns held the turn open with no
// way to stop it. Generous, because these are ordinarily `git status` and
// the like, and the cost of being wrong is a command reported as timed
// out rather than one that hangs forever.
const embeddedShellTimeout = 30 * time.Second

func runShell(cmdStr, cwd string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), embeddedShellTimeout)
	defer cancel()
	c := shell.Command(ctx, cmdStr)
	c.Dir = cwd
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("shell command %q did not finish within %s", cmdStr, embeddedShellTimeout)
		}
		return "", fmt.Errorf("shell command %q: %w", cmdStr, err)
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}
