package config

import (
	"encoding/json"
	"testing"
)

func TestToolPermissionUnmarshalFlatString(t *testing.T) {
	var tp ToolPermission
	if err := json.Unmarshal([]byte(`"allow"`), &tp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if tp.Flat != DecisionAllow {
		t.Errorf("Flat = %q, want %q", tp.Flat, DecisionAllow)
	}
	if len(tp.Rules) != 0 {
		t.Errorf("expected no Rules for a flat string, got %+v", tp.Rules)
	}
}

func TestToolPermissionUnmarshalRuleArray(t *testing.T) {
	var tp ToolPermission
	data := `[{"match":"*","decision":"ask"},{"match":"git *","decision":"allow"}]`
	if err := json.Unmarshal([]byte(data), &tp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if tp.Flat != "" {
		t.Errorf("expected empty Flat for a rule array, got %q", tp.Flat)
	}
	if len(tp.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %+v", tp.Rules)
	}
	if tp.Rules[0].Match != "*" || tp.Rules[0].Decision != DecisionAsk {
		t.Errorf("Rules[0] = %+v", tp.Rules[0])
	}
	if tp.Rules[1].Match != "git *" || tp.Rules[1].Decision != DecisionAllow {
		t.Errorf("Rules[1] = %+v", tp.Rules[1])
	}
}

func TestToolPermissionUnmarshalInvalidErrors(t *testing.T) {
	var tp ToolPermission
	if err := json.Unmarshal([]byte(`42`), &tp); err == nil {
		t.Error("expected an error for a permission value that's neither a string nor an array")
	}
}

func TestResolvePermissionNoConfigFallsBackToStatic(t *testing.T) {
	c := &Config{}
	if got := c.ResolvePermission("bash", "ls", true); got != DecisionAsk {
		t.Errorf("ResolvePermission() = %q, want %q (static default preserved with no config)", got, DecisionAsk)
	}
	if got := c.ResolvePermission("read_file", "x.go", false); got != DecisionAllow {
		t.Errorf("ResolvePermission() = %q, want %q", got, DecisionAllow)
	}
}

func TestResolvePermissionFlatOverridesStatic(t *testing.T) {
	c := &Config{Permissions: map[string]ToolPermission{
		"bash": {Flat: DecisionAllow},
	}}
	if got := c.ResolvePermission("bash", "any command", true); got != DecisionAllow {
		t.Errorf("ResolvePermission() = %q, want %q (flat rule overrides the tool's static \"ask\")", got, DecisionAllow)
	}
}

func TestResolvePermissionDenyBlocksEvenNormallySafeTool(t *testing.T) {
	// read_file's own static default is "no permission needed" — a
	// "deny" config rule should still be able to block it entirely.
	c := &Config{Permissions: map[string]ToolPermission{
		"read_file": {Flat: DecisionDeny},
	}}
	if got := c.ResolvePermission("read_file", "secrets.env", false); got != DecisionDeny {
		t.Errorf("ResolvePermission() = %q, want %q", got, DecisionDeny)
	}
}

func TestResolvePermissionRulesLastMatchWins(t *testing.T) {
	c := &Config{Permissions: map[string]ToolPermission{
		"bash": {Rules: []PermissionRule{
			{Match: "*", Decision: DecisionAsk},
			{Match: "git *", Decision: DecisionAllow},
			{Match: "git push*", Decision: DecisionDeny},
		}},
	}}

	cases := []struct {
		command string
		want    Decision
	}{
		{"ls -la", DecisionAsk},        // only "*" matches
		{"git status", DecisionAllow},  // "*" then "git *" — last wins
		{"git push origin", DecisionDeny}, // all three match — last ("git push*") wins
	}
	for _, tc := range cases {
		if got := c.ResolvePermission("bash", tc.command, true); got != tc.want {
			t.Errorf("ResolvePermission(bash, %q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestResolvePermissionWildcardToolFallback(t *testing.T) {
	c := &Config{Permissions: map[string]ToolPermission{
		"*": {Flat: DecisionAllow},
	}}
	if got := c.ResolvePermission("bash", "rm -rf /", true); got != DecisionAllow {
		t.Errorf("ResolvePermission() = %q, want the \"*\" fallback rule (%q) to apply", got, DecisionAllow)
	}
}

func TestResolvePermissionExactToolWinsOverWildcard(t *testing.T) {
	c := &Config{Permissions: map[string]ToolPermission{
		"*":    {Flat: DecisionAllow},
		"bash": {Flat: DecisionDeny},
	}}
	if got := c.ResolvePermission("bash", "anything", true); got != DecisionDeny {
		t.Errorf("ResolvePermission() = %q, want the exact \"bash\" rule (%q) to win over \"*\"", got, DecisionDeny)
	}
	if got := c.ResolvePermission("edit", "file.go", true); got != DecisionAllow {
		t.Errorf("ResolvePermission() = %q, want the \"*\" fallback (%q) for a tool with no exact rule", got, DecisionAllow)
	}
}

func TestResolvePermissionNoRuleMatchesFallsBackToStatic(t *testing.T) {
	// A rule list that never matches subject (e.g. all patterns are for
	// specific paths and this one is different) should fall through to
	// the tool's static default, not silently deny.
	c := &Config{Permissions: map[string]ToolPermission{
		"write_file": {Rules: []PermissionRule{
			{Match: "dist/*", Decision: DecisionAllow},
		}},
	}}
	if got := c.ResolvePermission("write_file", "src/main.go", true); got != DecisionAsk {
		t.Errorf("ResolvePermission() = %q, want the static default %q when no rule matches", got, DecisionAsk)
	}
	if got := c.ResolvePermission("write_file", "dist/out.js", true); got != DecisionAllow {
		t.Errorf("ResolvePermission() = %q, want %q for a matching rule", got, DecisionAllow)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, subject string
		want             bool
	}{
		{"*", "anything", true},
		{"*", "", true},
		{"git *", "git status", true},
		{"git *", "gitstatus", false},
		{"git *", "not git status", false},
		{"rm *", "rm -rf /", true},
		{"*.env", "secrets.env", true},
		{"*.env", "secrets.env.bak", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"a?c", "abbc", false},
	}
	for _, tc := range cases {
		if got := globMatch(tc.pattern, tc.subject); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
		}
	}
}
