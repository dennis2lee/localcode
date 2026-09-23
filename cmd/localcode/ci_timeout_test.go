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
// which takes about 75 seconds, ran for 44 minutes. The job carried no
// timeout-minutes and GitHub's default is six hours, so nothing was going
// to stop it; it was force-cancelled by hand, and the re-run replaced the
// log, so which step stalled is still unknown. The re-run passed in two
// minutes.
//
// Two rules come out of that, and this file holds both. Every job gets a
// timeout-minutes, so a wedged runner ends in minutes rather than hours.
// Every `go test` gets a -timeout, because the job timeout is a bound and
// not a diagnosis: it kills the runner and reports nothing, while a
// `go test` reaching its own timeout panics and prints every goroutine's
// stack, which names the test. Go's default of ten minutes per package is
// both looser than anything here needs and silent about being a default,
// which is how a step with no opinion about hanging came to have one.
//
// Neither rule covers everything. `go test -timeout` bounds the test
// binary's own run, not the download, compile and link around it, and a
// stall there is the likeliest explanation of the incident above. That
// one needs a bound on the step, which the workflows carry and which is
// not checked here: a step is where a judgement about its own duration
// belongs, and a rule saying every step must guess at one would be
// noise.

// ciFiles are the files that run this repo's tests unattended.
//
// The workflows are found rather than listed. A list would cover the two
// that exist and say nothing about the third somebody adds, which is the
// same defect these tests exist to catch one level down: a rule that
// holds only where you remembered to apply it.
func ciFiles(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..")
	out := map[string]string{}
	read := func(rel string) {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		out[rel] = string(raw)
	}
	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if err != nil {
		t.Fatalf("list workflows: %v", err)
	}
	if len(workflows) == 0 {
		t.Fatal("no workflows found, so this test is checking nothing")
	}
	for _, abs := range workflows {
		read(filepath.Join(".github", "workflows", filepath.Base(abs)))
	}
	read(filepath.Join("scripts", "check.sh"))
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
//
// Anything from a `#` onwards is dropped first. Without that,
// `go test ./... # -timeout 5m` read as bounded while setting nothing,
// which is the one shape where this guard would have been worse than no
// guard: it says in review that a limit is there.
func hasTimeoutFlag(invocation string) bool {
	if before, _, found := strings.Cut(invocation, "#"); found {
		invocation = before
	}
	for _, field := range strings.Fields(invocation) {
		if field == "-timeout" || strings.HasPrefix(field, "-timeout=") {
			return true
		}
	}
	return false
}

// The gate's own rules file quotes the race lane's command, and a
// -timeout is sized from a measurement rather than derived, so the two
// copies of the number drift silently. They did: check.sh was raised to
// 20m from a CI measurement and AGENTS.md kept saying 6m, which is below
// what internal/tui takes on a macOS runner. Nothing would have run the
// documented command, and the next person sizing a bound would have read
// it.
func TestTheRulesFileQuotesTheGateItDescribes(t *testing.T) {
	files := ciFiles(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	want := timeoutValues(files[filepath.Join("scripts", "check.sh")])
	got := timeoutValues(string(raw))
	if len(want) == 0 {
		t.Fatal("no -timeout found in check.sh, so this test is checking nothing")
	}
	for lane, value := range got {
		if real, ok := want[lane]; ok && real != value {
			t.Errorf("AGENTS.md says the %s lane runs -timeout %s and scripts/check.sh runs %s: a bound sized from the doc would be the wrong one",
				lane, value, real)
		}
	}
}

// timeoutValues maps a lane's distinguishing flag to the -timeout beside
// it, for the `go test` lines in either file. The race lane is the one
// that matters and the one with a flag nothing else carries.
func timeoutValues(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		start := strings.Index(line, "go test")
		if start < 0 {
			continue
		}
		invocation := line[start:]
		if before, _, found := strings.Cut(invocation, "#"); found {
			invocation = before
		}
		fields := strings.Fields(invocation)
		lane := "plain"
		for _, f := range fields {
			if f == "-race" {
				lane = "race"
			}
			if f == "-tags" {
				lane = "gui"
			}
		}
		for i, f := range fields {
			if f == "-timeout" && i+1 < len(fields) {
				if _, seen := out[lane]; !seen {
					// One file quotes the command in backticks and the
					// other in shell quotes, so the duration arrives
					// wearing whichever one it was written in.
					out[lane] = strings.TrimFunc(fields[i+1], func(r rune) bool {
						return !strings.ContainsRune("0123456789hms", r)
					})
				}
			}
		}
	}
	return out
}

// Every workflow job says how long it may run.
//
// Read from the text rather than a parsed document: this repo has no YAML
// dependency, and the shape being checked is a line at a known indent
// inside a block at a known indent, which is the whole of what a parser
// would tell us here.
func TestEveryWorkflowJobCarriesATimeout(t *testing.T) {
	for rel, body := range ciFiles(t) {
		if !strings.HasSuffix(rel, ".yml") && !strings.HasSuffix(rel, ".yaml") {
			continue
		}
		lines := strings.Split(body, "\n")
		inJobs := false
		job, jobLine := "", 0
		bounded := false
		// Counted so a parser that stopped recognising jobs fails loudly
		// rather than passing every file by finding nothing to check.
		found := 0
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
				found++
				continue
			}
			if job != "" && strings.HasPrefix(line, "    timeout-minutes:") {
				bounded = true
			}
		}
		finish()
		if found == 0 {
			t.Errorf("%s: no job was read out of this workflow, so this test is checking nothing in it", rel)
		}
	}
}
