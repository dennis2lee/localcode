package tools

import (
	"context"
	"encoding/json"
	"fmt"

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

	cmd := shell.Command(ctx, args.Command)
	// The session's directory, not the daemon's. Leaving this unset is
	// what made the workspace process-wide: every shell command ran
	// wherever localcode itself was started, so two sessions in different
	// projects could not both be right, and moving one had to move both.
	// Empty means "inherit the process's own", which is the old behaviour
	// and the right fallback when nothing has said otherwise.
	cmd.Dir = WorkingDir(ctx)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Result{Content: fmt.Sprintf("%s\n(exit error: %v)", out, err), IsError: true}
	}
	return Result{Content: string(out)}
}
