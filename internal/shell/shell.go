// Package shell resolves which shell runs command strings on this OS,
// shared by the bash tool, hooks, and custom-command expansion.
//
// Everything used to hardcode `sh -c`, which fails on the Windows build
// the moment the model calls a tool: unless Git Bash happens to be on
// PATH there is no `sh`, so every shell execution dies with
// `exec: "sh": executable file not found in %PATH%` and the agent can't
// run so much as `git pull`. Resolution order on Windows:
//
//  1. `sh` on PATH — Git for Windows puts sh.exe there when installed
//     with the "use Git from the command line" option. Best case: the
//     model's bash-flavored commands run unmodified. (WSL installs only
//     bash.exe, never sh.exe, so this lookup can't accidentally select
//     WSL and run commands inside a different filesystem.)
//  2. bash.exe at Git for Windows' well-known install paths — covers a
//     Git installed without putting its shell tools on PATH.
//  3. cmd /c — always present. Simple commands (`git pull`, `go test`)
//     work; bash-isms don't, which is why Notice() tells the model what
//     it's actually talking to.
//
// Non-Windows resolves to `sh -c` exactly as before.
package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"localcode/internal/childproc"
)

// resolved is the chosen shell: the executable plus the flag(s) that make
// it run one command string ("-c" for sh/bash, "/c" for cmd).
type resolved struct {
	path string
	args []string
	// posix reports whether the shell speaks POSIX syntax — false only
	// for the cmd.exe fallback, where the model needs to be told to drop
	// bash-isms.
	posix bool
}

var (
	once   sync.Once
	active resolved
)

func current() resolved {
	once.Do(func() {
		active = resolve(runtime.GOOS, exec.LookPath, os.Getenv, fileExists)
	})
	return active
}

// resolve picks the shell for goos. Its collaborators are parameters so
// the Windows paths are testable from any OS.
func resolve(goos string, lookPath func(string) (string, error), getenv func(string) string, exists func(string) bool) resolved {
	if goos != "windows" {
		return resolved{path: "sh", args: []string{"-c"}, posix: true}
	}

	if p, err := lookPath("sh"); err == nil {
		return resolved{path: p, args: []string{"-c"}, posix: true}
	}

	// Git for Windows, installed without exposing its tools on PATH.
	// Both the system-wide and the per-user installer locations.
	for _, root := range []string{getenv("ProgramFiles"), getenv("ProgramFiles(x86)"), filepath.Join(getenv("LocalAppData"), "Programs")} {
		if root == "" {
			continue
		}
		p := filepath.Join(root, "Git", "bin", "bash.exe")
		if exists(p) {
			return resolved{path: p, args: []string{"-c"}, posix: true}
		}
	}

	comspec := getenv("ComSpec")
	if comspec == "" {
		comspec = "cmd"
	}
	return resolved{path: comspec, args: []string{"/c"}, posix: false}
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// killGrace is how long Wait may go on waiting after the command has been
// killed, before the pipes are closed out from under whatever still holds
// them and Wait is made to return.
//
// It is a grace period, not a timeout on the command: it starts only once
// the context is cancelled and the tree has been killed. A second is
// enough for a process that is dying to finish dying, and short enough
// that Esc feels like it did something.
const killGrace = time.Second

// Command builds an exec.Cmd that runs script under the resolved shell,
// arranged so that cancelling ctx actually ends the command.
//
// Two things are needed for that, and neither is the default.
//
// The child gets a process group of its own and the whole group is killed,
// because killing the shell alone leaves its children running. That is not
// an edge case: `npm run dev &`, a test runner, anything that forks. Those
// children inherit the pipe CombinedOutput reads, and the read does not end
// until the last holder of the pipe closes it — so the tool call went on
// blocking after the kill, for as long as the orphan lived. Measured
// before the fix: cancelling `sh -c "sleep 30 & sleep 30"` returned after
// 30.0s, with the shell itself reported as killed half a second in.
//
// WaitDelay is the backstop for whatever the kill does not reach — a
// process ignoring SIGKILL is impossible, but a grandchild in a different
// session, or a Windows tree taskkill declined, is not. It bounds the wait
// rather than trusting it.
func Command(ctx context.Context, script string) *exec.Cmd {
	sh := current()
	cmd := exec.CommandContext(ctx, sh.path, append(append([]string{}, sh.args...), script)...)
	// Without this, every bash tool call from the Windows desktop build
	// pops up its own console window. See internal/childproc.
	childproc.Hide(cmd)
	childproc.NewGroup(cmd)
	cmd.Cancel = func() error { return childproc.KillGroup(cmd) }
	cmd.WaitDelay = killGrace
	return cmd
}

// Notice returns a one-line caveat for surfaces the model reads (the bash
// tool's description), non-empty only when the fallback shell is not
// POSIX. Without it the model keeps emitting bash syntax at cmd.exe and
// can't tell why pipelines and `export` misbehave.
func Notice() string {
	if current().posix {
		return ""
	}
	return " Commands run under cmd.exe (no POSIX sh was found on this Windows system); use cmd syntax, not bash."
}
