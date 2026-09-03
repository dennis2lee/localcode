package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// Leaving the project is two questions, not one.
//
// The workspace boundary used to have a single answer: outside is outside,
// and every path that landed there asked. That is the right shape for a
// warning and the wrong shape for a setting, because the two things a
// model does out there are not the same risk. Reading a header in
// /usr/include, a config in ~/.config, the sibling repository this one
// depends on: ordinary work, and being asked about each of them is how a
// person learns to press y without reading. Writing to a directory this
// conversation was never told about is the failure the boundary exists to
// catch, and it is worth one question every time until somebody says
// otherwise.
//
// So a tool that takes a path says which of the two it is doing, and the
// two have separate switches.

// OutsideClass is which half of the boundary a call falls under.
type OutsideClass int

const (
	// OutsideNone is a tool the boundary has nothing to say about: its
	// subject is not a path. bash is the important member — a shell
	// command is not a path, and "cd /etc && cat passwd" cannot be judged
	// by looking at it. bash asks by default for its own reasons, and
	// this guard does not pretend to cover it.
	OutsideNone OutsideClass = iota
	OutsideRead
	OutsideWrite
)

func (c OutsideClass) String() string {
	switch c {
	case OutsideRead:
		return "read"
	case OutsideWrite:
		return "write"
	}
	return ""
}

// ParseOutsideClass is String's inverse, for a class that went through a
// log and came back as text. The empty string is OutsideNone and is not
// reported as a failure; anything else unknown is.
func ParseOutsideClass(s string) (OutsideClass, bool) {
	switch s {
	case "":
		return OutsideNone, true
	case "read":
		return OutsideRead, true
	case "write":
		return OutsideWrite, true
	}
	return OutsideNone, false
}

// PathTool is implemented by tools whose permission subject is a
// filesystem path, and says whether the call reads it or writes it.
//
// Declared by the tool rather than looked up in a table here, so a tool
// added later cannot be silently left out of the boundary: the question
// "does this touch a path, and how" is asked where the answer is known.
type PathTool interface {
	OutsideClass() OutsideClass
}

// ClassOf reports the boundary class of a tool, OutsideNone for anything
// that does not declare one.
func ClassOf(t Tool) OutsideClass {
	if pt, ok := t.(PathTool); ok {
		return pt.OutsideClass()
	}
	return OutsideNone
}

// OutsideDir is the directory an "allow this directory" answer covers:
// the physical directory the subject lives in.
//
// A directory rather than the file, because a file is not a useful unit
// of consent. A model told to read a sibling project reads forty files in
// it, and being asked forty times is not forty decisions; it is one
// decision and thirty-nine keystrokes, which is how a permission prompt
// stops being read.
//
// The subject itself when it is a directory (grep is pointed at one), its
// parent otherwise, and the parent again for a path that does not exist
// yet, which is the ordinary case for a write.
func OutsideDir(ctx context.Context, subject string) string {
	full := subject
	if full == "" {
		return ""
	}
	if !filepath.IsAbs(full) {
		if dir := WorkingDir(ctx); dir != "" {
			full = filepath.Join(dir, full)
		}
	}
	if fi, err := os.Stat(full); err == nil && fi.IsDir() {
		if resolved, err := resolvePhysical(full); err == nil {
			return resolved
		}
		return filepath.Clean(full)
	}
	parent := filepath.Dir(full)
	if resolved, err := resolvePhysical(parent); err == nil {
		return resolved
	}
	return filepath.Clean(parent)
}

// UnderDir reports whether path is dir or sits inside it.
//
// The tree, not the one level. Approving a directory and then being asked
// again about the file one level down inside it would be the same
// keystroke problem in a smaller box, and "this directory" is not how
// anybody reads a directory name.
//
// Both sides are resolved physically, for the reason OutsideWorkspace is:
// a grant is about where a path leads, not how it is spelled.
func UnderDir(dir, path string) bool {
	if dir == "" || path == "" {
		return false
	}
	base, berr := resolvePhysical(dir)
	target, terr := resolvePhysical(path)
	if berr != nil || terr != nil {
		return false
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// globSubject is the part of a glob pattern that is a path: everything
// before the first element containing a wildcard.
//
// "src/**/*.go" is a question about src, "/etc/*.conf" is a question
// about /etc, and a bare "*.go" is a question about the workspace itself.
// Without this, glob was the one file tool with no permission subject at
// all, which meant no rule could match it and the boundary could not see
// it: "/Users/someone/other-project/**/*.go" listed another project's
// files and nothing asked.
func globSubject(pattern string) string {
	if pattern == "" {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(pattern), "/")
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.ContainsAny(p, "*?[") {
			break
		}
		kept = append(kept, p)
	}
	if len(kept) == len(parts) {
		// No wildcard anywhere: the pattern is an ordinary path.
		return pattern
	}
	if len(kept) == 0 {
		return "."
	}
	return filepath.FromSlash(strings.Join(kept, "/"))
}
