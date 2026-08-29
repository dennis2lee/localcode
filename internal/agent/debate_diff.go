package agent

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"localcode/internal/childproc"
)

// What actually changed, rather than what localcode happened to watch.
//
// The first version of the reviewer's brief listed the files it had seen
// `write_file` and `edit` called on. That is a list of tool calls
// wearing the costume of a diff, and it is wrong in both directions: a
// file the author moved, generated or rewrote through the shell is
// missing from it, and a file written back with identical contents is in
// it. A reviewer that trusts such a list reviews the wrong set.
//
// A git repository already answers the question properly, so it is
// asked. `git diff HEAD` is the state of the work against the last
// commit, cumulative rather than per round — which is the more useful
// answer here anyway: what a reviewer wants in round four is the change
// as it now stands, not the increment since round three.
//
// Outside a repository there is no cheap honest answer, and inventing an
// expensive one (hashing the tree each round) would buy little. There the
// brief falls back to the tool-call list and says that is what it is.

// diffLimit bounds what goes into a prompt. A generated file or a
// checked-in lockfile can be a megabyte of diff on its own, and a
// reviewer given that has spent its whole context before reading a line
// of the change.
const diffLimit = 24000

// gitTimeout bounds each git call. A repository on a slow or
// disconnected network filesystem must not hang a debate.
const gitTimeout = 20 * time.Second

// workspaceDiff is the change in dir against HEAD, plus the files that
// are not tracked at all. ok is false when dir is not a git repository,
// or git is not installed, which is the caller's cue to fall back.
func workspaceDiff(dir string) (diff string, ok bool) {
	if dir == "" || !isGitRepo(dir) {
		return "", false
	}

	var b strings.Builder
	if patch, err := git(dir, "diff", "HEAD"); err == nil && strings.TrimSpace(patch) != "" {
		b.WriteString(patch)
	}
	// Untracked files by name only. Their contents are not in the diff
	// and pasting them would be the largest thing in the brief for a new
	// file of any size; the reviewer has read tools and the path.
	if others, err := git(dir, "ls-files", "--others", "--exclude-standard"); err == nil {
		if names := nonEmptyLines(others); len(names) > 0 {
			sort.Strings(names)
			b.WriteString("\n--- new files, not yet added to git (read them yourself) ---\n")
			for _, name := range names {
				b.WriteString("  " + name + "\n")
			}
		}
	}

	out := strings.TrimSpace(b.String())
	if out == "" {
		// A repository with nothing changed is an answer, not a failure,
		// and it is one the reviewer needs: it means the author changed
		// nothing this time.
		return "", true
	}
	return clampDiff(out), true
}

// clampDiff cuts an oversized diff and says so, rather than sending a
// truncated patch that reads like a complete one.
func clampDiff(diff string) string {
	if len(diff) <= diffLimit {
		return diff
	}
	cut := diff[:diffLimit]
	// Back up to a line boundary so the last hunk shown is not half a
	// line of code.
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return fmt.Sprintf("%s\n... (diff truncated at %d characters — read the files for the rest)", cut, diffLimit)
}

func isGitRepo(dir string) bool {
	out, err := git(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// git runs one git subcommand in dir.
//
// exec directly rather than through a shell: there is no command line to
// compose here, so there is nothing to quote and nothing a filename can
// do to it.
func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Every round of a debate asks git two questions, and on the desktop
	// build a console child gets a window of its own: without this a
	// debate is a stack of black rectangles blinking over the app.
	childproc.Hide(cmd)
	out, err := cmd.Output()
	return string(out), err
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
