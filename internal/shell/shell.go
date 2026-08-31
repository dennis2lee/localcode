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
	"fmt"
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
		// Lowercased before the suffix is trimmed, not after: TrimSuffix is
		// case-sensitive, so "PYTHON3.EXE" kept its extension and matched
		// nothing.
		name := strings.TrimSuffix(strings.ToLower(filepath.Base(fields[0])), ".exe")
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
		"not found, look for it by absolute path (see below). Prefer node or awk instead if this " +
		"task does not actually need Python."
}

// Interpreter names that are worth explaining when they are not found.
//
// Short on purpose. Widening this is what creates false positives, and
// the miss it accepts is named rather than hidden: "env python3 x.py",
// "xargs python3" and "PYTHONPATH=. python3 x.py" all put something else
// in the leading position and get nothing from this.
var namedInterpreters = map[string]bool{
	"python": true, "python3": true, "pip": true, "pip3": true,
}

// MissingInterpreter explains a Windows command that failed because its
// interpreter is not on PATH, or "" when that is not what happened.
//
// The complement of StoreStub, and deliberately the other side of the
// same test. StoreStub fires when LookPath SUCCEEDS and lands in the
// Store alias directory; this fires when LookPath FAILS. The two cannot
// both answer, and between them they cover the two ways "python" goes
// wrong on Windows.
//
// LookPath is the whole gate, and it is chosen over reading the shell's
// error text because that text is translated: cmd.exe says "is not
// recognized as an internal or external command" in the machine's own
// language, and MSYS bash follows the MSYS locale. The report this was
// built from came from a machine whose model was writing Korean.
//
// This runs only after a command has already failed. A pre-flight check
// that guessed wrong would stop a command that was going to work, and
// there is no reading of PATH that can be sure: a shim, an alias or a
// function inside the shell is invisible to LookPath.
func MissingInterpreter(command string, failed bool) string {
	return missingInterpreter(runtime.GOOS, command, failed, exec.LookPath, current().posix)
}

func missingInterpreter(goos, command string, failed bool, lookPath func(string) (string, error), posix bool) string {
	if goos != "windows" || !failed {
		return ""
	}
	name := ""
	for _, segment := range splitSegments(command) {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		lead := strings.TrimSuffix(strings.ToLower(filepath.Base(fields[0])), ".exe")
		if !namedInterpreters[lead] {
			continue
		}
		if _, err := lookPath(fields[0]); err != nil {
			name = fields[0]
			break
		}
	}
	if name == "" {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n\n(%s is not on PATH on this machine, which is a common way for this to fail on "+
		"Windows rather than a sign that Python is missing. ", name)

	// A machine fact, when there is one: what PATH actually resolves. It
	// is not a claim that this interpreter suits the project, and the
	// wording keeps that line, because naming a conda base that cannot
	// import the project turns one failed call into a wrong hypothesis.
	if other, err := lookPath("python"); err == nil && !strings.Contains(strings.ToLower(other), `\microsoft\windowsapps\`) {
		fmt.Fprintf(&b, "There is a python on PATH at %s; whether it is the one this project needs is a "+
			"separate question. ", forShell(other, posix))
	} else {
		b.WriteString("The interpreter file is normally called python.exe: a conda, miniforge or " +
			"miniconda install does not provide python3.exe at all, and neither do python.org's " +
			"classic installers, and none of those put themselves on PATH by default. ")
	}

	b.WriteString("Each tool call gets a fresh shell, so an activated environment does not carry " +
		"over: call the interpreter by its absolute path. To find one, run:\n\n    " +
		huntCommand(posix) + "\n\nIf that finds nothing, Python really is absent.)")
	return b.String()
}

// huntCommand is handed back for the model to run through the ordinary
// permission gate, the way stubMessage hands back the winget line, rather
// than localcode searching the disk itself. Searching would mean this
// package spawning processes and walking install roots on a path where a
// person has approved nothing, and it would still be answering "is there
// any Python" when the question is "which Python can import this project".
func huntCommand(posix bool) string {
	if !posix {
		return `dir /b /s "%UserProfile%\miniforge3\python.exe" "%UserProfile%\miniconda3\python.exe" ` +
			`"%UserProfile%\anaconda3\python.exe" "%LocalAppData%\Programs\Python\python.exe" 2>nul`
	}
	return `ls -1 ~/miniforge3/python.exe ~/miniconda3/python.exe ~/anaconda3/python.exe ` +
		`~/AppData/Local/Programs/Python/Python3*/python.exe ~/AppData/Local/Python/bin/python3.exe ` +
		`./.venv/Scripts/python.exe ./venv/Scripts/python.exe 2>/dev/null`
}

// forShell renders a Windows path in the form the shell that will run it
// accepts. A bare backslash path inside a bash -c string is eaten by bash
// before MSYS ever sees it, so printing one under Git Bash hands the model
// something that cannot be pasted back.
func forShell(path string, posix bool) string {
	if !posix {
		return path
	}
	p := strings.ReplaceAll(path, `\`, "/")
	if len(p) > 2 && p[1] == ':' {
		return "/" + strings.ToLower(p[:1]) + p[2:]
	}
	return p
}

// splitSegments breaks a command line at the operators that start a new
// command (|, &&, ||, ;, newline), so only the leading word of each
// command is considered. "grep python3 notes.txt" must not trip the
// detector.
//
// Quote-aware, which it was not, and the difference was a command that
// never ran. Splitting the raw text on ";" finds one inside a quoted
// string too, so
//
//	git commit -m "fix; python3 helper"
//
// produced a second segment beginning "python3", and on a machine where
// the WindowsApps alias is enabled that made the git commit a Python
// command: refused, unrun, and answered with instructions for installing
// an interpreter it was never going to use. Measured, not imagined.
//
// The rules are the shell's own. A single quote protects everything up to
// the next single quote. A double quote protects everything up to the
// next unescaped double quote. A backslash outside single quotes protects
// the next character. Nothing here has to interpret any of it, only
// decline to split inside it, so this is a scanner rather than a parser
// and an unbalanced quote simply means the rest of the line is one
// segment, which is the safe direction: a missed split can only fail to
// notice a python, and a wrong split refuses somebody's commit.
func splitSegments(command string) []string {
	var out []string
	var cur strings.Builder
	var quote byte // 0, '\'' or '"'

	flush := func() {
		out = append(out, cur.String())
		cur.Reset()
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
			cur.WriteByte(c)
			continue
		case quote == '"':
			if c == '\\' && i+1 < len(command) {
				cur.WriteByte(c)
				i++
				cur.WriteByte(command[i])
				continue
			}
			if c == '"' {
				quote = 0
			}
			cur.WriteByte(c)
			continue
		}

		switch c {
		case '\'', '"':
			quote = c
			cur.WriteByte(c)
		case '\\':
			cur.WriteByte(c)
			if i+1 < len(command) {
				i++
				cur.WriteByte(command[i])
			}
		case ';', '\n':
			flush()
		case '&', '|':
			// "&&" and "||" and a bare "|" all start a new command. A
			// bare "&" does too under cmd.exe, and under a POSIX shell it
			// backgrounds what came before, which also ends the segment.
			if i+1 < len(command) && command[i+1] == c {
				i++
			}
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}
