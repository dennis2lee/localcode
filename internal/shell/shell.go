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
	"strings"
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

// Windows ships "python.exe" and "python3.exe" as Microsoft Store
// app-execution-alias stubs: running one does not run Python, it opens
// the Store to offer an install. On a machine where policy blocks the
// Store that is the worst possible failure shape — the command exits
// with no output and no explanation, a Store window the user may not
// even see is left asking for an install that cannot happen, and the
// model, told nothing, tries python again three different ways before
// giving up. Seen in the field exactly like that: four failed python3
// calls and a popup security had silently swallowed.
//
// So the stub is detected before anything is launched, and the answer is
// an error that tells the model the truth: Python is not installed here,
// and what to do instead.

// StoreStub reports whether command would launch a Store alias stub
// rather than a real program, with the message to return in place of
// running it. Always false off Windows.
func StoreStub(command string) (string, bool) {
	return storeStub(runtime.GOOS, command, exec.LookPath)
}

func storeStub(goos, command string, lookPath func(string) (string, error)) (string, bool) {
	if goos != "windows" {
		return "", false
	}
	for _, segment := range splitSegments(command) {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(strings.TrimSuffix(filepath.Base(fields[0]), ".exe"))
		if name != "python" && name != "python3" {
			continue
		}
		path, err := lookPath(fields[0])
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(path), `\microsoft\windowsapps\`) {
			return stubMessage(fields[0], lookPath), true
		}
	}
	return "", false
}

// pythonWingetID is the winget package to install. Versioned because
// winget publishes Python per minor version: there is no floating
// "latest 3.x" id, so this is one line to bump per release rather than
// something that can be derived at runtime.
const pythonWingetID = "Python.Python.3.14"

// stubMessage is what a python command gets instead of running.
//
// It is an instruction rather than an apology. The failure it replaces
// taught the model nothing, so it retried python in three more
// spellings; this tells it exactly what to do, in the order that works:
// install once, then use it.
//
// The install goes through the ordinary bash tool, which means it goes
// through the ordinary permission gate. That is deliberate. localcode
// could shell out to winget itself the moment it saw the stub, and it
// would be installing software on someone's machine without the
// confirmation every other command needs. Handing the command back
// keeps one permission model instead of two.
func stubMessage(invoked string, lookPath func(string) (string, error)) string {
	msg := invoked + " is not installed: it resolves to a Microsoft Store app-execution-alias " +
		"stub, which opens the Store rather than running Python. Do not retry python in another " +
		"spelling; every spelling hits the same stub."
	if _, err := lookPath("winget"); err != nil {
		return msg + " winget is not available here either, so this cannot be fixed from a command: " +
			"install Python through whatever software channel this machine uses, or disable the " +
			"aliases under Settings > Apps > Advanced app settings > App execution aliases. For " +
			"now, use node, awk, or another available tool."
	}
	return msg + " Install it with:\n\n    winget install --id " + pythonWingetID +
		" -e --source winget --scope user --accept-package-agreements --accept-source-agreements\n\n" +
		"If winget reports that id is not available, run `winget search Python.Python.3` and install " +
		"the newest one listed. The install does not reach a shell that is already running, so call " +
		"python in a later command rather than chaining it onto the install with &&; if it is still " +
		"not found, `py -3` uses the launcher the installer adds. Prefer node or awk instead if this " +
		"task does not actually need Python."
}

// splitSegments breaks a command line at the operators that start a new
// command (|, &&, ||, ;), so only the leading word of each command is
// considered. "grep python3 notes.txt" must not trip the detector.
func splitSegments(command string) []string {
	replaced := command
	for _, op := range []string{"&&", "||", "|", ";", "\n"} {
		replaced = strings.ReplaceAll(replaced, op, "\x00")
	}
	return strings.Split(replaced, "\x00")
}
