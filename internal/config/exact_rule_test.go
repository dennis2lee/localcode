package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPermissionRuleForLiteralSubjects(t *testing.T) {
	for _, tc := range []struct {
		name, tool, subject, other string
	}{
		{"path_star", "write_file", "/proj/a*b", "/proj/aXYZb"},
		{"path_question", "write_file", "/proj/a?b", "/proj/aXb"},
		{"path_backslash_star", "write_file", `/proj/a\*b`, `/proj/a\XYZb`},
		{"path_backslash_question", "write_file", `/proj/a\?b`, `/proj/a\Xb`},
		{"path_backslashes", "write_file", `/proj/a\\*?b\`, `/proj/a\\XYb\`},
		{"path_brackets", "write_file", "/proj/[ab]", "/proj/a"},
		{"path_backslash_only", "write_file", `/proj/a\b`, "/proj/ab"},
		{"path_unicode", "write_file", "/proj/한*?글", "/proj/한XY글"},
		{"bash_one_word", BashToolName, "custom*", "customXYZ"},
		{"bash_whole_command", BashToolName, "rm build*", "rm buildXYZ"},
		{"bash_program", BashToolName, "custom* build", "customXYZ build"},
		{"bash_program_arguments", BashToolName, "custom* build", "custom* test"},
		{"bash_quoted", BashToolName, `rm 'build*'`, `rm 'buildXYZ'`},
		{"bash_quoted_quote", BashToolName, `rm 'build"*'`, `rm 'build"XYZ'`},
		{"bash_backslash", BashToolName, `rm build\?`, `rm buildX`},
		{"bash_one_word_question", BashToolName, "custom?", "customX"},
		{"bash_one_word_spaces", BashToolName, "  custom*  ", "customXYZ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.AddPermissionRuleRuntime(tc.tool, PermissionRuleFor(tc.tool, tc.subject))
			if got := cfg.ResolvePermission(tc.tool, tc.other, true); got != DecisionAsk {
				t.Errorf("ResolvePermission(%q, %q) = %q, want ask", tc.tool, tc.other, got)
			}
			if got := cfg.ResolvePermission(tc.tool, tc.subject, true); got != DecisionAllow {
				t.Errorf("ResolvePermission(%q, %q) = %q, want allow", tc.tool, tc.subject, got)
			}
		})
	}
}

func TestPermissionRuleForGeneralizedProgram(t *testing.T) {
	for _, program := range []string{"cargo", "npm", "git"} {
		t.Run(program, func(t *testing.T) {
			rule := PermissionRuleFor(BashToolName, program+" build")
			want := PermissionRule{Match: program + " *", Decision: DecisionAllow}
			if rule != want {
				t.Fatalf("rule = %+v, want %+v", rule, want)
			}
			tp := ToolPermission{Rules: []PermissionRule{rule}}
			for _, subject := range []string{program + " test", program + " "} {
				if got, ok := tp.resolve(subject); !ok || got != DecisionAllow {
					t.Errorf("rule %+v does not allow %q", rule, subject)
				}
			}
			if got, ok := tp.resolve("other test"); ok {
				t.Errorf("rule %+v matches other program: %q", rule, got)
			}
		})
	}
}

func TestPermissionRuleForLiteralProgram(t *testing.T) {
	for _, program := range []string{"custom*", "custom?", `C:\tools\custom*?`} {
		t.Run(program, func(t *testing.T) {
			subject := program + " build"
			rule := PermissionRuleFor(BashToolName, subject)
			want := PermissionRule{Match: subject, Decision: DecisionAllow, Literal: true}
			if rule != want {
				t.Fatalf("rule = %+v, want %+v", rule, want)
			}
			tp := ToolPermission{Rules: []PermissionRule{rule}}
			if got, ok := tp.resolve(subject); !ok || got != DecisionAllow {
				t.Errorf("rule %+v does not allow %q", rule, subject)
			}
			for _, other := range []string{program + " test", "customXYZ build", "customX build"} {
				if got, ok := tp.resolve(other); ok {
					t.Errorf("rule %+v matches %q: %q", rule, other, got)
				}
			}
		})
	}
}

func TestPermissionRuleLiteralRoundTrip(t *testing.T) {
	for _, tc := range []struct{ tool, subject, other string }{
		{"write_file", `/proj/a*?\[x]\`, `/proj/aXY\[x]\`},
		{BashToolName, "custom*", "customXYZ"},
		{BashToolName, "rm build*", "rm buildXYZ"},
		{BashToolName, "custom* build", "customXYZ build"},
	} {
		t.Run(tc.subject, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			rule := PermissionRuleFor(tc.tool, tc.subject)
			if err := AddPermissionRuleToFile(path, tc.tool, rule); err != nil {
				t.Fatal(err)
			}
			for pass := 0; pass < 2; pass++ {
				cfg, err := LoadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := cfg.Permissions[tc.tool].Rules; len(got) != 1 || got[0] != rule {
					t.Fatalf("loaded rules = %+v, want %+v", got, rule)
				}
				if got := cfg.ResolvePermission(tc.tool, tc.other, true); got != DecisionAsk {
					t.Errorf("loaded ResolvePermission(%q) = %q, want ask", tc.other, got)
				}
				if got := cfg.ResolvePermission(tc.tool, tc.subject, true); got != DecisionAllow {
					t.Errorf("loaded ResolvePermission(%q) = %q, want allow", tc.subject, got)
				}
				// Rewriting another rule also decodes and re-encodes this one.
				if err := AddPermissionRuleToFile(path, "other_tool", PermissionRule{Match: "*", Decision: DecisionAsk}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPermissionRuleLegacyPatternsRoundTrip(t *testing.T) {
	for _, rules := range []string{
		`[{"match":"/proj/a*?b","decision":"allow"},{"match":"C:\\proj\\*","decision":"allow"}]`,
		`{"/proj/a*?b":"allow","C:\\proj\\*":"allow"}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"permission":{"write_file":`+rules+`}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		for pass := 0; pass < 2; pass++ {
			cfg, err := LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, subject := range []string{"/proj/aXYZb", "/proj/a/nested/b", `C:\proj\child.txt`} {
				if got := cfg.ResolvePermission("write_file", subject, true); got != DecisionAllow {
					t.Errorf("legacy ResolvePermission(%q) = %q, want allow", subject, got)
				}
			}
			if got := cfg.ResolvePermission("write_file", `C:\proj*`, true); got != DecisionAsk {
				t.Errorf("legacy backslash was treated as an escape: %q", got)
			}
			if err := AddPermissionRuleToFile(path, "write_file", PermissionRuleFor("write_file", "/different/*")); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPermissionRuleLiteralIdentity(t *testing.T) {
	glob := PermissionRule{Match: "/proj/a*b", Decision: DecisionAllow}
	literal := PermissionRuleFor("write_file", glob.Match)
	deny := PermissionRule{Match: glob.Match, Decision: DecisionDeny}
	other := PermissionRule{Match: "/proj/other", Decision: DecisionAllow}
	for _, first := range []PermissionRule{glob, literal} {
		cfg := &Config{}
		for _, rule := range []PermissionRule{first, glob, literal, glob, literal, deny, other} {
			cfg.AddPermissionRuleRuntime("write_file", rule)
		}
		if got := len(cfg.Permissions["write_file"].Rules); got != 4 {
			t.Fatalf("rules = %d, want distinct literal and glob rules without duplicates", got)
		}
		cfg.RemovePermissionRuleRuntime("write_file", glob)
		got := cfg.Permissions["write_file"].Rules
		if len(got) != 2 || got[0] != deny || got[1] != other {
			t.Errorf("removal left %+v, want only deny and other", got)
		}
	}
	cfg := &Config{Permissions: Permissions{"write_file": {Flat: DecisionAllow}}}
	cfg.RemovePermissionRuleRuntime("write_file", PermissionRule{Match: "*", Decision: DecisionAllow, Literal: true})
	if _, ok := cfg.Permissions["write_file"]; ok {
		t.Error("removal left the matching flat permission")
	}
}

func TestPermissionRuleLiteralFileRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	glob := PermissionRule{Match: "/proj/a*b", Decision: DecisionAllow}
	for _, rule := range []PermissionRule{glob, PermissionRuleFor("write_file", glob.Match)} {
		if err := AddPermissionRuleToFile(path, "write_file", rule); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemovePermissionRuleFromFile(path, "write_file", glob); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Permissions["write_file"]; ok {
		t.Error("removal left a rule for write_file")
	}
}

func TestPermissionRuleHandWrittenLiteral(t *testing.T) {
	var tp ToolPermission
	if err := json.Unmarshal([]byte(`[{"match":"/proj/a*b","decision":"deny","literal":true}]`), &tp); err != nil {
		t.Fatal(err)
	}
	if got, ok := tp.resolve("/proj/a*b"); !ok || got != DecisionDeny {
		t.Errorf("literal subject = %q, %v, want deny, true", got, ok)
	}
	if got, ok := tp.resolve("/proj/aXYZb"); ok {
		t.Errorf("literal rule matches widened subject: %q", got)
	}
}

func TestPermissionRuleLiteralJSON(t *testing.T) {
	data, err := json.Marshal(PermissionRuleFor("write_file", "/proj/a*b"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"match":"/proj/a*b","decision":"allow","literal":true}` {
		t.Errorf("stored literal rule = %s", data)
	}
}

func TestPermissionRuleWithoutWildcards(t *testing.T) {
	for _, subject := range []string{"/proj/ab", `/proj/a\b`, "/proj/[ab]"} {
		rule := PermissionRuleFor("write_file", subject)
		if rule != (PermissionRule{Match: subject, Decision: DecisionAllow}) {
			t.Errorf("rule = %+v, want unchanged subject without flag", rule)
		}
		data, err := json.Marshal(rule)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 2 {
			t.Errorf("unexpected JSON fields: %s", data)
		}
	}
}

// A literal bash rule is the command as approved, quotes and all. Matched
// against the command with its quoting removed as well, approving
// rm "a*b", which removes the one file named a*b, allowed rm a*b, which
// removes every file the shell expands a*b to.
func TestPermissionRuleLiteralKeepsItsQuotes(t *testing.T) {
	for _, tc := range []struct{ approved, other string }{
		{`rm "a*b"`, `rm a*b`},
		{`rm 'build*'`, `rm build*`},
		{`custom* "x y"`, `custom* x y`},
	} {
		cfg := &Config{}
		cfg.AddPermissionRuleRuntime(BashToolName, PermissionRuleFor(BashToolName, tc.approved))
		if got := cfg.ResolvePermission(BashToolName, tc.other, true); got != DecisionAsk {
			t.Errorf("approving %s resolves %s as %q, want ask", tc.approved, tc.other, got)
		}
		if got := cfg.ResolvePermission(BashToolName, tc.approved, true); got != DecisionAllow {
			t.Errorf("approving %s resolves it again as %q, want allow", tc.approved, got)
		}
	}
}
