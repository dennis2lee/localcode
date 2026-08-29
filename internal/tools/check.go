package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"localcode/internal/shell"
)

// Running the project's own check, without handing anybody a shell.
//
// `bash` takes a command the model wrote, which is why it asks before it
// runs and why a read-only agent cannot have it: "cd /tmp && sh -c ..."
// is not a path and cannot be judged by looking at it. That rules out the
// most useful thing a reviewer could do — find out whether the code
// actually runs — and leaves it reviewing by reading.
//
// This is the narrow way back. The command is `verify_command` from
// config.json: one line, written by the person, fixed before any model
// saw it. The tool takes no arguments at all, so there is nothing for a
// model to influence. What it decides is whether to run the check, never
// what the check is.
//
// It is not registered when nothing is configured, so a project that has
// not said how to check itself does not advertise a tool that can only
// fail.

// Check runs the project's configured verification command.
type Check struct {
	// Command is asked each time rather than captured, so a configuration
	// reloaded at runtime takes effect on the next call rather than the
	// next restart.
	Command func() string
	// Timeout bounds the run; zero means five minutes. A test suite is
	// slower than the shell tool's usual two.
	Timeout time.Duration
}

func NewCheck(command func() string) Check { return Check{Command: command} }

func (Check) Name() string { return "check" }

func (c Check) Description() string {
	cmd := c.command()
	if cmd == "" {
		return "Run this project's configured check. Not configured in this project."
	}
	return "Run this project's own check and return its output and exit status. " +
		"It runs exactly `" + cmd + "` in the project directory, always, and takes no arguments — " +
		"you choose whether to run it, not what it runs. Use it to find out whether the code " +
		"actually builds and passes before saying anything about whether it works."
}

// DescriptionFor makes the description follow a reloaded configuration,
// so the model is never told about a command that is no longer the one.
func (c Check) DescriptionFor(context.Context) string { return c.Description() }

func (c Check) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (c Check) InputSchemaFor(context.Context) json.RawMessage { return c.InputSchema() }

// RequiresPermission is false, and the argument for it is the same one
// that makes this tool safe to give a read-only agent: what runs was
// written by the person in their own configuration file, before any of
// this started, and cannot be changed from here. That is the trust level
// a hook has, and hooks do not ask either.
//
// A rule can still deny it by name for a project where that is wrong.
func (Check) RequiresPermission(json.RawMessage) bool { return false }

// Subject is the command, so a permission rule can match on what would
// actually run rather than on the word "check".
func (c Check) Subject(json.RawMessage) string { return c.command() }

func (c Check) command() string {
	if c.Command == nil {
		return ""
	}
	return strings.TrimSpace(c.Command())
}

func (c Check) Execute(ctx context.Context, _ json.RawMessage) Result {
	command := c.command()
	if command == "" {
		return Result{
			Content: "this project has no verify_command configured, so there is no check to run",
			IsError: true,
		}
	}

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := shell.Command(ctx, command)
	cmd.Dir = WorkingDir(ctx)
	out, err := cmd.CombinedOutput()
	// A failing check is not a failing tool. The exit status is the
	// answer, and reporting it as an error made a red tool line for the
	// one outcome the caller most wants to read — and, for a reviewer,
	// turned the evidence that something is broken into the appearance
	// that the reviewer is broken.
	if err != nil {
		return Result{Content: fmt.Sprintf("%s\n(%s failed: %v)", out, command, err)}
	}
	return Result{Content: fmt.Sprintf("%s\n(%s passed)", out, command)}
}
