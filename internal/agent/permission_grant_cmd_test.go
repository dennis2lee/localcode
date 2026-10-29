package agent

import (
	"testing"

	"localcode/internal/shell"
)

// Under cmd.exe "%" is a substitution, and the first one: cmd expands
// %VAR% (and %VAR:~start,len%, a substring of it) before it looks for
// & | < > at all, so a variable holding "&" anywhere lends it to the line.
// A OneDrive for Business folder is "OneDrive - <organization>", and many
// organization names have "&" in them. A line with "%" is not covered by
// a grant for `git status`: cmd.exe would run it as `git log &calc`.
func TestACmdPercentSubstringIsNotCovered(t *testing.T) {
	shell.Configure("cmd")
	t.Cleanup(func() { shell.Configure("") })
	if shell.IsPOSIX() {
		t.Fatal("precondition: cmd is read as POSIX")
	}
	broker, _, _ := newPermissionTestBroker(t)
	if !callAndResolve(t, broker, "s1", "bash", "git status", true, ScopeSession) {
		t.Fatal("the first call should be allowed")
	}
	const line = "git log %OneDrive:~19,1%calc"
	if askedOrAllowed(t, broker, "s1", line) {
		t.Errorf("under cmd.exe, a session grant for `git status` ran %q without a question; "+
			"cmd expands %%VAR:~n,1%% before it parses & and |", line)
	}
}

// A grant of `"rm" *` does not cover `"rm" -rf ~` under a shell that is
// not POSIX either: fish strips quotes as sh does and runs `rm -rf ~`,
// which a config rule of `"rm" *` asks for.
func TestANonPOSIXGrantForAQuotedProgram(t *testing.T) {
	shell.Configure("fish")
	t.Cleanup(func() { shell.Configure("") })
	if shell.IsPOSIX() {
		t.Fatal("precondition: fish is read as POSIX")
	}
	const approved, next = `"rm" -rf build/`, `"rm" -rf ~`
	broker, _, _ := newPermissionTestBroker(t)
	if !callAndResolve(t, broker, "s1", "bash", approved, true, ScopeSession) {
		t.Fatal("the first call should be allowed")
	}
	if askedOrAllowed(t, broker, "s1", next) {
		t.Errorf("under fish, a session grant from %s ran %s without a question", approved, next)
	}
}
