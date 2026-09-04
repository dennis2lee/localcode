package config

import "testing"

// "Always allow" writes a rule, and the rule has to mean what the person
// read. For most programs the useful generalization is the program:
// approving "cargo test" means cargo.
func TestAlwaysAllowGeneralizesToTheProgram(t *testing.T) {
	for cmd, want := range map[string]string{
		"cargo test":     "cargo *",
		"git status":     "git *",
		"npm run dev":    "npm *",
		"gh pr checks 1": "gh *",
		"ls":             "ls",
	} {
		if got := PermissionRuleFor(BashToolName, cmd).Match; got != want {
			t.Errorf("%q -> %q, want %q", cmd, got, want)
		}
	}
}

// It is the wrong generalization twice over. An interpreter takes its
// program as an argument, so "python3 *" is not a permission to run a
// command but a permission to run any code written later. A destructive
// command differs from its neighbours in the argument: "rm -rf build"
// and "rm -rf ~" are one rule apart under "rm *".
func TestAlwaysAllowKeepsTheWholeCommandForWideProgrms(t *testing.T) {
	for _, cmd := range []string{
		"rm -rf build",
		"python3 scripts/gen.py",
		"sudo apt install ripgrep",
		"sh -c 'make'",
		"chmod 755 bin/tool",
		"node build.js",
		"/usr/bin/sudo systemctl restart x",
		"PowerShell -File deploy.ps1",
	} {
		if got := PermissionRuleFor(BashToolName, cmd).Match; got != cmd {
			t.Errorf("%q -> %q, want the exact command", cmd, got)
		}
	}
}

// The rule the person was shown is the rule that gets written, so the
// prompt and the config cannot disagree.
func TestTheProposedRuleStillAllows(t *testing.T) {
	for _, cmd := range []string{"rm -rf build", "cargo test"} {
		r := PermissionRuleFor(BashToolName, cmd)
		if r.Decision != DecisionAllow {
			t.Errorf("%q -> decision %q", cmd, r.Decision)
		}
	}
}
