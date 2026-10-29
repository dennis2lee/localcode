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
// An absolute path keeps its place: the model asked for a specific one
// and gets it. A relative path is joined onto the session's directory,
// which is the whole point — "src/main.go" has to mean the src/main.go of
// the workspace this turn belongs to, not of whichever directory the
// daemon was started in.
//
// Both come back cleaned, so ".." is taken off the text before the file
// system sees it. The permission checks (OutsideWorkspace, IntoGitDir)
// judge the cleaned path, and the file system reads "link/.." as the
// directory above wherever link points: an absolute "<ws>/h/../config"
// with h a link to .git/hooks was judged as <ws>/config and written to
// .git/config. A relative path joined onto the directory was always
// cleaned, by Join.
//
// An empty path stays empty rather than becoming the directory itself, so
// a tool that treats "" as "unset" keeps doing so.
func resolve(ctx context.Context, path string) string {
	if path == "" {
		return path
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	dir := WorkingDir(ctx)
	if dir == "" {
		return filepath.Clean(path)
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
	// Only followed links are bounded, and the bound is the kernel's
	// reason: a chain of dangling links that never converges is a loop
	// in everything but name, and a loop is unresolvable, which the
	// caller treats as outside.
	//
	// Peeling an ordinary missing component is not a hop and is not
	// counted. The two operations shared a counter once, and the result
	// was that a path more than 64 components deep exhausted a *link*
	// budget without following a single link: an ordinary new directory
	// tree, entirely inside the workspace, classified as outside and
	// escalated to a question. Peeling terminates on its own at the
	// filesystem root, so it needs no bound of its own.
	links := 0
	for {
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
			//
			// Not cleaned: a target is read the way the file system reads
			// it, and that is not by its text. "h/../info/attributes"
			// with h a link to .git/hooks is .git/info/attributes, and
			// EvalSymlinks, on the next pass, follows h before it applies
			// the "..". Cleaning first made it <ws>/info/attributes.
			if links++; links > maxDanglingLinkHops {
				return "", errors.New("too many links")
			}
			dest, rerr := os.Readlink(p)
			if rerr != nil {
				return "", rerr
			}
			if !filepath.IsAbs(dest) {
				parent, _ := splitLastElement(p)
				dest = strings.TrimSuffix(parent, string(filepath.Separator)) + string(filepath.Separator) + dest
			}
			p = dest
			continue
		}
		// Peeled by its text for the same reason: filepath.Dir would
		// clean what is left. A missing ".." or "." is not a name to
		// peel; the path does not resolve.
		parent, base := splitLastElement(p)
		if parent == p || base == "" || base == "." || base == ".." {
			return "", err
		}
		tail = filepath.Join(base, tail)
		p = parent
	}
}

// splitLastElement is filepath.Split without the cleaning filepath.Dir
// does: the parent is the text before the last separator, the root kept
// whole, and "." for a relative path of one element. Trailing separators
// are not an element: a link to "newdir/" names newdir.
func splitLastElement(p string) (parent, base string) {
	vol := len(filepath.VolumeName(p))
	for len(p) > vol+1 && os.IsPathSeparator(p[len(p)-1]) {
		p = p[:len(p)-1]
	}
	i := len(p) - 1
	for i >= vol && !os.IsPathSeparator(p[i]) {
		i--
	}
	if i < vol {
		return ".", p[vol:]
	}
	parent = p[:i]
	if len(parent) == vol {
		parent = p[:i+1]
	}
	return parent, p[i+1:]
}

// maxDanglingLinkHops bounds how many dangling symlinks resolution will
// follow before calling the chain a loop. The same order as the kernel's
// own ELOOP limit, and for the same reason: past this depth, a chain that
// has not converged is not going to.
const maxDanglingLinkHops = 64

// Policy is the four switches, answered by whoever knows about sessions
// and configuration. This package knows about paths and nothing else.
//
// All three take a context because all four switches are per session: two
// conversations on one daemon are two projects, and "do not ask me about
// this one" is a sentence about a project.
type Policy struct {
	// SkipAll allows every question, the boundary included. The blanket.
	SkipAll func(ctx context.Context) bool
	// SkipTools allows every question the boundary did not raise. The
	// useful middle: work without being interrupted about this project,
	// and still be asked before anything leaves it.
	SkipTools func(ctx context.Context) bool
	// OutsideAllowed answers whether this session has already said yes to
	// leaving the workspace for reads, or for writes.
	OutsideAllowed func(ctx context.Context, class OutsideClass) bool
}

// Query is one permission question as the registry asks it.
type Query struct {
	Tool    string
	Subject string
	// Static is the tool's own default (Tool.RequiresPermission).
	Static bool
	// Class is what this tool does to Subject, from the tool itself.
	Class OutsideClass
}

// Outcome is the decision and, when the workspace boundary is the thing
// asking, enough for the question to explain itself.
//
// The boundary reports back rather than being recomputed by whoever draws
// the prompt: deciding it costs symlink resolution, and two independent
// answers to "is this outside?" is one of them being wrong eventually.
type Outcome struct {
	Decision Decision
	// Outside is set only when this decision is the boundary speaking.
	Outside OutsideClass
	// Dir is the directory an "allow this directory" answer covers, and
	// Workspace is the project the subject is outside of. Both empty
	// unless Outside is set.
	Dir       string
	Workspace string
	// GitDir is set when the question is a write into a git directory.
	// It is asked every time (Ask.Once): a remembered answer is keyed by
	// how a path is spelled, and a file approved for the session can be
	// replaced by a link into .git, which git's own allowed commands can
	// check out. An answer covers the one write.
	GitDir bool
	// Reason is what the question says on top of the tool's own
	// description, for a question the call alone would not explain: the
	// path the model typed can be a link, and nothing in it says .git.
	Reason string
}

// ComposeResolver is the whole permission pipeline in its required order.
//
//  1. The user's rules and the shipped guards. A deny here is final:
//     nothing below softens a rule somebody wrote to forbid something.
//  2. The workspace boundary. A path that leaves the project is a
//     question of its own, with its own switch per direction, and it is
//     asked whatever the rules said — a rule that allows write_file
//     everywhere is a statement about this project, not a licence to
//     edit the one next door.
//  3. A write into a git directory, which is asked whatever the rules
//     said; see IntoGitDir. After the boundary, which asks its own
//     question for a .git outside the project.
//  4. skip_tools over whatever ask is left, then skip_all over
//     everything.
//
// The order is the contract and it has been wrong twice. The boundary
// used to run after the skip downgrade, so a person who had said "ask me
// nothing" was asked anyway. Then it ran before, and skip_permissions
// silenced the one guard worth keeping, which is what skip_tools now
// exists to separate: skip_tools stops at the edge of the project, and
// only skip_all crosses it.
func ComposeResolver(
	resolve func(ctx context.Context, toolName, subject string, staticRequiresPermission bool) Decision,
	p Policy,
) func(ctx context.Context, q Query) Outcome {
	return func(ctx context.Context, q Query) Outcome {
		d := resolve(ctx, q.Tool, q.Subject, q.Static)
		if d == DecisionDeny {
			return Outcome{Decision: d}
		}
		gitWrite := q.Class == OutsideWrite && IntoGitDir(ctx, q.Subject)

		if q.Class != OutsideNone && OutsideWorkspace(ctx, q.Subject) &&
			!p.OutsideAllowed(ctx, q.Class) {
			if p.SkipAll(ctx) {
				return Outcome{Decision: DecisionAllow}
			}
			if gitWrite {
				// Asked as a plain question, not as a boundary one. A
				// boundary question is answered for a directory, and
				// "allow writes in other-project", given for its
				// main.go, would cover other-project/.git/config too;
				// asked here, it would cover the whole .git. A plain
				// answer covers the one write.
				return Outcome{Decision: DecisionAsk, GitDir: true, Reason: gitWriteReason(ctx, q.Subject)}
			}
			return Outcome{
				Decision:  DecisionAsk,
				Outside:   q.Class,
				Dir:       OutsideDir(ctx, q.Subject),
				Workspace: WorkingDir(ctx),
			}
		}

		// A write the boundary let through, because it is inside the
		// project or because writing outside is allowed, whose path does
		// not resolve. The kernel may still follow it: a link whose
		// expansion is longer than the path limit, which git's own allowed
		// commands can check out, leads into .git with nothing in its text
		// to say so. Inside the project such a path is outside to the
		// boundary, so this is the write-outside case.
		reason := ""
		if q.Class == OutsideWrite && !gitWrite && !resolves(ctx, q.Subject) {
			gitWrite = true
			reason = "Where this write lands cannot be worked out: its path does not resolve, and it could lead into .git."
		} else if gitWrite {
			reason = gitWriteReason(ctx, q.Subject)
		}
		if gitWrite {
			d = DecisionAsk
		}

		if d == DecisionAsk && (p.SkipAll(ctx) || p.SkipTools(ctx)) {
			return Outcome{Decision: DecisionAllow}
		}
		return Outcome{Decision: d, GitDir: gitWrite, Reason: reason}
	}
}
