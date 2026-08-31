package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
)

// The Smart Agent agent-computer interface.
//
// This file exists because of a finding that is not localcode's. Hold the
// model, the prompt and the problem fixed, change only the program driving
// them, and the result moves: published comparisons of one model across
// several coding harnesses spread by several points on the same benchmark.
// The reported causes are not clever ones. They are the shape of what the
// tools hand back: whether a search admits it stopped early, whether a
// failed edit says why it failed, whether a read can ask for part of a file
// instead of all of it.
//
// The two design papers behind most of it say the same thing from opposite
// ends. The agent-computer-interface work found that a file viewer that
// pages, a search that summarises rather than dumps, and an edit that
// reports precisely what went wrong were worth more than model changes. The
// context-engineering work found that a model's recall degrades as its
// window fills, which makes every avoidable token a tool returns a tax on
// every later step of the same turn.
//
// So this is the other half of Smart Agent. internal/smart is the roster
// and the orchestration prompt: who does the work. This is what they do it
// with — and it matters most there, because four of the six specialists
// have only the read-only set (read_file, glob, grep, Skill), and the
// cheapest model in the roster is the one holding it.
//
// It is behind the same switch, pinned the same way, for the same reason
// the roster is: a turn that was shown a tool's schema with the switch on
// must not execute that tool with it off.
//
// Three things here are not behind it, and the line between them is worth
// stating because everything else in this file sits on one side of it.
// Most of what follows makes an answer better: a window instead of a whole
// file, a budget one generated table cannot monopolise, a diagnosis
// instead of "not found". Those are a way of working, and a way of working
// is something to opt into. But grep saying nothing when its match budget
// ran out, when a long line ended a file's scan, or when a file could not
// be opened at all, are not worse answers. They are a search that did not
// look at part of the tree and did not say so, and a defect does not
// become a feature by being fixed next to one. All three apply with the
// switch off. See grepWalk.

type smartKey struct{}

// WithSmartAgent pins the Smart Agent setting for tools run under ctx.
//
// The tools package deliberately does not import internal/config — it
// mirrors the permission vocabulary rather than depending on it — so the
// switch has to arrive here as its own value. Only the agent loop sets it,
// in the one place that pins the config-side snapshot, so the two cannot
// come apart: see (*Loop).pinSmart.
func WithSmartAgent(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, smartKey{}, on)
}

// smartAgent reports whether the work under ctx was admitted with Smart
// Agent on. Unset reads as off, which is what a bare context in a test
// wants and what every caller outside a turn gets.
func smartAgent(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	on, _ := ctx.Value(smartKey{}).(bool)
	return on
}

// notSource are the directories a code search walks into and never wants:
// version-control internals and package caches. Nothing in them is
// somebody's source, all of them are large, and .git in particular is full
// of compressed objects that match text patterns by coincidence.
//
// Deliberately short. It would be easy to add "vendor", "build", "dist"
// and "target" and make searches faster still, and it would be wrong: each
// of those is real source in some project, and a search that silently
// refuses to look somewhere is the failure this whole file is against. The
// budget notice handles the rest — a search that stops early says so, and
// the model narrows it.
var notSource = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "__pycache__": true,
	".mypy_cache": true, ".pytest_cache": true, ".ruff_cache": true,
}

// binarySniff is how much of a file is examined to decide whether it is
// text. A NUL byte in the first few KB is what every grep in existence
// uses, and it is right often enough that nothing better is worth the
// read.
const binarySniff = 8000

// looksBinary reports whether head — the first bytes of a file — contains a
// NUL.
func looksBinary(head []byte) bool {
	for _, b := range head {
		if b == 0 {
			return true
		}
	}
	return false
}

// maxLineBytes is the longest single line the scanners will read.
//
// bufio.Scanner's default is 64KB, and past it Scan returns false with no
// output and no error the caller was checking — so grep over a repository
// containing one minified bundle or one long generated table reported "no
// matches" for a file that contained the pattern twice. A false negative is
// the worst answer a search can give: the model concludes the symbol does
// not exist and works from there.
//
// A megabyte covers real minified files. Past it the file is reported as
// unsearchable rather than dropped, which is the same principle one line
// down: say what was not looked at.
const maxLineBytes = 1 << 20

// lineScanner is bufio.NewScanner with a buffer that can hold a real
// generated file.
func lineScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	return s
}

// skipDir reports whether a walk under Smart Agent should descend into a
// directory. The root itself is always entered, even when it is named
// ".git" — someone who points a search at that path has asked for it.
func skipDir(root, path string, name string) bool {
	if path == root {
		return false
	}
	return notSource[name]
}

// walkNotice is the sentence a search appends when it did not look at
// everything. Empty when it did.
//
// Every field here is a thing the caller would otherwise have swallowed.
// The old grep hit its 200-match cap and returned 200 lines, with the same
// shape a complete answer has: no count, no marker, nothing to tell a
// reader that the file they were looking for lost its place to 199 hits in
// one generated table. That is not a truncated answer, it is a wrong one.
type walkNotice struct {
	capped     bool // the total budget ran out
	limit      int
	binary     int      // files skipped for containing NUL bytes
	unreadable []string // files that could not be opened at all
	tooLong    []string // files with a line past maxLineBytes
	skipped    []string // directories not descended into
}

func (n walkNotice) String() string {
	var parts []string
	if n.capped {
		parts = append(parts, fmt.Sprintf("stopped at the %d-result limit — narrow the pattern, or pass a path, to see the rest", n.limit))
	}
	if n.binary > 0 {
		parts = append(parts, fmt.Sprintf("skipped %d binary file(s)", n.binary))
	}
	if len(n.unreadable) > 0 {
		parts = append(parts, "could not open "+nameList(n.unreadable))
	}
	if len(n.tooLong) > 0 {
		parts = append(parts, fmt.Sprintf("could not search %s — it has a line longer than %dKB", nameList(n.tooLong), maxLineBytes/1024))
	}
	if len(n.skipped) > 0 {
		parts = append(parts, "did not descend into "+nameList(n.skipped))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

// namesShown is how many paths a notice lists before it starts counting.
//
// Names rather than a count, because a count is not something anybody can
// act on. "could not open 2 file(s)" on every search of a project with one
// dangling symlink is a line that repeats forever and is never once useful;
// "could not open link.txt" names a thing to go and delete. Three, because
// past that the list stops being a name and becomes a wall, and the tail is
// the same story as the head.
const namesShown = 3

// nameList renders paths, sorted and deduplicated, at most namesShown of
// them.
func nameList(in []string) string {
	names := dedupe(append([]string(nil), in...))
	sort.Strings(names)
	if len(names) <= namesShown {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:namesShown], ", "), len(names)-namesShown)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// numbered renders lines with 1-indexed line numbers starting at first, in
// the same "%6d\t" shape read_file has always used.
func numbered(lines []string, first int) string {
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%6d\t%s\n", first+i, line)
	}
	return b.String()
}

// splitLines splits file text into lines without inventing a final empty
// one for the trailing newline every text file ends with.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// relName is one path relative to base, for display — relativeTo for a
// single path rather than a slice, since the walks below report as they go.
func relName(base, path string) string {
	return relativeTo(base, []string{path})[0]
}
