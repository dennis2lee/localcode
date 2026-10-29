package agent

import (
	"testing"

	"localcode/internal/shell"
)

// Under a shell that is not POSIX, a session grant covers only a line made
// of plain characters. fish and PowerShell run a command in parentheses
// inside an argument, so a grant for `ls -la` must not run `ls (rm -rf ~)`
// there without a question.
func TestANonPOSIXGrantDoesNotCoverASubstitution(t *testing.T) {
	for _, sh := range []string{"fish", "pwsh"} {
		t.Run(sh, func(t *testing.T) {
			shell.Configure(sh)
			t.Cleanup(func() { shell.Configure("") })
			if shell.IsPOSIX() {
				t.Fatalf("precondition: %s is read as POSIX", sh)
			}
			broker, _, _ := newPermissionTestBroker(t)
			if !callAndResolve(t, broker, "s1", "bash", "ls -la", true, ScopeSession) {
				t.Fatal("the first call should be allowed")
			}
			for _, line := range []string{"ls (rm -rf ~)", "ls @(Remove-Item -Recurse -Force ~)"} {
				if askedOrAllowed(t, broker, "s1", line) {
					t.Errorf("under %s, a session grant for `ls -la` ran %q without a question", sh, line)
				}
			}
		})
	}
}
