package config

import (
	"context"
	"testing"
)

// bashRequiresPermission is what the bash tool actually passes as its own
// hardcoded default (tools.Bash.RequiresPermission). Writing false here
// instead would make every unmatched segment allowed, which is not the
// question these tests are asking.
const bashRequiresPermission = true

// An allow decided by splitting a command at POSIX operators is only
// sound where the shell reads them the POSIX way.
//
// The case that matters is not hypothetical and does not need the new
// key to reach it: on a Windows machine with no sh installed, commands
// run under cmd.exe, where a backslash escapes nothing and & still
// separates. splitShellSegments reads the backslash as an escape, so
// `git status \& rm -rf /` is one segment, matches a "git *" rule, and
// runs as two commands.
func TestAnAllowIsNotTrustedUnderANonPOSIXShell(t *testing.T) {
	cfg := &Config{Permissions: map[string]ToolPermission{
		"bash": {Rules: []PermissionRule{{Match: "git *", Decision: DecisionAllow}}},
	}}
	ctx := context.Background()

	// The command the splitter reads as one piece and cmd.exe reads as two.
	const twoUnderCmd = `git status \& rm -rf /`

	if got := cfg.resolveShellCommandUnder(ctx, twoUnderCmd, bashRequiresPermission, true); got != DecisionAllow {
		t.Errorf("under a POSIX shell the rule means what it says: got %q, want %q", got, DecisionAllow)
	}
	if got := cfg.resolveShellCommandUnder(ctx, twoUnderCmd, bashRequiresPermission, false); got != DecisionAsk {
		t.Errorf("under a shell that splits differently the allow was still trusted: got %q, want %q", got, DecisionAsk)
	}
}

// A deny does not depend on the split being right — it needs one piece to
// match rather than all of them — so it is unchanged.
func TestADenyIsUnchangedUnderANonPOSIXShell(t *testing.T) {
	cfg := &Config{Permissions: map[string]ToolPermission{
		"bash": {Rules: []PermissionRule{{Match: "*rm -rf*", Decision: DecisionDeny}}},
	}}
	ctx := context.Background()
	for _, posix := range []bool{true, false} {
		if got := cfg.resolveShellCommandUnder(ctx, "rm -rf /tmp/x", bashRequiresPermission, posix); got != DecisionDeny {
			t.Errorf("posix=%v: got %q, want %q", posix, got, DecisionDeny)
		}
	}
}

// And skip_permissions does not reach this one. It downgrades a prompt
// somebody found noisy; this prompt is the difference between having read
// the command and not.
func TestSkipPermissionsDoesNotTrustANonPOSIXSplit(t *testing.T) {
	skip := true
	cfg := &Config{
		SkipPermissions: &skip,
		Permissions: map[string]ToolPermission{
			"bash": {Rules: []PermissionRule{{Match: "git *", Decision: DecisionAllow}}},
		},
	}
	if got := cfg.resolveShellCommandUnder(context.Background(), `git status \& rm -rf /`, bashRequiresPermission, false); got != DecisionAsk {
		t.Errorf("got %q, want %q even with skip_permissions on", got, DecisionAsk)
	}
}

// The ordinary case, which is every machine with a POSIX shell: nothing
// about permission resolution changes.
func TestAPOSIXShellResolvesExactlyAsBefore(t *testing.T) {
	cfg := &Config{Permissions: map[string]ToolPermission{
		"bash": {Rules: []PermissionRule{{Match: "git *", Decision: DecisionAllow}}},
	}}
	ctx := context.Background()
	for _, tc := range []struct {
		command string
		want    Decision
	}{
		{"git status", DecisionAllow},
		{"git status && git log", DecisionAllow},
		{"git status && rm -rf ~", DecisionAsk},
		{"git status > out.txt", DecisionAsk},
	} {
		if got := cfg.resolveShellCommandUnder(ctx, tc.command, bashRequiresPermission, true); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.command, got, tc.want)
		}
	}
}
