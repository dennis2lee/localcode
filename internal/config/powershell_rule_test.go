package config

import (
	"encoding/base64"
	"testing"
	"unicode/utf16"
)

// A deny rule for powershell is found wherever PowerShell starts a
// command, not only where bash would. Each line below runs New-Item in
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
	} {
		if got := c.ResolvePermission(PowerShellToolName, line, true); got != DecisionDeny {
			t.Errorf("%q: %s, want deny", line, got)
		}
	}
	// Lines that do not run it are not denied.
	for _, line := range []string{`Get-ChildItem`, `Write-Output New`, `Get-Item marker`} {
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

// "Allow for this session" on a dot-sourced script is that line, not
// every line that starts with a dot: ". *" covered ". New-Item marker".
func TestADotSourcedLineIsGrantedAsWritten(t *testing.T) {
	if rule := PermissionRuleFor(PowerShellToolName, `. .\helper.ps1`); rule.Match != `. .\helper.ps1` {
		t.Errorf("the rule a dot-sourced line grants: %+v", rule)
	}
}

func TestPowerShellPieces(t *testing.T) {
	for line, want := range map[string][]string{
		`a; b | c`:               {"a", "b", "c"},
		`x (y) {z}`:              {"x", "y", "z"},
		`$v = [int] (Get-Thing)`: {"v", "Get-Thing"},
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
