package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	want := timeoutValues(t, files[filepath.Join("scripts", "check.sh")])
	got := timeoutValues(t, string(raw))
	// Every lane check.sh runs must be one this can see, or a drift in
	// the lane it cannot see would go unreported.
	for _, lane := range []string{"race", "plain", "gui"} {
		if _, ok := want[lane]; !ok {
			t.Fatalf("no -timeout read for the %s lane of check.sh, so a drift in it would not be compared: %v", lane, want)
		}
	}
	for lane, value := range got {
		if real, ok := want[lane]; ok && real != value {
			t.Errorf("AGENTS.md says the %s lane runs -timeout %s and scripts/check.sh runs %s: a bound sized from the doc would be the wrong one",
				lane, value, real)
		}
	}
}

// timeoutValues maps each lane to the -timeout it runs with, in seconds,
// for the `go test` lines in either file.
//
// Three things it has to get right, each of which it got wrong first.
// Both spellings of the flag, because hasTimeoutFlag accepts both and a
// lane written as -timeout=20m would otherwise vanish from the
// comparison rather than be compared. The gui lane before the race lane,
// because the gui check carries -race too and matching on that first put
// the two under one key and dropped one of them. And the durations
// parsed rather than compared as text, so 1200s and 20m are the same
// answer.
func timeoutValues(t *testing.T, body string) map[string]time.Duration {
	t.Helper()
	out := map[string]time.Duration{}
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
		for i, f := range fields {
			if f == "-tags" && i+1 < len(fields) && strings.Contains(fields[i+1], "gui") {
				lane = "gui"
				break
			}
			if strings.HasPrefix(f, "-tags=") && strings.Contains(f, "gui") {
				lane = "gui"
				break
			}
			if f == "-race" {
				lane = "race"
			}
		}

		value := ""
		for i, f := range fields {
			if f == "-timeout" && i+1 < len(fields) {
				value = fields[i+1]
			} else if rest, ok := strings.CutPrefix(f, "-timeout="); ok {
				value = rest
			} else {
				continue
			}
			break
		}
		if value == "" {
			continue
		}
		// One file quotes the command in backticks and the other in
		// shell quotes, so the duration arrives wearing whichever one it
		// was written in.
		value = strings.TrimFunc(value, func(r rune) bool {
			return !strings.ContainsRune("0123456789hms", r)
		})
		d, err := time.ParseDuration(value)
		if err != nil {
			t.Errorf("cannot read %q as a duration, from:\n\t%s", value, strings.TrimSpace(line))
			continue
		}
		if _, seen := out[lane]; !seen {
			out[lane] = d
		}
	}
	return out
}

// hasTimeoutFlag's own cases, because nothing that runs feeds it the one
// it was fixed for: no workflow carries a commented-out -timeout, so
// removing the comment strip left the whole suite green and the fix
// unverified by anything.
func TestTheTimeoutFlagHasToBeRealToCount(t *testing.T) {
	cases := []struct {
		invocation string
		want       bool
	}{
		{"go test ./...", false},
		{"go test ./... -timeout 5m", true},
		{"go test ./... -timeout=5m", true},
		{"go test ./... # -timeout 5m", false},
		{"go test ./... #-timeout=5m", false},
		{"go test ./... -count=1 # a note about -timeout", false},
		{"go test -timeout 5m ./... # a note", true},
	}
	for _, c := range cases {
		if got := hasTimeoutFlag(c.invocation); got != c.want {
			t.Errorf("hasTimeoutFlag(%q) = %v, want %v", c.invocation, got, c.want)
		}
	}
}

// timeoutValues' own cases, because the files it reads use one spelling
// and one duration format, so the branches for the others are never
// exercised by anything that runs. Removing them left the suite green.
func TestTheLaneTimeoutsAreReadInEveryShapeTheyAreWritten(t *testing.T) {
	const body = `
	"race	race	go test ./... -race -parallel 8 -count=1 -timeout=1200s"
	"plain	plain	go test ./... -count=1 -timeout 10m"
	"go	gui	go build -tags gui ./... && go test -tags gui -race ./internal/gui/ -count=1 -timeout 600s"
`
	got := timeoutValues(t, body)
	want := map[string]time.Duration{
		"race":  20 * time.Minute,
		"plain": 10 * time.Minute,
		"gui":   10 * time.Minute,
	}
	for lane, d := range want {
		if got[lane] != d {
			t.Errorf("the %s lane read as %v, want %v: %v", lane, got[lane], d, got)
		}
	}
	// The gui lane carries -race too, so a scan that matched that first
	// filed it under race and one of the two disappeared.
	if len(got) != 3 {
		t.Errorf("read %d lanes, want 3: %v", len(got), got)
	}
	// A comment is not a bound here either.
	if v := timeoutValues(t, "go test ./... -race # -timeout 5m"); len(v) != 0 {
		t.Errorf("a commented-out timeout was read as one: %v", v)
	}
}

// The stamp is the one file whose wrongness cannot be seen: `make dist`
// trusts it. Every command that writes it has to be checked, which the
// redirect was not and then the move was not.
func TestTheStampIsNeverWrittenByAnUncheckedCommand(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "check.sh"))
	if err != nil {
		t.Fatalf("read check.sh: %v", err)
	}
	writes := 0
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, ".check-passed") {
			continue
		}
		// Reading it, or removing it, cannot leave a wrong one behind.
		if strings.HasPrefix(trimmed, "rm ") || strings.Contains(trimmed, "$stamp_file") {
			continue
		}
		// Every way a shell writes a file, not only the two this has
		// been bitten by. Named this way round because the test claims
		// "never written by an unchecked command", and a list of two
		// verbs does not say that.
		write := strings.Contains(trimmed, ">")
		for _, verb := range []string{"mv ", "cp ", "tee ", "install ", "ln ", "printf ", "echo ", "cat "} {
			write = write || strings.Contains(trimmed, verb)
		}
		if !write {
			continue
		}
		writes++
		if !strings.HasPrefix(trimmed, "elif !") && !strings.HasPrefix(trimmed, "if !") && !strings.Contains(trimmed, "||") {
			t.Errorf("scripts/check.sh:%d writes the stamp without checking whether it worked, so a failure here reports a passing gate over a stamp for some other tree:\n\t%s", i+1, trimmed)
		}
	}
	if writes == 0 {
		t.Error("no stamp write found in check.sh, so this test is checking nothing")
	}
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
