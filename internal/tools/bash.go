package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

type Bash struct {
	// Timeout bounds how long a command may run; zero means 2 minutes.
	Timeout time.Duration
}

func (Bash) Name() string        { return "bash" }
func (Bash) Description() string { return "Run a shell command and return its combined stdout/stderr." }
func (Bash) InputSchema() json.RawMessage {
	return schema(`{"command":{"type":"string"}}`, "command")
}
func (Bash) RequiresPermission(json.RawMessage) bool { return true }

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

	cmd := exec.CommandContext(ctx, "sh", "-c", args.Command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Result{Content: fmt.Sprintf("%s\n(exit error: %v)", out, err), IsError: true}
	}
	return Result{Content: string(out)}
}
