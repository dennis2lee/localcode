package tools

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// This file is what replaced os.Chdir.
//
// The workspace used to be the process's own working directory, which made
// it exactly one thing for the whole daemon. Every consequence followed
// from that: a turn running in any session had to block a workspace change
// (a tool call mid-execution would otherwise find the ground moved under
// it), including a turn nobody was watching and a turn parked forever on
// an unanswered permission request. Two clients on one daemon shared a
// workspace, so one of them selecting a session moved the other's.
//
// A working directory carried on the context instead is per-turn, so none
// of that applies: two sessions in different directories are just two
// contexts, and changing one cannot disturb the other.
//
// The process's own cwd is left alone deliberately. Nothing sets it now,
// so it stays wherever localcode was started, and it remains the fallback
// for any path resolved without a directory on the context.

type workDirKey struct{}

// WithWorkingDir returns ctx carrying dir as the directory relative paths
// resolve against. An empty dir is a no-op, so a caller with nothing to
// say does not have to special-case it.
func WithWorkingDir(ctx context.Context, dir string) context.Context {
	if dir == "" {
		return ctx
	}
	return context.WithValue(ctx, workDirKey{}, dir)
}

// WorkingDir reports the directory on ctx, or "" if there is none — in
// which case the process's own working directory applies, exactly as it
// did before any of this existed.
func WorkingDir(ctx context.Context) string {
	dir, _ := ctx.Value(workDirKey{}).(string)
	return dir
}

// resolve turns a tool's path argument into one that means the same thing
// no matter what the process's working directory happens to be.
//
// Absolute paths are returned untouched: the model asked for a specific
// place and gets it. A relative path is joined onto the session's
// directory, which is the whole point — "src/main.go" has to mean the
// src/main.go of the workspace this turn belongs to, not of whichever
// directory the daemon was started in.
//
// An empty path stays empty rather than becoming the directory itself, so
// a tool that treats "" as "unset" keeps doing so.
func resolve(ctx context.Context, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	dir := WorkingDir(ctx)
	if dir == "" {
		return path
	}
	return filepath.Join(dir, path)
}

// relativeTo trims base off each path, so a search run inside a session's
// directory answers in the same terms the question was asked in.
//
// Reporting absolute paths would work — every tool resolves them — but it
// puts the whole of C:\work\localcode-main in front of every line of a
// grep, and it leaks where the daemon happens to be running into output
// the model then quotes back. A path outside base is left absolute,
// because there is no shorter way to say where it is.
func relativeTo(base string, paths []string) []string {
	if base == "" {
		return paths
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		rel, err := filepath.Rel(base, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			out[i] = p
			continue
		}
		out[i] = rel
	}
	return out
}

// OutsideWorkspace reports whether path lands outside the directory this
// turn belongs to.
//
// The boundary a coding agent is expected to respect. Every relative path
// is inside it by construction (resolve joins it on), so what this is
// really asking about is an absolute path, one that climbs out with
// "..", or a symlink that points out: /etc/passwd, ~/.aws, the other
// project in the next directory along, a link in the repo aimed at a
// dotfile. Those are not always wrong — reading a system header or a
// config somewhere else is ordinary work — which is why the answer this
// feeds is "ask", not "refuse".
//
// The comparison is between physical paths: both the workspace and the
// path have their symlinks resolved first, so a link inside the
// workspace pointing out of it is outside, which is where it actually
// leads. A path that cannot be resolved at all (a permission failure, a
// link loop) is treated as outside for the same reason unresolvable is
// not the same as safe — and because the consequence is a question, not
// a refusal, the cost of being wrong is one prompt.
//
// False when the context carries no directory, because then there is no
// boundary to be outside of, and false for an empty path, which is a tool
// with nothing to say rather than a path at the filesystem root.
func OutsideWorkspace(ctx context.Context, path string) bool {
	dir := WorkingDir(ctx)
	if dir == "" || path == "" {
		return false
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(dir, full)
	}
	base, berr := resolvePhysical(dir)
	target, terr := resolvePhysical(full)
	if berr != nil || terr != nil {
		return true
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		// Different volumes on Windows, which is as outside as it gets.
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolvePhysical is EvalSymlinks for paths that do not fully exist yet,
// which a boundary check meets constantly: the file about to be written
// is the common subject. The longest existing ancestor is resolved and
// the not-yet-existing tail rejoined, so "link-to-elsewhere/new.go" is
// judged by where the link leads, not by where it sits.
//
// A component that EvalSymlinks reports as missing is not always missing:
// a symlink whose target does not exist yet reports ErrNotExist too, and
// stripping it as an ordinary absent tail would judge the path by where
// the link sits instead of where it points — which is exactly the case a
// write boundary has to get right, because writing through that link
// creates the file at the target. So a missing component is Lstat'ed
// first, and if it is itself a symlink, resolution follows the link
// rather than discarding it.
func resolvePhysical(path string) (string, error) {
	p := filepath.Clean(path)
	tail := ""
	// Bounded like the kernel bounds it: a chain of dangling links that
	// never converges is a loop in everything but name, and a loop is
	// unresolvable, which the caller treats as outside.
	for hops := 0; hops < 64; hops++ {
		resolved, err := filepath.EvalSymlinks(p)
		if err == nil {
			return filepath.Join(resolved, tail), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			// A loop, a permission failure: not resolvable, and the
			// caller treats that as outside rather than as fine.
			return "", err
		}
		if fi, lerr := os.Lstat(p); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
			// The component exists and is a symlink — its *target* is
			// what's missing. Follow it; the target is where a write
			// through this path would land.
			dest, rerr := os.Readlink(p)
			if rerr != nil {
				return "", rerr
			}
			if !filepath.IsAbs(dest) {
				dest = filepath.Join(filepath.Dir(p), dest)
			}
			p = filepath.Clean(dest)
			continue
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		tail = filepath.Join(filepath.Base(p), tail)
		p = parent
	}
	return "", errors.New("too many links")
}

// BoundaryDecision applies the workspace boundary to a resolved
// permission decision.
//
// It escalates allow to ask and does nothing else. A deny stays a deny —
// a rule written to forbid something is not softened by where the file is
// — and an ask is already asking. enforce is the caller's switch, so the
// boundary can be part of a feature that is opted into rather than
// something that starts happening to everyone.
func BoundaryDecision(ctx context.Context, d Decision, subject string, enforce bool) Decision {
	if !enforce || d != DecisionAllow {
		return d
	}
	if OutsideWorkspace(ctx, subject) {
		return DecisionAsk
	}
	return d
}
