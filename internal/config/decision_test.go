package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// An unknown decision must fail at load, naming the tool, the pattern and
// the bad value alongside the valid ones — the way unknown hook events do.
// A typo'd prohibition ("denied") used to sail through to execution.
func TestValidateRefusesUnknownDecisionInRule(t *testing.T) {
	c := &Config{Permissions: map[string]ToolPermission{
		"bash": {Rules: []PermissionRule{{Match: "rm *", Decision: "denied"}}},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want refusal of decision \"denied\"")
	}
	for _, want := range []string{"bash", "rm *", "denied", "allow", "ask", "deny"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

func TestValidateRefusesUnknownDecisionInFlatString(t *testing.T) {
	c := &Config{Permissions: map[string]ToolPermission{
		"bash": {Flat: "denied"},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want refusal of flat decision \"denied\"")
	}
	for _, want := range []string{"bash", "denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

// Near-miss, case difference and padding are all refusals. Nothing here is
// normalised: folding "DENY" into "deny" would silently rewrite a rule the
// user wrote into something nearby.
func TestValidateRefusesDecisionLookalikes(t *testing.T) {
	for _, bad := range []string{"denied", "DENY", "deny ", " deny", "Allow", "ASK", "", "block", "yes"} {
		c := &Config{Permissions: map[string]ToolPermission{
			"bash": {Rules: []PermissionRule{{Match: "*", Decision: Decision(bad)}}},
		}}
		if err := c.Validate(); err == nil {
			t.Errorf("Validate() = nil for decision %q, want refusal", bad)
		}
	}
}

func TestValidateAcceptsEveryKnownDecision(t *testing.T) {
	c := &Config{Permissions: map[string]ToolPermission{
		"bash":      {Flat: DecisionAllow},
		"read_file": {Flat: DecisionAsk},
		"edit":      {Flat: DecisionDeny},
		"glob": {Rules: []PermissionRule{
			{Match: "*", Decision: DecisionAllow},
			{Match: "secrets/*", Decision: DecisionAsk},
			{Match: "secrets/root", Decision: DecisionDeny},
		}},
	}}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for the three known decisions in both forms", err)
	}
}

func TestValidDecisionMatchesRoster(t *testing.T) {
	for _, d := range AllDecisions {
		if !ValidDecision(d) {
			t.Errorf("ValidDecision(%q) = false, want true for a rostered decision", d)
		}
	}
	if DecisionNames() == "" {
		t.Fatal("DecisionNames() is empty")
	}
	for _, d := range AllDecisions {
		if !strings.Contains(DecisionNames(), string(d)) {
			t.Errorf("DecisionNames() = %q, does not name rostered decision %q", DecisionNames(), d)
		}
	}
}

// Every Decision* constant declared in rules.go must reach AllDecisions,
// and AllDecisions must name nothing undeclared — the hooks package's
// AllEvents guard applied to the permission roster, so a fourth decision
// cannot be added without being taught to the validator.
func TestEveryDecisionConstantReachesAllDecisions(t *testing.T) {
	declared, err := declaredDecisionConstants("rules.go")
	if err != nil {
		t.Fatalf("parse rules.go: %v", err)
	}
	if len(declared) < 3 {
		t.Fatalf("only %d Decision* constants found in rules.go, so this guard is checking almost nothing", len(declared))
	}
	seen := map[string]bool{}
	for _, d := range AllDecisions {
		if seen[string(d)] {
			t.Errorf("AllDecisions contains duplicate entry %q", d)
		}
		seen[string(d)] = true
	}
	for constName, val := range declared {
		if !seen[val] {
			t.Errorf("constant %s = %q is declared in rules.go but missing from AllDecisions", constName, val)
		}
	}
	declaredVals := map[string]string{}
	for name, val := range declared {
		declaredVals[val] = name
	}
	for _, d := range AllDecisions {
		if _, ok := declaredVals[string(d)]; !ok {
			t.Errorf("AllDecisions contains %q, which has no Decision* constant declared in rules.go", d)
		}
	}
}

// declaredDecisionConstants parses rules.go and returns every constant
// whose identifier starts with "Decision" (other than the Decision type
// itself, which is a type declaration, not a constant), mapped to its
// unquoted string value.
func declaredDecisionConstants(path string) (map[string]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	consts := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		vspec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vspec.Names {
			if !strings.HasPrefix(name.Name, "Decision") || i >= len(vspec.Values) {
				continue
			}
			lit, ok := vspec.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			consts[name.Name] = val
		}
		return true
	})
	return consts, nil
}
