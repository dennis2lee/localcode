package tools

import (
	"context"
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
