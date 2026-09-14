package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Windows runs every test in this package, with no filter.
//
// cmd/localcode used to run on Windows behind a -run inclusion filter
// naming the tests that passed there. An inclusion filter is the wrong
// shape for this even when it is green: a test added tomorrow does not
// match it, so it does not run on Windows, and nobody is told. The
// defect is not a test failing, it is a test never running, which no
// suite can report from inside itself — hence a test reading the job
// that runs it.
//
// What is checked is the requirement, not the spelling: any `go test`
// invocation in .github/workflows/gui-windows.yml that reaches this
// package — naming it, or running ./... — must not select which tests
// run. If a test genuinely cannot run on Windows one day, the sanctioned
// shape is an explicit exclusion with a reason per entry, and this test
// is where that conversation happens: update it, do not work around it.
func TestNoTestInThisPackageIsHiddenFromWindows(t *testing.T) {
	// From the package directory, the way TestEveryToolIsDocumented
	// reaches docs/USAGE.md: the test never changes directory, so a
	// relative path names the file it means.
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "gui-windows.yml"))
	if err != nil {
		t.Fatalf("read gui-windows.yml: %v", err)
	}

	var covering, selecting []string
	for _, line := range strings.Split(string(raw), "\n") {
		start := strings.Index(line, "go test")
		if start < 0 {
			continue
		}
		invocation := line[start:]
		if !strings.Contains(invocation, "cmd/localcode") && !strings.Contains(invocation, "./...") {
			continue
		}
		covering = append(covering, strings.TrimSpace(line))
		for _, field := range strings.Fields(invocation) {
			if field == "-run" || field == "-skip" ||
				strings.HasPrefix(field, "-run=") || strings.HasPrefix(field, "-skip=") {
				selecting = append(selecting, strings.TrimSpace(line))
				break
			}
		}
	}

	// A workflow that stopped running this package at all would pass a
	// test that only looks for filters. The passing state this protects
	// is "runs whole", which starts with "runs".
	if len(covering) == 0 {
		t.Fatal("no `go test` in gui-windows.yml reaches cmd/localcode any more, " +
			"so this test is checking nothing: the package must run on Windows whole")
	}
	if len(selecting) > 0 {
		t.Errorf("these Windows invocations select which of this package's tests run:\n  %s\n"+
			"Run cmd/localcode whole there. A test added tomorrow must run on Windows "+
			"without anyone having to name it first.",
			strings.Join(selecting, "\n  "))
	}
}
