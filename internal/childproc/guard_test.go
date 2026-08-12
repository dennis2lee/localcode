package childproc

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exemptSpawnSites are exec.Command calls that deliberately do not call
// Hide, keyed by "<path>:<function>". Each needs a reason, because the
// default has to be "hide it" — a console window appearing on the Windows
// desktop build is invisible to anyone developing on macOS or Linux, so this
// list is the only thing standing between a new exec.Command and the bug
// coming back.
var exemptSpawnSites = map[string]string{
	// The mac/linux dialog helpers: these OSes have no notion of a spawned
	// console window, and Hide is a no-op there anyway.
	"internal/dialog/dialog.go:pickDarwin": "macOS only; Hide is a no-op on darwin",
	"internal/dialog/dialog.go:pickLinux":  "Linux only; Hide is a no-op on linux",
	// LookPath, not a spawn.
	"internal/dialog/dialog.go:linuxHelper": "exec.LookPath only, starts nothing",
}

// TestEverySpawnSiteHidesItsWindow walks the repository's non-test Go source
// and fails if a function starts a child process without calling
// childproc.Hide on it.
//
// This is a guard against a whole class of regression rather than a test of
// behavior: the symptom (a black console window per child) only appears on
// Windows, in the GUI-subsystem build, so nothing in a normal dev loop or in
// CI's headless smoke check would catch a newly added exec.Command that
// forgot it.
func TestEverySpawnSiteHidesItsWindow(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	var missing []string

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			// .claude holds git worktrees, which are whole second copies
			// of the repository. Walking into one scans every file twice
			// and reports the copy's paths as unexempted — so this test
			// failed for anyone who happened to have a worktree open,
			// naming files that are exempt in the tree being tested.
			case ".git", ".claude", "dist", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // not our job to police syntax; the build does that
		}
		rel, _ := filepath.Rel(root, path)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			spawns, hides := false, false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				// A call to Hide from inside this package has no
				// qualifier, so matching only "childproc.Hide" reported
				// childproc's own spawn site as unhidden.
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "Hide" {
					hides = true
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch {
				case pkg.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext"):
					spawns = true
				case pkg.Name == "childproc" && sel.Sel.Name == "Hide":
					hides = true
				}
				return true
			})

			key := rel + ":" + fn.Name.Name
			if spawns && !hides {
				if _, exempt := exemptSpawnSites[key]; !exempt {
					missing = append(missing, key)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(missing) > 0 {
		t.Errorf(`these functions start a child process without calling childproc.Hide:
  %s

On Windows, a GUI-subsystem process (localcode-gui.exe) gives every console
child its own visible window. Add childproc.Hide(cmd) before starting it, or
add an entry to exemptSpawnSites with the reason it doesn't need one.`,
			strings.Join(missing, "\n  "))
	}
}
