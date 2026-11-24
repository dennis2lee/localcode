package config

import (
	"encoding/base64"
	"testing"
	"unicode/utf16"
)

// A deny rule for powershell is found wherever PowerShell starts a
// command, not only where bash would, and in what it hands to another
// interpreter. Each line below runs New-Item in
// PowerShell, and each was asked about (and, under skip_permissions,
// run) under a deny rule "New-Item *" while it was split as a bash line.
func TestAPowerShellDenyIsFoundWherePowerShellStartsACommand(t *testing.T) {
	c := &Config{Permissions: map[string]ToolPermission{
		PowerShellToolName: {Rules: []PermissionRule{{Match: "New-Item *", Decision: DecisionDeny}}},
	}}
	for _, line := range []string{
		`New-Item marker`,
		`Write-Output safe; New-Item marker`,
		`Write-Output safe\; New-Item marker`,
		`Write-Output safe\| New-Item marker`,
		`Write-Output "a\"; New-Item marker"`,
		`1 | % {New-Item marker}`,
		`1 | ForEach-Object { New-Item marker }`,
		`Write-Output (New-Item marker)`,
		`& {New-Item marker}`,
		`. { New-Item marker }`,
		`Write-Output "$(New-Item marker)"`,
		`Write-Output @(New-Item marker)`,
		`Invoke-Command -ScriptBlock {New-Item marker}`,
		`if ($true) {New-Item marker}`,
		`$x = New-Item marker`,
		`[void](New-Item marker)`,
		`[void] (New-Item marker)`,
		`return New-Item marker`,
		`-not (New-Item marker)`,
		`try { New-Item marker } finally { }`,
		"Write-Output a\nNew-Item marker",
		`new-item marker`,
		`NEW-ITEM marker`,
		`Write-Output a && new-item marker`,
		`New-item marker`,
		`echo a\& New-Item marker`,
		`echo "C:\"; New-Item marker`,
		`cd "C:\work\"; New-Item marker`,
		"Ne`w-Item marker",
		`. New-Item marker`,
		`& 'New-Item' marker`,
		`iex "New-Item marker"`,
		`Invoke-Expression 'New-Item marker'`,
		`Invoke-Expression -Command "New-Item marker"`,
		`powershell -c "New-Item marker"`,
		`powershell.exe -NoProfile -Command "New-Item marker"`,
		`pwsh -Command New-Item marker`,
		`powershell "New-Item marker"`,
		`cmd /c "New-Item marker"`,
		`powershell -EncodedCommand ` + encodedForTest("New-Item marker"),
		`pwsh -ec ` + encodedForTest("Write-Output a; New-Item marker"),
		`powershell -ExecutionPolicy Bypass -Command "New-Item marker"`,
		`powershell -NoProfile -ExecutionPolicy Bypass "New-Item marker"`,
		`Invoke-Expression -ErrorAction Stop "New-Item marker"`,
		`cmd /s /c "New-Item marker"`,
		"Write-Output a``; New-Item marker",
		`powershell -ExecutionPolicy Bypass -EncodedCommand ` + encodedForTest("New-Item marker"),
		`pwsh -WorkingDirectory . -Command "New-Item marker"`,
		`iex -Verbose "New-Item marker"`,
		`iex ' New-Item marker'`,
		`powershell -Command " New-Item marker"`,
		`cmd /c " New-Item marker"`,
		`iex -Command:"New-Item marker"`,
		`powershell -Command:"New-Item marker"`,
		`pwsh -ec:` + encodedForTest("New-Item marker"),
		`cmd /c"powershell New-Item marker"`,
		`powershell.exe /EncodedCommand ` + encodedForTest("New-Item marker"),
		`& "C:\Program Files\PowerShell\7\pwsh.exe" -Command "New-Item marker"`,
		`Microsoft.PowerShell.Management\New-Item marker`,
		`>$null New-Item marker`,
		`> $null New-Item marker`,
		"iex \"Write-Output a`nNew-Item marker\"",
		"iex \"New-Item`tmarker\"",
		`powershell -EncodedCommand ` + encodedForTest("New-Item`\nmarker"),
		`powershell -EncodedCommand ` + encodedForTest(".\tNew-Item marker"),
		`powershell -EncodedCommand ` + encodedForTest("return\tNew-Item marker"),
		`Write-Output 'New-Item marker' | iex`,
		"Write-Output \u201ca\u201d; New-Item marker",
	} {
		if got := c.ResolvePermission(PowerShellToolName, line, true); got != DecisionDeny {
			t.Errorf("%q: %s, want deny", line, got)
		}
	}
	// Lines that do not run it are not denied.
	for _, line := range []string{`Get-ChildItem`, `Write-Output New`, `Get-Item marker`,
		`git log --grep=New-Item --oneline`, `go test -run=New-Item ./...`} {
		if got := c.ResolvePermission(PowerShellToolName, line, true); got == DecisionDeny {
			t.Errorf("%q: denied", line)
		}
	}
}

func encodedForTest(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, r := range u {
		b[2*i], b[2*i+1] = byte(r), byte(r>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// "Allow for this session" on a dot-sourced script or a module is that
// line, not every line that starts with a dot: ". *" covered ". New-Item
// marker". Both run code the line names.
func TestADotSourcedLineIsGrantedAsWritten(t *testing.T) {
	for _, line := range []string{`. .\helper.ps1`, `Import-Module .\tools.psm1`, `ipmo .\tools.psm1`} {
		if rule := PermissionRuleFor(PowerShellToolName, line); rule.Match != line {
			t.Errorf("the rule %q grants: %+v", line, rule)
		}
	}
}

func TestPowerShellPieces(t *testing.T) {
	for line, want := range map[string][]string{
		`a; b | c`:               {"a", "b", "c"},
		`x (y) {z}`:              {"x", "y", "z"},
		`$v = [int] (Get-Thing)`: {"Get-Thing"},
		`return Get-Thing`:       {"Get-Thing"},
		`. ./script.ps1`:         {"./script.ps1"},
		`Write-Output "a;b"`:     {`Write-Output "a`, `b"`},
	} {
		got := powershellPieces(line)
		if len(got) != len(want) {
			t.Errorf("%q: %q, want %q", line, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q: %q, want %q", line, got, want)
				break
			}
		}
	}
}

// A rule that is a wildcard alone keeps deciding a line as split for
// bash: the pieces a PowerShell line is cut into are not all commands,
// and "oneline" of --format=oneline matched "*" and was denied.
func TestAWildcardDenyDoesNotDenyThePiecesOfALine(t *testing.T) {
	c := &Config{Permissions: map[string]ToolPermission{
		PowerShellToolName: {Rules: []PermissionRule{
			{Match: "*", Decision: DecisionDeny},
			{Match: "git *", Decision: DecisionAllow},
		}},
	}}
	for _, line := range []string{`git log --format=oneline`, `git diff --color=never`, `git log --format="%h (%s)"`} {
		if got := c.ResolvePermission(PowerShellToolName, line, true); got == DecisionDeny {
			t.Errorf("%q: denied", line)
		}
	}
	for _, line := range []string{`Get-ChildItem`, `git status; Get-ChildItem`} {
		if got := c.ResolvePermission(PowerShellToolName, line, true); got != DecisionDeny {
			t.Errorf("%q: %s, want deny", line, got)
		}
	}
}
