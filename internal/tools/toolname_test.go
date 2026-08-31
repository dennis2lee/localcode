package tools

import (
	"strings"
	"testing"
)

// A model that gets a tool's name slightly wrong.
//
// The case this was written for is in a screenshot: five identical calls
// to "bash.command", each answered "not available to this agent", each
// followed by the same call again. The answer named the mistake and
// nothing else, so there was nothing in it to act on.

func TestADecoratedNameResolvesToTheToolItNames(t *testing.T) {
	have := []string{"bash", "read", "edit", "grep"}
	for _, tc := range []struct{ asked, want string }{
		{"bash", "bash"},           // exact
		{"bash.command", "bash"},   // named by its own parameter
		{"functions.bash", "bash"}, // a namespace in front
		{"mcp.local.bash", "bash"}, // several
		{"Bash", "bash"},           // case
		{"BASH", "bash"},
	} {
		got, _, _ := Nearest(have, tc.asked)
		if got != tc.want {
			t.Errorf("Nearest(%q) = %q, want %q", tc.asked, got, tc.want)
		}
	}

	// Separator spelling is not a different tool.
	have2 := []string{"read_file", "write_file"}
	for _, asked := range []string{"readFile", "read-file", "ReadFile", "read file"} {
		got, exact, _ := Nearest(have2, asked)
		if got != "read_file" || exact {
			t.Errorf("Nearest(%q) = %q exact=%v, want read_file", asked, got, exact)
		}
	}
}

func TestOnlyAnExactNameIsReportedExact(t *testing.T) {
	if _, exact, _ := Nearest([]string{"bash"}, "bash"); !exact {
		t.Error("an exact name was not reported exact")
	}
	if _, exact, _ := Nearest([]string{"bash"}, "bash.command"); exact {
		t.Error("a repaired name was reported exact, so the result would not say it was repaired")
	}
}

// The guard that matters: resolution searches what the agent was offered,
// so a misspelling cannot reach past a restriction.
func TestARepairCannotReachAToolTheAgentDoesNotHave(t *testing.T) {
	readonly := []string{"read", "grep"}
	for _, asked := range []string{"bash", "bash.command", "functions.bash", "Bash"} {
		if got, _, _ := Nearest(readonly, asked); got != "" {
			t.Errorf("Nearest(%q) reached %q, which this agent was not offered", asked, got)
		}
	}
}

// Two tools it could equally mean is not a repair.
func TestAnAmbiguousNameIsNotGuessed(t *testing.T) {
	// "x.read" is not ambiguous — it names read and nothing else — so the
	// case has to be two tools that really are one name spelled two ways.
	got2, _, cands2 := Nearest([]string{"readfile", "read_file"}, "readFile")
	if got2 != "" {
		t.Errorf("resolved %q from two equal candidates", got2)
	}
	if len(cands2) != 2 {
		t.Errorf("candidates = %v, want both", cands2)
	}
}

// The refusal names the roster. Without it the model has no way to find
// out what it may call, and asks again.
func TestARefusalNamesWhatTheAgentActuallyHas(t *testing.T) {
	msg := NoSuchTool([]string{"read", "grep"}, "bash", nil)
	for _, want := range []string{`"bash"`, `"read"`, `"grep"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not name %s: %s", want, msg)
		}
	}

	// A near miss is offered by name.
	msg2 := NoSuchTool([]string{"read", "grep"}, "reed", []string{"read"})
	if !strings.Contains(msg2, "Did you mean") || !strings.Contains(msg2, `"read"`) {
		t.Errorf("no suggestion: %s", msg2)
	}

	// A long roster says how long it is rather than claiming to be all of
	// it, which would be a false statement in a message about accuracy.
	many := make([]string, 60)
	for i := range many {
		many[i] = "t" + string(rune('a'+i%26)) + itoa(i)
	}
	msg3 := NoSuchTool(many, "nope", nil)
	if !strings.Contains(msg3, "60 tools") || !strings.Contains(msg3, "begin:") {
		t.Errorf("a capped roster did not say it was capped: %s", msg3)
	}

	// No tools at all is a real configuration, and "the tools available
	// are:" followed by nothing is not an answer.
	msg4 := NoSuchTool(nil, "bash", nil)
	if !strings.Contains(msg4, "no tools at all") {
		t.Errorf("msg4 = %s", msg4)
	}
}

// A truncated name is what the streaming defect produced, and it is worth
// suggesting rather than only refusing.
func TestATruncatedNameIsOfferedItsWholeForm(t *testing.T) {
	_, _, cands := Nearest([]string{"read_file", "write_file"}, "read_")
	if len(cands) != 1 || cands[0] != "read_file" {
		t.Errorf("candidates = %v, want read_file", cands)
	}
}
