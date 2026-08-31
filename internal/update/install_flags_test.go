package update

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The flags the Windows installer is started with.
//
// A test that runs on every platform for something that only happens on
// one, because the flags are the whole of what makes an update apply and
// this machine cannot run them. What it pins is the reasoning, so that
// removing a flag has to be a decision rather than a tidy-up.
func TestTheInstallerIsStartedAtBasicUI(t *testing.T) {
	args := installerArgs(`C:\Users\u\AppData\Local\localcode\updates\localcode.msi`)

	if len(args) < 2 || args[0] != "/i" {
		t.Fatalf("args = %v, want an /i install", args)
	}
	if args[1] != `C:\Users\u\AppData\Local\localcode\updates\localcode.msi` {
		t.Errorf("the package is not the second argument: %v", args)
	}

	// /qb is the fix, not a preference. At full UI the engine looks for an
	// authored MsiRMFilesInUse dialog to offer closing the processes holding the
	// files; wixl cannot author dialogs, so this package has none, and the
	// documented fallback is that nothing is shown and a reboot is
	// scheduled instead. The install then reports success and changes
	// nothing until the machine restarts.
	if !has(args, "/qb") {
		t.Error("the installer runs at full UI, where a package with no files-in-use dialog schedules a reboot instead of replacing the files")
	}

	// Not silent. Replacing the program somebody is using is not something
	// to do behind a progress bar they cannot see or cancel, and /qn would
	// also close localcode with no warning at all.
	for _, quiet := range []string{"/qn", "/quiet", "/q"} {
		if has(args, quiet) {
			t.Errorf("%s: the installer would close localcode with nothing shown", quiet)
		}
	}

	// Nothing that suppresses the restart handling: /norestart would leave
	// the files in use unresolved, which is the state this fixes.
	for _, bad := range []string{"/norestart", "/forcerestart"} {
		if has(args, bad) {
			t.Errorf("%s changes what the Restart Manager is allowed to do", bad)
		}
	}
}

func has(args []string, want string) bool {
	for _, a := range args {
		if strings.EqualFold(a, want) {
			return true
		}
	}
	return false
}

// The call site, checked as source because it cannot be checked as code.
//
// installerArgs is pinned by the test above, and on its own that pins
// nothing that ships: startInstaller is behind a windows build tag, so no
// test on this machine can call it, and someone could inline the flags at
// the call site and leave every test here passing. Reading the file is the
// only check available, and this repository already uses the shape
// elsewhere (the spawn-site walk in internal/agent, the stylesheet
// property check) for the same reason: a guard that cannot run the code
// can still read it.
func TestTheWindowsCallSiteUsesTheFlagsThisFilePins(t *testing.T) {
	src, err := os.ReadFile("install_windows.go")
	if err != nil {
		t.Fatalf("read the windows install file: %v", err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "install_windows.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Command" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "exec" {
			return true
		}
		found = true

		// Exactly two arguments: the program, and this file's flags
		// spread into it. Anything else is flags written twice.
		if len(call.Args) != 2 {
			t.Errorf("exec.Command has %d arguments; the flags belong in installerArgs, not at the call site", len(call.Args))
			return false
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); !ok || lit.Value != `"msiexec"` {
			t.Errorf("the program is %v, want msiexec", call.Args[0])
		}
		spread, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			t.Fatalf("the second argument is not a call: the flags are not coming from installerArgs")
		}
		if call.Ellipsis == token.NoPos {
			t.Error("the flags are not spread, so installerArgs is not supplying the whole argument list")
		}
		name, ok := spread.Fun.(*ast.Ident)
		if !ok || name.Name != "installerArgs" {
			t.Errorf("the flags come from %v, not installerArgs, so the test above pins nothing that ships", spread.Fun)
		}
		return false
	})
	if !found {
		t.Error("install_windows.go starts no command, so nothing here reaches msiexec")
	}
}
