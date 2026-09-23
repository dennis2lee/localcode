package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hang has to end by itself, and it has to say what hung.
//
// On 2026-09-22 the "Test the Windows code paths" step of gui-windows.yml,
// which takes about 75 seconds, ran for 38 minutes. The job carried no
// timeout-minutes and GitHub's default is six hours, so nothing was going
// to stop it; it was force-cancelled by hand, and a force-cancelled
// attempt keeps no log, so which test hung is still unknown. The re-run
// passed in two minutes.
//
// Two rules come out of that, and this file holds both. Every job gets a
// timeout-minutes, so a wedged runner ends in minutes rather than hours.
// Every `go test` gets a -timeout, because the job timeout is a bound and
// not a diagnosis: it kills the runner and reports nothing, while a
// `go test` reaching its own timeout panics and prints every goroutine's
// stack, which names the test. Go's default of ten minutes per package is
// both looser than anything here needs and silent about being a default,
// which is how a step with no opinion about hanging came to have one.

// ciFiles are the files that run this repo's tests unattended.
func ciFiles(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..")
	out := map[string]string{}
	for _, rel := range []string{
		filepath.Join(".github", "workflows", "gate.yml"),
		filepath.Join(".github", "workflows", "gui-windows.yml"),
		filepath.Join("scripts", "check.sh"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		out[rel] = string(raw)
	}
	return out
}

// Every `go test` that runs unattended says how long it may take.
func TestEveryCIGoTestCarriesATimeout(t *testing.T) {
	for rel, body := range ciFiles(t) {
		for i, line := range strings.Split(body, "\n") {
			start := strings.Index(line, "go test")
			if start < 0 || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			// One line can hold two, joined by && as the gui check is.
			for _, invocation := range strings.Split(line[start:], "&&") {
				if !strings.Contains(invocation, "go test") {
					continue
				}
				if hasTimeoutFlag(invocation) {
					continue
				}
				t.Errorf("%s:%d: this `go test` has no -timeout, so a hang here waits on Go's silent ten-minute default and the job timeout, and names nothing:\n\t%s",
					rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// hasTimeoutFlag reports whether one `go test` invocation sets -timeout,
// in either spelling.
func hasTimeoutFlag(invocation string) bool {
	for _, field := range strings.Fields(invocation) {
		if field == "-timeout" || strings.HasPrefix(field, "-timeout=") {
			return true
		}
	}
	return false
}

// Every workflow job says how long it may run.
//
// Read from the text rather than a parsed document: this repo has no YAML
// dependency, and the shape being checked is a line at a known indent
// inside a block at a known indent, which is the whole of what a parser
// would tell us here.
func TestEveryWorkflowJobCarriesATimeout(t *testing.T) {
	for rel, body := range ciFiles(t) {
		if !strings.HasSuffix(rel, ".yml") {
			continue
		}
		lines := strings.Split(body, "\n")
		inJobs := false
		job, jobLine := "", 0
		bounded := false
		// finish closes the job being read, if any.
		finish := func() {
			if job != "" && !bounded {
				t.Errorf("%s:%d: job %q has no timeout-minutes, so it runs until GitHub's six-hour default: a hang costs hours and is killed without a reason",
					rel, jobLine, job)
			}
		}
		for i, line := range lines {
			switch {
			case line == "jobs:":
				inJobs = true
				continue
			case !inJobs, strings.TrimSpace(line) == "", strings.HasPrefix(strings.TrimSpace(line), "#"):
				continue
			case !strings.HasPrefix(line, " "):
				// A top-level key after jobs: ends the block.
				finish()
				job, inJobs, bounded = "", false, false
				continue
			}
			if name, ok := strings.CutPrefix(line, "  "); ok && !strings.HasPrefix(name, " ") && strings.HasSuffix(strings.TrimSpace(name), ":") {
				finish()
				job, jobLine, bounded = strings.TrimSuffix(strings.TrimSpace(name), ":"), i+1, false
				continue
			}
			if job != "" && strings.HasPrefix(line, "    timeout-minutes:") {
				bounded = true
			}
		}
		finish()
		if job == "" && !strings.Contains(body, "timeout-minutes:") {
			t.Errorf("%s: no job was read, so this test is checking nothing", rel)
		}
	}
}
