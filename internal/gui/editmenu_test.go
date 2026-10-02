//go:build gui

package gui

import (
	"go/ast"
	"os"
	"strings"
	"testing"
)

// The macOS window has no menu bar unless one is installed, and on macOS
// the Command key equivalents for editing live in the menu. Before the
// Edit menu, Cmd+C on selected transcript text and Cmd+V into the prompt
// box silently did nothing in the desktop window, while working in
// browsers and the Windows window.
func TestEditMenuIsWiredIntoLaunch(t *testing.T) {
	_, f := parseSource(t, "gui.go")
	launch := funcDecl(f, "Launch")
	if launch == nil || launch.Body == nil {
		t.Fatal("gui.go has no Launch; this test no longer reads what it thinks it reads")
	}
	found := false
	ast.Inspect(launch.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "installEditMenu" && len(call.Args) == 0 {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("Launch does not call installEditMenu(), so the macOS window has no Edit menu and Cmd+C/Cmd+V do nothing there")
	}
}

// The Edit menu has to carry the six standard items with Command key
// equivalents: Undo, Redo, Cut, Copy, Paste, Select All. A menu with the
// wrong items, or with the right titles bound to the wrong selectors, is a
// different way of losing the same keystrokes.
func TestEditMenuCarriesTheStandardItems(t *testing.T) {
	src, err := os.ReadFile("editmenu_darwin.go")
	if err != nil {
		t.Fatalf("read editmenu_darwin.go: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		`{"Undo", @selector(undo:), "z", NSEventModifierFlagCommand}`,
		`{"Redo", @selector(redo:), "z", NSEventModifierFlagCommand | NSEventModifierFlagShift}`,
		`{"Cut", @selector(cut:), "x", NSEventModifierFlagCommand}`,
		`{"Copy", @selector(copy:), "c", NSEventModifierFlagCommand}`,
		`{"Paste", @selector(paste:), "v", NSEventModifierFlagCommand}`,
		`{"Select All", @selector(selectAll:), "a", NSEventModifierFlagCommand}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("editmenu_darwin.go no longer defines %s", want)
		}
	}
	if !strings.Contains(body, "setMainMenu") {
		t.Error("editmenu_darwin.go no longer installs the menu as the application main menu, so the key equivalents have nowhere to live")
	}
}

// Off macOS the installer is an explicit no-op: the Command key
// equivalents are a macOS convention, and the other platforms provide
// their editing bindings without one.
func TestEditMenuIsANoOpOffMacOS(t *testing.T) {
	src, err := os.ReadFile("editmenu_other.go")
	if err != nil {
		t.Fatalf("read editmenu_other.go: %v", err)
	}
	if !strings.Contains(string(src), "func installEditMenu() {}") {
		t.Error("editmenu_other.go no longer defines installEditMenu as a no-op")
	}
}
