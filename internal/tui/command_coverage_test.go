package tui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every command and alias in localCommands has a test somewhere that names it.
//
// The weakest useful guard, modeled on internal/agent/slash_coverage_test.go.
// It scans every test file in the repository for string literals of each
// command and alias in localCommands(). What it prevents is the failure mode
// that occurred repeatedly as commands were added: a command wired into
// localCommands, the router, and help texts, but never named in any test.
// Unexercised commands break nothing in the build, leaving missing coverage
// invisible until someone notices at runtime.
//
// Scanning source text rather than inspecting coverage counters is necessary
// because the subject being tested is a string command name rather than a
// function: multiple commands share dispatch logic, so line coverage of
// dispatchLocalCommand proves nothing about whether a specific slash command
// or alias was ever invoked or named in a test.
func TestEveryLocalCommandIsNamedInATest(t *testing.T) {
	root := repoRoot(t)
	files := testFileBodies(t, root)
	if len(files) == 0 {
		t.Fatalf("found no test files under %s — the scan is broken, not the coverage", root)
	}

	named := func(name string) []string {
		trimmed := strings.TrimPrefix(name, "/")
		var in []string
		for path, body := range files {
			if strings.Contains(body, `"/`+trimmed+`"`) || strings.Contains(body, `"/`+trimmed+` `) {
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

	for _, c := range localCommands() {
		for _, name := range c.names() {
			if len(named(name)) == 0 {
				t.Errorf("%s is in localCommands and no test names it", name)
			}
		}
	}
}

// Every command and alias in localCommands dispatches through dispatchLocalCommand.
//
// A source-text name check alone is too weak for local commands because these
// commands have ALIASES. A name-appears check cannot tell an alias that resolves
// from an alias that falls through and reaches the model as prose. An
// unrecognised slash command used to fall through to the model as ordinary chat
// text — a real failure mode that previously caused unwanted turns and
// accidental shell execution.
//
// Driving each command and alias through dispatchLocalCommand confirms that
// each one is registered in the command table, matches the dispatch rules,
// and intercepts the prompt rather than letting it fall through.
func TestEveryLocalCommandAndAliasDispatches(t *testing.T) {
	for _, cmd := range localCommands() {
		for _, name := range cmd.names() {
			t.Run(name, func(t *testing.T) {
				m := withAgents(newTestModel(), "a", "b")
				_, ok := dispatchLocalCommand(&m, name)
				if !ok {
					t.Errorf("%s matched no local command, so it would have gone to the model as prose", name)
				}
				if cmd.takesArg {
					mArg := withAgents(newTestModel(), "a", "b")
					_, okArg := dispatchLocalCommand(&mArg, name+" test-arg")
					if !okArg {
						t.Errorf("%s with argument matched no local command, so it would have gone to the model as prose", name)
					}
				}
			})
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
// Test files only, excluding this coverage test itself so it cannot satisfy
// its own coverage assertions: a command's name appears in localCommands()
// and help text, so a scan reaching implementation files would find all of
// them and report perfect coverage of nothing.
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
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		slashRel := filepath.ToSlash(rel)
		if slashRel == "internal/tui/command_coverage_test.go" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[slashRel] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(fmt.Errorf("walking %s: %w", root, err))
	}
	return out
}
