package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// A write into a git directory is asked, whatever allowed the write.
//
// git reads its configuration and hooks from the repository it runs in,
// and runs what they name: core.fsmonitor on every "git status", an alias
// that starts with "!", an executable hook on every commit. The built-in
// bash rule lets git itself run without a question. So a model allowed to
// write files (a "write_file" allow rule, an "always" answer) could put a
// command into .git/config and have the next ordinary "git status" run
// it, and nothing would have asked at either step. lineRunsGitCommand in
// internal/config closes that when it is asked for on the command line;
// this closes it when it is asked for by writing the file.
//
// skip_tools and skip_all still allow it. With either on, bash runs any
// command without asking, so a question here would stop nothing.

// IntoGitDir reports whether a write to path lands in a git directory: a
// directory named .git, anywhere on the path, or the .git file itself
// (which, in a worktree or a submodule, names the directory git uses). Or
// in git's per-user configuration (see gitUserConfig), which it reads in
// every repository, so core.fsmonitor written there runs on the next "git
// status" anywhere.
//
// The path is judged twice and either is enough. As the tools will use
// it, which is cleaned (see resolve), so ".git/../.git/config" and
// "./.git/config" are what they say. And physically, with symlinks
// followed the way resolvePhysical follows them for the workspace
// boundary, so a link to .git is where it leads.
//
// A path that does not resolve is judged by its text alone: a symlink
// loop, a directory that cannot be read, and on Windows a junction, which
// Go's EvalSymlinks does not follow. Counting those as .git made every
// write in a workspace reached through a junction a question nothing
// could answer for longer than one write. The workspace boundary already
// asks about them, as outside.
func IntoGitDir(ctx context.Context, path string) bool {
	if path == "" {
		return false
	}
	full := resolve(ctx, path)
	if hasGitDirComponent(full) || gitUserConfig(full) {
		return true
	}
	physical, err := resolvePhysical(full)
	return err == nil && (hasGitDirComponent(physical) || gitUserConfig(physical))
}

// gitUserConfig reports whether path is where git reads its per-user
// configuration: ~/.gitconfig, the file $GIT_CONFIG_GLOBAL names, or
// anything in $XDG_CONFIG_HOME/git (~/.config/git when unset), which also
// holds the user-wide attributes file a diff or filter driver is named in.
//
// Compared both as spelled and with the home directory's own links
// resolved, and without case, which is how macOS and Windows compare
// names. On Linux that costs a question about a file git does not read.
func gitUserConfig(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	type place struct {
		path string
		dir  bool
	}
	places := []place{{filepath.Join(home, ".gitconfig"), false}, {filepath.Join(xdg, "git"), true}}
	if g := os.Getenv("GIT_CONFIG_GLOBAL"); g != "" {
		places = append(places, place{g, false})
	}
	target := strings.ToLower(path)
	for _, pl := range places {
		spellings := []string{pl.path}
		if r, err := resolvePhysical(pl.path); err == nil {
			spellings = append(spellings, r)
		}
		for _, sp := range spellings {
			sp = strings.ToLower(filepath.Clean(sp))
			if target == sp || pl.dir && strings.HasPrefix(target, sp+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

func hasGitDirComponent(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if isGitDirName(part) {
			return true
		}
	}
	return false
}

// isGitDirName reports whether a path component names .git on some file
// system localcode runs on. git refuses the same spellings in a tree it
// checks out, for the same reason: each is .git to the file system.
//
//   - Case: ".GIT" is .git on macOS and Windows.
//   - HFS+ ignores some Unicode code points in a name, so ".g\u200cit"
//     is .git there.
//   - Windows drops trailing dots and spaces (".git. "), reads what
//     follows a colon as a stream of the file (".git::$INDEX_ALLOCATION"),
//     and gives .git the short name GIT~1.
//
// On a file system where one of these is a different name, it costs one
// question about a file that is not git's.
func isGitDirName(part string) bool {
	part = strings.Map(func(r rune) rune {
		if ignoredByHFS(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, part)
	if i := strings.IndexByte(part, ':'); i >= 0 {
		part = part[:i]
	}
	part = strings.TrimRight(part, ". ")
	return part == ".git" || gitShortName.MatchString(part)
}

var gitShortName = regexp.MustCompile(`^git~[0-9]+$`)

// ignoredByHFS is the set of code points HFS+ leaves out when it compares
// names, as git's is_hfs_dotgit lists them.
func ignoredByHFS(r rune) bool {
	switch {
	case r >= 0x200c && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x206a && r <= 0x206f:
		return true
	}
	return r == 0xfeff
}
