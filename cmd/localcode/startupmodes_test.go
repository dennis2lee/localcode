package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Every way localcode starts asks whether there is a newer release.
//
// There are three, and for two releases only one of them did. The change
// that added the Windows startup handoff replaced runDaemon's update call
// with the handoff branch instead of putting the two side by side, so
// `--headless` on macOS and Linux installed nothing at startup whatever
// auto_update said — and startupHandoffBinary returns false immediately
// on those platforms, so nothing else covered it. The comment above the
// deleted line went on describing both halves, and the daemon's own
// refusal told a headless caller it "still installs updates at startup".
//
// Read from the syntax rather than by running a startup, because the
// thing that broke is a call that is not there, and a test that drives
// the mode would need a release server, a writable binary and an exec.
// The next change shaped like that one fails here instead.
func TestEveryStartupModeTriesToUpdate(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "modes.go", nil, 0)
	if err != nil {
		t.Fatalf("parse modes.go: %v", err)
	}

	// runGUI is not in this list on purpose. The window has never done
	// this: its daemon is built after the window is on screen, and exec
	// out of a process holding a native window is not the same operation
	// as exec out of a headless one. It is recorded as open in
	// docs/IMPROVEMENTS.md rather than half-done here.
	want := map[string]bool{"runDaemon": false, "runEmbedded": false}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if _, watched := want[fn.Name.Name]; !watched {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "autoUpdateAtStartup" {
				want[fn.Name.Name] = true
			}
			return true
		})
	}

	var missing []string
	for name, found := range want {
		if !found {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("these startup modes never call autoUpdateAtStartup: %s\n"+
			"a mode that does not ask is one where \"auto_update\": true does nothing, "+
			"and startupHandoffBinary covers only Windows.", strings.Join(missing, ", "))
	}
}
