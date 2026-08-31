package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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

// One at a time per directory.
//
// This tool is the one place a read-only agent can run the project's own
// command, and the argument for that is entirely about *what* runs: one
// line, written by the person, fixed before any model saw it. Nothing in
// that argument says anything about how many of it run at once, and the
// answer turned out to be "as many as there are agents".
//
// A debate panel is concurrent by construction, check is in the reviewers'
// read-only list, and every child session inherits the parent's workspace.
// So three reviewers each deciding to check the work is three `go test
// ./...` runs in one tree at the same time, sharing a build cache, test
// binaries and output files, each on its own five-minute clock, on a
// machine already busy running the model. Measured before it was fixed: four
// concurrent calls all entered before any of them left.
//
// Keyed by directory rather than globally, because two sessions working in
// two projects have nothing to contend over and queueing them would be a
// cost for no reason. A per-directory lock is also all this can honestly
// promise: it does not stop the person running the same command in a
// terminal, and it is not a lock on the tree.
var checkRunning sync.Map // directory -> chan struct{}, a 1-deep semaphore

// takeDirectory blocks until nothing else is running a check in dir, and
// returns the release plus whether it had to wait. Reports false when ctx
// ended first: waiting for a five-minute test run is exactly when somebody
// presses Esc.
func takeDirectory(ctx context.Context, dir string) (release func(), waited, ok bool) {
	if dir == "" {
		// The process's own working directory. One key, because that is one
		// directory however many callers reached it with nothing set.
		dir = "."
	}
	v, _ := checkRunning.LoadOrStore(filepath.Clean(dir), make(chan struct{}, 1))
	sem := v.(chan struct{})

	select {
	case sem <- struct{}{}:
		return func() { <-sem }, false, true
	default:
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, true, true
	case <-ctx.Done():
		return func() {}, true, false
	}
}

func (c Check) Execute(ctx context.Context, _ json.RawMessage) Result {
	command := c.command()
	if command == "" {
		return Result{
			Content: "this project has no verify_command configured, so there is no check to run",
			IsError: true,
		}
	}

	dir := WorkingDir(ctx)
	release, waited, ok := takeDirectory(ctx, dir)
	if !ok {
		return Result{
			Content: "cancelled while waiting for another check to finish in this directory",
			IsError: true,
		}
	}
	defer release()

	// The clock starts after the wait, not before it. A check that queued
	// behind a five-minute test run has not used any of its own five
	// minutes, and cutting it short for somebody else's run would report a
	// timeout as though the command were slow.
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Said out loud, because otherwise the only trace of the wait is a
	// number that reads as a slow command.
	note := ""
	if waited {
		note = " after waiting for another check in this directory to finish"
	}

	cmd := shell.Command(ctx, command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	// A failing check is not a failing tool. The exit status is the
	// answer, and reporting it as an error made a red tool line for the
	// one outcome the caller most wants to read — and, for a reviewer,
	// turned the evidence that something is broken into the appearance
	// that the reviewer is broken.
	if err != nil {
		return Result{Content: fmt.Sprintf("%s\n(%s failed%s: %v)", out, command, note, err)}
	}
	return Result{Content: fmt.Sprintf("%s\n(%s passed%s)", out, command, note)}
}
