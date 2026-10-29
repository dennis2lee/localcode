package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
// resolved, and component by component as fileSystemName spells them,
// which is how macOS and Windows compare names. On Linux that costs a
// question about a file git does not read. Not compared: Unicode
// normalization, which APFS also ignores; a home path with a decomposed
// accented letter written the other way is not recognised.
func gitUserConfig(path string) bool {
	// git takes $HOME first, on Windows too, where Go's UserHomeDir is
	// %USERPROFILE%; the two can differ there. With no HOME, git for
	// Windows takes %HOMEDRIVE%%HOMEPATH% before %USERPROFILE%.
	homes := []string{os.Getenv("HOME")}
	if drive, p := os.Getenv("HOMEDRIVE"), os.Getenv("HOMEPATH"); drive != "" && p != "" {
		homes = append(homes, drive+p)
	}
	if h, err := os.UserHomeDir(); err == nil {
		homes = append(homes, h)
	}
	type place struct {
		path string
		dir  bool
	}
	var places []place
	for _, home := range homes {
		if home == "" {
			continue
		}
		places = append(places, place{filepath.Join(home, ".gitconfig"), false})
		if os.Getenv("XDG_CONFIG_HOME") == "" {
			places = append(places, place{filepath.Join(home, ".config", "git"), true})
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		places = append(places, place{filepath.Join(xdg, "git"), true})
	}
	if g := os.Getenv("GIT_CONFIG_GLOBAL"); g != "" {
		places = append(places, place{g, false})
	}
	target := fileSystemPath(path)
	for _, pl := range places {
		spellings := []string{pl.path}
		if r, err := resolvePhysical(pl.path); err == nil {
			spellings = append(spellings, r)
		}
		for _, sp := range spellings {
			want := fileSystemPath(sp)
			if len(target) < len(want) || !pl.dir && len(target) != len(want) {
				continue
			}
			if slices.Equal(target[:len(want)], want) {
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
	part = fileSystemName(part)
	return part == ".git" || gitShortName.MatchString(part)
}

// fileSystemName is a path component as the file systems above compare
// it: no case, no code points HFS+ ignores, nothing from a colon on, and
// no trailing dots or spaces.
func fileSystemName(part string) string {
	part = strings.Map(func(r rune) rune {
		if ignoredByHFS(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, part)
	if i := strings.IndexByte(part, ':'); i >= 0 && i > len(filepath.VolumeName(part)) {
		part = part[:i]
	}
	return strings.TrimRight(part, ". ")
}

// fileSystemPath is a path split into its components, each spelled by
// fileSystemName, so ".gitconfig." and ".gitconfig::$DATA" are
// .gitconfig as Windows reads them.
func fileSystemPath(path string) []string {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for i, p := range parts {
		parts[i] = fileSystemName(p)
	}
	return parts
}

// resolves reports whether a path's physical location can be worked out.
// One that cannot (a link whose expansion is longer than the system's
// path limit, a junction on Windows) may still be followed by the kernel,
// so where a write through it lands is unknown.
func resolves(ctx context.Context, path string) bool {
	if path == "" {
		return true
	}
	_, err := resolvePhysical(resolve(ctx, path))
	return err == nil
}

// gitWriteReason is what a .git question adds to the tool's description:
// where the write lands, which the typed path need not show.
func gitWriteReason(ctx context.Context, path string) string {
	full := resolve(ctx, path)
	if physical, err := resolvePhysical(full); err == nil {
		full = physical
	}
	return "This writes " + full + ", which git reads its configuration or hooks from. git runs what they name."
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
