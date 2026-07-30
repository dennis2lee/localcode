package main

import (
	"reflect"
	"testing"
)

func TestParseScope(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantScope string
		wantRest  []string
		wantErr   bool
	}{
		{"no flags", []string{"myserver"}, "", []string{"myserver"}, false},
		{"scope long form", []string{"--scope", "project", "myserver"}, "project", []string{"myserver"}, false},
		{"scope short form", []string{"-s", "global", "myserver"}, "global", []string{"myserver"}, false},
		{"scope after positional", []string{"myserver", "-s", "project"}, "project", []string{"myserver"}, false},
		{"missing scope value", []string{"-s"}, "", nil, true},
		{"unknown flag rejected", []string{"--bogus", "myserver"}, "", nil, true},
		{"bare dash is positional", []string{"-", "myserver"}, "", []string{"-", "myserver"}, false},
		{"multiple positionals", []string{"name", "value"}, "", []string{"name", "value"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope, rest, err := parseScope(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseScope(%v) = nil error, want an error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseScope(%v): %v", tc.args, err)
			}
			if scope != tc.wantScope {
				t.Errorf("scope = %q, want %q", scope, tc.wantScope)
			}
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Errorf("rest = %v, want %v", rest, tc.wantRest)
			}
		})
	}
}

// TestParseScopeAgreesWithMCPAddOnUnknownFlags pins the B9 fix: mcp add
// already rejected an unknown flag; add-json and remove previously treated
// it as a positional argument instead. parseScope is what both of those
// now use, so this documents the contract the fix establishes.
func TestParseScopeAgreesWithMCPAddOnUnknownFlags(t *testing.T) {
	if _, _, err := parseScope([]string{"--transport", "http", "name"}); err == nil {
		t.Error("expected an unknown flag to be rejected, not silently treated as a positional")
	}
}
