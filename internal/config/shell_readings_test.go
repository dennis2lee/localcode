package config

import (
	"slices"
	"testing"
)

// A command hidden behind a quote that the shell does not read as one.
//
// Each line below was one command to the splitter and was allowed by the
// built-in "git *" rule with nothing configured; sh on macOS (bash) ran
// the second command in each. Checked by running them: the touch
// happened every time.
func TestAQuoteTheShellDoesNotReadHidesNoCommand(t *testing.T) {
	hidden := map[string]string{
		// $'...' escapes its quote with a backslash; a plain '...' does not.
		`git log $'\'' && touch pwned`: "touch pwned",
		// A quote in a comment opens nothing.
		"git status --short # '\ntouch pwned\n'": "touch pwned",
		// Nor does one in a here-document's body.
		"git log -1 <<EOF\ngit '\nEOF\ntouch pwned\n'": "touch pwned",
	}
	cfg := &Config{}
	deny := &Config{Permissions: map[string]ToolPermission{
		BashToolName: {Rules: []PermissionRule{{Match: "touch *", Decision: DecisionDeny}}},
	}}
	for command, second := range hidden {
		if got := cfg.ResolvePermission(BashToolName, command, true); got == DecisionAllow {
			t.Errorf("ResolvePermission(bash, %q) = allow under the built-in git rule; the shell also runs %q", command, second)
		}
		if got := deny.ResolvePermission(BashToolName, command, true); got != DecisionDeny {
			t.Errorf("ResolvePermission(bash, %q) = %v with touch denied, want deny", command, got)
		}
		segments, ok := grantSegmentsUnder(command, true)
		if ok && !slices.Contains(segments, second) {
			t.Errorf("grantSegmentsUnder(%q) = %q; a grant for git would cover %q", command, segments, second)
		}
	}
}

// The extra readings are taken only when the construct is there, and do
// not turn ordinary lines into prompts.
func TestOrdinaryQuotingIsStillAllowed(t *testing.T) {
	cfg := &Config{}
	for _, command := range []string{
		`git commit -m "fix #12"`,
		`git commit -m "fix(tui): a; b"`,
		`git log --format='%H %s'`,
		`git show $'HEAD'`,
		`git log --grep 'a#b'`,
	} {
		if got := cfg.ResolvePermission(BashToolName, command, true); got != DecisionAllow {
			t.Errorf("ResolvePermission(bash, %q) = %v, want allow", command, got)
		}
	}
}

// A parenthesis the posix reading thinks is quoted, and $'...' does not.
func TestAParenthesisAfterAnAnsiCQuoteIsUnsafe(t *testing.T) {
	command := `git log $'a\'b' a(e:'touch pwned':)`
	if !hasUnquotedParen(command) {
		t.Errorf("hasUnquotedParen(%q) = false; under zsh the qualifier runs touch", command)
	}
	if got := (&Config{}).ResolvePermission(BashToolName, command, true); got == DecisionAllow {
		t.Errorf("ResolvePermission(bash, %q) = allow", command)
	}
}

// An escaped or quoted "$" does not start $'...': the quote after it is
// an ordinary one, which ends at the next quote, so the ";" is live.
func TestOnlyABareDollarStartsAnAnsiCQuote(t *testing.T) {
	for _, command := range []string{`echo \$'a\' ; b'`, `echo "$"'a\' ; b'`} {
		runes := []rune(command)
		bare := bareRunes(runes, ansiCQuoting)
		semicolon := false
		for i, r := range runes {
			if r == ';' && bare[i] {
				semicolon = true
			}
		}
		if !semicolon {
			t.Errorf("bareRunes(%q, ansiC) read the ; as quoted, as if the $ were bare", command)
		}
	}
}
