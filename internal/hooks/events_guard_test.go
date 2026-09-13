package hooks

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every lifecycle event constant defined in hooks.go must reach AllEvents,
// KnownEvents, the configuration validation error message, and the reference
// documentation in config.example.json.
//
// The hook event list was previously duplicated by hand across four sites:
// the Event* constants, the KnownEvents lookup map, the config validator's
// "want one of" error string, and the reference note in config.example.json.
// When new lifecycle events (pre_model, post_model, delegate, compact, retry)
// were introduced, the validator error and the example config silently drifted,
// leaving users with error messages that omitted valid events and example notes
// describing only half the engine. Meanwhile, a guard in internal/agent compared
// two hand-written lists against each other, proving only that the two copies
// agreed.
//
// Go constants cannot be enumerated reflectively at runtime, so this test
// parses hooks.go as syntax to discover declared Event* constants and ensures
// none can be added without reaching AllEvents, KnownEvents, the validator,
// and the reference documentation.
func TestEveryEventConstantReachesEveryConsumer(t *testing.T) {
	declared, err := declaredEventConstants("hooks.go")
	if err != nil {
		t.Fatalf("parse hooks.go: %v", err)
	}
	if len(declared) < 5 {
		t.Fatalf("only %d Event* constants found in hooks.go, so this guard is checking almost nothing", len(declared))
	}

	seenInAll := make(map[string]bool, len(AllEvents))
	for _, e := range AllEvents {
		if seenInAll[e] {
			t.Errorf("AllEvents contains duplicate entry %q", e)
		}
		seenInAll[e] = true
	}

	for constName, eventVal := range declared {
		if !seenInAll[eventVal] {
			t.Errorf("constant %s = %q is declared in hooks.go but missing from AllEvents", constName, eventVal)
		}
	}

	declaredVals := make(map[string]string, len(declared))
	for name, val := range declared {
		declaredVals[val] = name
	}
	for _, e := range AllEvents {
		if _, ok := declaredVals[e]; !ok {
			t.Errorf("AllEvents contains %q, which has no Event* constant declared in hooks.go", e)
		}
	}

	if len(KnownEvents) != len(AllEvents) {
		t.Errorf("KnownEvents has %d entries, want %d (matching AllEvents)", len(KnownEvents), len(AllEvents))
	}
	for _, e := range AllEvents {
		if !KnownEvents[e] {
			t.Errorf("event %q is in AllEvents but not in KnownEvents", e)
		}
	}

	examplePath := filepath.Join("..", "..", "config.example.json")
	exampleRaw, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("read config.example.json: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(exampleRaw, &doc); err != nil {
		t.Fatalf("unmarshal config.example.json: %v", err)
	}
	rawNote, ok := doc["//hooks"]
	if !ok {
		t.Fatal("config.example.json has no //hooks note")
	}
	var note string
	if err := json.Unmarshal(rawNote, &note); err != nil {
		t.Fatalf("unmarshal //hooks note: %v", err)
	}

	for _, event := range AllEvents {
		if !strings.Contains(note, event) {
			t.Errorf("config.example.json //hooks note does not name %q: documentation has drifted from AllEvents", event)
		}
	}
	for _, setting := range []string{"timeout", "fail_closed"} {
		if !strings.Contains(note, setting) {
			t.Errorf("config.example.json //hooks note does not name %q: option added in v0.118.0 is missing from the example config", setting)
		}
	}

	fset := token.NewFileSet()
	configPath := filepath.Join("..", "config", "config.go")
	configSrc, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	configFile, err := parser.ParseFile(fset, configPath, configSrc, 0)
	if err != nil {
		t.Fatalf("parse config.go: %v", err)
	}
	var referencesHooksAllEvents bool
	ast.Inspect(configFile, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "AllEvents" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "hooks" {
			referencesHooksAllEvents = true
		}
		return true
	})
	if !referencesHooksAllEvents {
		t.Error("internal/config/config.go does not reference hooks.AllEvents: " +
			"validation errors must generate the accepted event list from the canonical slice rather than a hardcoded copy")
	}
}

// declaredEventConstants parses hooks.go and returns every constant whose
// identifier starts with "Event", mapped to its unquoted string literal value.
func declaredEventConstants(path string) (map[string]string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, err
	}

	consts := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vspec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vspec.Names {
				if !strings.HasPrefix(name.Name, "Event") {
					continue
				}
				if i >= len(vspec.Values) {
					continue
				}
				lit, ok := vspec.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					return nil, fmt.Errorf("unquote %s value %s: %w", name.Name, lit.Value, err)
				}
				consts[name.Name] = val
			}
		}
	}
	return consts, nil
}
