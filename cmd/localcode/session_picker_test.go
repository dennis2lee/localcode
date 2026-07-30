package main

import "testing"

func TestParseChoice(t *testing.T) {
	const n = 3
	cases := []struct {
		name string
		line string
		want sessionChoice
	}{
		{"empty defaults to new", "", sessionChoice{action: actionNew}},
		{"n starts new", "n", sessionChoice{action: actionNew}},
		{"N is case-insensitive", "N", sessionChoice{action: actionNew}},
		{"da deletes all", "da", sessionChoice{action: actionDeleteAll}},
		{"DA is case-insensitive", "DA", sessionChoice{action: actionDeleteAll}},
		{"d1 deletes session 1", "d1", sessionChoice{action: actionDeleteOne, index: 1}},
		{"D2 is case-insensitive", "D2", sessionChoice{action: actionDeleteOne, index: 2}},
		{"d0 out of range is invalid", "d0", sessionChoice{action: actionInvalid}},
		{"d4 out of range is invalid", "d4", sessionChoice{action: actionInvalid}},
		{"plain index resumes", "2", sessionChoice{action: actionResume, index: 2}},
		{"index 0 is invalid", "0", sessionChoice{action: actionInvalid}},
		{"index out of range is invalid", "4", sessionChoice{action: actionInvalid}},
		{"garbage is invalid", "banana", sessionChoice{action: actionInvalid}},
		{"whitespace trimmed", "  1  ", sessionChoice{action: actionResume, index: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseChoice(tc.line, n)
			if got != tc.want {
				t.Errorf("parseChoice(%q, %d) = %+v, want %+v", tc.line, n, got, tc.want)
			}
		})
	}
}
