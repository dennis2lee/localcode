package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestEveryTestdataFixtureBuildsWithPlatformExtension walks the testdata
// directory and verifies that every fixture package found there builds into an
// executable with the platform-appropriate binary name (.exe on Windows, bare
// fixture name elsewhere), and that the resulting binary exists on disk at
// that exact path.
//
// Walking the directory on disk rather than checking a static list of fixture
// names is deliberate: the directory is the source of truth, and this ensures
// that whenever a new fixture is added under testdata, it cannot escape platform
// extension verification.
//
// On Windows, the OS process loader and exec.LookPath require the .exe
// extension to execute a binary by path. Without it, exec.Command fails with:
//
//	exec: "<path>\echoserver": executable file not found in %PATH%
//
// On Unix-like operating systems (macOS, Linux), executables conventionally have
// no extension.
func TestEveryTestdataFixtureBuildsWithPlatformExtension(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("read testdata directory: %v", err)
	}

	var fixtures []string
	for _, entry := range entries {
		if entry.IsDir() {
			fixtures = append(fixtures, entry.Name())
		}
	}
	if len(fixtures) == 0 {
		t.Fatal("found no fixture directories in testdata; directory scan is broken")
	}

	// Negative control: verify helperExeName actually differentiates Windows
	// from non-Windows platforms. A function that returned the bare name on
	// all platforms or .exe on all platforms would pass a vacuous check.
	if got := helperExeName("control", "windows"); got != "control.exe" {
		t.Fatalf("helperExeName for Windows = %q, want %q", got, "control.exe")
	}
	if got := helperExeName("control", "linux"); got != "control" {
		t.Fatalf("helperExeName for Linux = %q, want %q", got, "control")
	}
	if got := helperExeName("control", "darwin"); got != "control" {
		t.Fatalf("helperExeName for Darwin = %q, want %q", got, "control")
	}

	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			// Check the platform name calculation for Windows and non-Windows
			// explicitly, independent of which host OS this test happens to run on.
			winName := helperExeName(fixture, "windows")
			if !strings.HasSuffix(winName, ".exe") {
				t.Errorf("helperExeName(%q, \"windows\") = %q, want suffix .exe", fixture, winName)
			}
			unixName := helperExeName(fixture, "linux")
			if strings.HasSuffix(unixName, ".exe") {
				t.Errorf("helperExeName(%q, \"linux\") = %q, unexpectedly has suffix .exe", fixture, unixName)
			}

			// Build the fixture with the shared helper for the host platform.
			bin := buildTestHelper(t, fixture)

			fi, err := os.Stat(bin)
			if err != nil {
				t.Fatalf("fixture %q was built to %q but file does not exist: %v", fixture, bin, err)
			}
			if fi.IsDir() {
				t.Fatalf("fixture %q binary %q is a directory", fixture, bin)
			}

			// The returned binary path must match the host platform's requirement.
			if runtime.GOOS == "windows" {
				if !strings.HasSuffix(bin, ".exe") {
					t.Errorf("built binary %q missing .exe suffix on Windows", bin)
				}
			} else {
				if strings.HasSuffix(bin, ".exe") {
					t.Errorf("built binary %q unexpectedly has .exe suffix on %s", bin, runtime.GOOS)
				}
			}
		})
	}
}

// TestEveryGoBuildInTestFilesUsesSharedHelper parses all test files in the mcp
// package and confirms that any "go build" execution happens only inside
// buildTestHelper.
//
// A direct exec.Command("go", "build", ...) call at an individual test site
// risks writing or executing the binary without the platform executable extension,
// re-introducing the defect where tests pass on developer macOS/Linux machines
// but fail when executed on Windows.
func TestEveryGoBuildInTestFilesUsesSharedHelper(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob test files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("found no test files in mcp package")
	}

	fset := token.NewFileSet()
	var unsharedBuildSites []string

	for _, file := range files {
		node, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}

		for _, decl := range node.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				// Look for exec.Command(...) or exec.CommandContext(...)
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "exec" {
					return true
				}
				if sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext" {
					return true
				}

				// Check if arguments start with "go", "build"
				argOffset := 0
				if sel.Sel.Name == "CommandContext" {
					argOffset = 1
				}
				if len(call.Args) > argOffset+1 {
					firstArg, ok1 := call.Args[argOffset].(*ast.BasicLit)
					secondArg, ok2 := call.Args[argOffset+1].(*ast.BasicLit)
					if ok1 && ok2 && firstArg.Value == `"go"` && secondArg.Value == `"build"` {
						if fn.Name.Name != "buildTestHelper" {
							unsharedBuildSites = append(unsharedBuildSites, file+":"+fn.Name.Name)
						}
					}
				}
				return true
			})
		}
	}

	if len(unsharedBuildSites) > 0 {
		t.Errorf("these functions invoke \"go build\" directly instead of using buildTestHelper:\n  %s\n\nAll fixture builds in package mcp must use buildTestHelper to ensure the .exe extension is properly applied on Windows.",
			strings.Join(unsharedBuildSites, "\n  "))
	}
}
