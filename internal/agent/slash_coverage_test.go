package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every command in SlashCommands has a test somewhere that names it.
//
// The weakest useful guard, and deliberately so. It cannot tell a test
// that proves a command works from one that merely mentions it, and it
// does not try — what it catches is the case that actually happened
// repeatedly while these commands were being written: a command added to
// the list, to the router, to both help texts, and to no test at all.
// That one is invisible to every other guard in the build, because a
// command nothing exercises breaks nothing.
//
// Scanning source text rather than asking the toolchain for coverage
// because the thing being asked about is a string, not a function: the
// commands share a handful of routes, so line coverage of routeToggle
// says nothing about whether "/keep-going" was ever sent.
func TestEveryCommandIsNamedInATest(t *testing.T) {
	root := repoRoot(t)
	files := testFileBodies(t, root)
	if len(files) == 0 {
		t.Fatalf("found no test files under %s — the scan is broken, not the coverage", root)
	}

	named := func(name string) []string {
		var in []string
		for path, body := range files {
			if strings.Contains(body, `"/`+name+`"`) || strings.Contains(body, `"/`+name+` `) {
				in = append(in, path)
			}
		}
		return in
	}

	// A negative control, checked first. A matcher that reports every name
	// as covered would pass this test with no tests in the repository at
	// all, which is the failure this whole file exists to rule out.
	if where := named("not-a-real-command-negative-control"); len(where) > 0 {
		t.Fatalf("the matcher claims a command that does not exist is tested, in %v — it matches too much to prove anything", where)
	}

	for _, c := range SlashCommands() {
		if len(named(c.Name)) == 0 {
			t.Errorf("/%s is in SlashCommands and no test names it", c.Name)
		}
	}
}

// repoRoot walks up to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// testFileBodies reads every _test.go file in the tree, keyed by its path
// relative to root.
//
// Test files only, and that is the point rather than an optimisation: a
// command's name appears in its own route, in SlashCommands, and in both
// help texts, so a scan that reached implementation files would find all
// 39 of them and report perfect coverage of nothing.
func testFileBodies(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(fmt.Errorf("walking %s: %w", root, err))
	}
	return out
}
