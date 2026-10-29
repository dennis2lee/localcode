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

// Windows destructive commands, shells, launchers, and privilege commands
// must not generalize to "<program> *", including when spelled with
// Windows case variations or executable extensions.
func TestAlwaysAllowWindowsDestructiveCommandsKeepExactCommand(t *testing.T) {
	tests := []string{
		"del x.obj",
		"DEL x.obj",
		"Remove-Item file.txt",
		"REMOVE-ITEM file.txt",
		"format.com C:",
		"FORMAT.COM C:",
		"erase old.log",
		"rd /s /q temp",
		"ri scratch.txt",
		"icacls C:\\data /grant Users:F",
		"takeown /f secret.txt",
		"cacls C:\\data /e /p Users:n",
		"attrib +h file.txt",
		"taskkill /PID 1234",
		"Stop-Process -Id 1234",
		"STOP-PROCESS -Id 1234",
		"spps -Id 1234",
		"start calc.exe",
		"Start-Process notepad",
		"saps notepad",
		"call script.bat",
		"Invoke-Expression 'Get-Process'",
		"iex 'Get-Process'",
		"Invoke-Command -ScriptBlock { Get-Process }",
		"icm -ScriptBlock { Get-Process }",
		"wsl ls -la",
		"mshta vbscript:Close()",
		"rundll32 user32.dll,MessageBeep",
		"regsvr32 /s mydll.dll",
		"diskpart /s script.txt",
		"Clear-Content file.txt",
	}
	for _, cmd := range tests {
		if got := PermissionRuleFor(BashToolName, cmd).Match; got != cmd {
			t.Errorf("%q -> %q, want the exact command", cmd, got)
		}
	}
}
