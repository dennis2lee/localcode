package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"localcode/internal/shell"
	"time"
)

type Bash struct {
	// Timeout bounds how long a command may run; zero means 2 minutes.
	Timeout time.Duration
}

func (Bash) Name() string { return "bash" }

// Description carries shell.Notice so that on a Windows machine with no
// POSIX sh the model is told it is talking to cmd.exe and writes cmd
// syntax instead of bash-isms.
func (Bash) Description() string {
	return "Run a shell command and return its combined stdout/stderr." + shell.Notice()
}
func (Bash) InputSchema() json.RawMessage {
	return schema(`{"command":{"type":"string"}}`, "command")
}
func (Bash) RequiresPermission(json.RawMessage) bool { return true }

// Subject exposes the shell command itself as the permission-rule
// pattern subject, so config can e.g. allow "git *" while asking for
// everything else.
func (Bash) Subject(input json.RawMessage) string {
	var args struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(input, &args)
	return args.Command
}

func (b Bash) Execute(ctx context.Context, input json.RawMessage) Result {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}

	timeout := b.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// A command that would only launch a Microsoft Store install stub is
	// answered instead of run: the stub's failure shape (no output, a
	// popup, possibly a policy block) teaches the model nothing, and it
	// retries python in every spelling before trying anything else.
	if msg, stub := shell.StoreStub(args.Command); stub {
		return Result{Content: msg, IsError: true}
	}

	cmd := shell.Command(ctx, args.Command)
	// The session's directory, not the daemon's. Leaving this unset is
	// what made the workspace process-wide: every shell command ran
	// wherever localcode itself was started, so two sessions in different
	// projects could not both be right, and moving one had to move both.
	// Empty means "inherit the process's own", which is the old behaviour
	// and the right fallback when nothing has said otherwise.
	cmd.Dir = WorkingDir(ctx)
	out, err := combinedOutputCapped(cmd)
	if err == nil {
		return Result{Content: withNotice(string(out), "")}
	}

	status, exited := exitStatus(err)
	if !exited {
		// Not a status the command chose: it could not be started at all,
		// or it was killed before it could exit.
		//
		// MissingInterpreter comes after the failure, never before it. On
		// Windows, "python3: not found" carries a false implication that
		// Python is absent, and the model's next move is to hunt for an
		// interpreter one guess at a time. It adds what localcode can
		// actually observe, and nothing it cannot: it never names an
		// interpreter as the project's. See internal/shell.
		return Result{
			Content: withNotice(string(out), killNotice(ctx, timeout, err)) +
				shell.MissingInterpreter(args.Command, true),
			IsError: true,
		}
	}

	// The command ran and answered with its status. Calling that an error
	// is what sent a model back to re-run a search it had already
	// completed — see shell.ExitAnswer for the report this comes from.
	if answer := shell.ExitAnswer(args.Command, status); answer != "" {
		return Result{Content: withNotice(string(out),
			fmt.Sprintf("(exited with status %d: %s)", status, answer))}
	}

	// "exited with status", not "exit error". That a command exited
	// non-zero is a fact; that it went wrong is an interpretation, and
	// outside the table above localcode has no grounds for it. The number
	// is what the model needs either way.
	return Result{
		Content: withNotice(string(out), fmt.Sprintf("(exited with status %d)", status)) +
			shell.MissingInterpreter(args.Command, true),
		IsError: true,
	}
}

// killNotice explains a command that never got to choose an exit status.
//
// A command that ran past its timeout and a command the person cancelled
// both arrive here as "signal: killed", and the two call for opposite
// things: one that ran out of time wants narrowing and trying again, one
// the person interrupted wants leaving alone. Told the same sentence for
// both, a model retries both — which is the whole shape of the defect
// this file's exit handling was rewritten for.
//
// localcode does not have to guess between them. It set the deadline and
// it holds the context, so it knows which of the two happened, and says
// which. Anything else — a command killed by the OOM killer, a shell
// that could not start — leaves the context clean and keeps the raw
// error, because then the error really is all that is known.
func killNotice(ctx context.Context, timeout time.Duration, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Sprintf("(no exit status: killed after the %s timeout)", timeout)
	case errors.Is(ctx.Err(), context.Canceled):
		return "(no exit status: cancelled)"
	}
	return fmt.Sprintf("(exit error: %v)", err)
}

// exitStatus is the status a command chose to exit with, and whether it
// chose one at all. A process killed by a signal reports -1, which is not
// a status and must not be read as one.
// bashOutputCap is the most of a command's output this keeps: half from
// the start, half from the end.
//
// CombinedOutput accumulates everything a command prints, with no ceiling
// at all, and the ceiling that does exist runs later — capToolResult, on
// a string already fully in memory, after several intermediate copies of
// it. One `find /` or one build with a warning per line was a daemon
// holding hundreds of megabytes for a result the model would never see
// more than about eighty kilobytes of.
//
// Deliberately far above that budget rather than equal to it. The
// question here is only whether memory is bounded; what the model sees is
// still capToolResult's decision, and matching the two would make this
// file the one that quietly changed it.
const bashOutputCap = 4 << 20

// combinedOutputCapped runs cmd and returns its combined output, keeping
// at most bashOutputCap bytes of it.
func combinedOutputCapped(cmd *exec.Cmd) ([]byte, error) {
	w := &cappedWriter{limit: bashOutputCap}
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	return w.bytes(), err
}

// cappedWriter keeps the first half of what it is given and the last
// half, and says in the middle how much went by.
//
// Head and tail rather than head alone: a command that fails usually says
// why at the end, and one that is going to be read for what it found
// usually says that at the start.
type cappedWriter struct {
	limit int
	head  []byte
	tail  []byte
	total int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.total += len(p)
	half := w.limit / 2
	if len(w.head) < half {
		take := half - len(w.head)
		if take > len(p) {
			take = len(p)
		}
		w.head = append(w.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		w.tail = append(w.tail, p...)
		if len(w.tail) > half {
			w.tail = w.tail[len(w.tail)-half:]
		}
	}
	return w.total, nil
}

func (w *cappedWriter) bytes() []byte {
	if w.total <= len(w.head)+len(w.tail) {
		return append(w.head, w.tail...)
	}
	skipped := fmt.Sprintf("\n… %d bytes of output are not shown …\n", w.total-len(w.head)-len(w.tail))
	out := make([]byte, 0, len(w.head)+len(skipped)+len(w.tail))
	out = append(out, w.head...)
	out = append(out, skipped...)
	return append(out, w.tail...)
}

func exitStatus(err error) (int, bool) {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 0, false
	}
	if code := ee.ExitCode(); code >= 0 {
		return code, true
	}
	return 0, false
}

// withNotice keeps an empty result from looking like a lost one.
//
// A command that produced no output and a command whose output went
// astray are the same empty string, and nothing downstream can tell them
// apart. The other tools already refuse to do this: grep answers "no
// matches" and glob "no files match" rather than returning nothing at
// all. A shell command has no words of its own for it, so these are
// localcode's.
//
// Output with no notice is passed through exactly as it came, so the
// ordinary successful command is byte-for-byte what it always was.
func withNotice(out, notice string) string {
	switch {
	case notice == "" && out != "":
		return out
	case notice == "" && out == "":
		return "(no output)"
	case out == "":
		return notice
	}
	return strings.TrimRight(out, "\n") + "\n" + notice
}
