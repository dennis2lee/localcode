package config

import "testing"

// A config file is written on one machine and read on another, so a rule
// about the shape of a path in it has to give the same answer everywhere.
//
// filepath.IsAbs answers for the host, and that is the failure this
// guards: "/srv/mcp" is absolute on Linux and relative on Windows, so a
// cwd the config refused on one machine was accepted and joined onto the
// project on the other. Windows CI is where it was found, and these cases
// run on every platform so it does not have to be found there again.
func TestAPathIsAbsoluteInEitherConvention(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/srv/mcp", true},
		{`\srv\mcp`, true},
		{`C:\srv\mcp`, true},
		{"C:/srv/mcp", true},
		{"c:/srv", true},
		{`\\server\share\x`, true},
		{"//server/share/x", true},
		{"sub/dir", false},
		{`sub\dir`, false},
		{"./sub", false},
		{"../sub", false},
		{"", false},
		{"CONTRIBUTING.md", false},
	} {
		if got := absolutePath(tc.path); got != tc.want {
			t.Errorf("absolutePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// The same for climbing out of the project, which filepath.Clean answers
// for the host too: "..\\escape" is one segment on Linux and two on
// Windows.
func TestClimbingOutIsSeenInEitherConvention(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"..", true},
		{"../escape.md", true},
		{`..\escape.md`, true},
		{"docs/../../escape.md", true},
		{`docs\..\..\escape.md`, true},
		{"docs/../rules.md", false},
		{"docs/rules.md", false},
		{`docs\rules.md`, false},
		{"./rules.md", false},
		{"rules.md", false},
		{"", false},
	} {
		if got := escapesUpward(tc.path); got != tc.want {
			t.Errorf("escapesUpward(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// And the two rules that are built on them, stated as what a person would
// hit: the same entry is refused whichever machine reads the file.
func TestTheSameEntryIsRefusedOnEveryPlatform(t *testing.T) {
	for _, entry := range []string{"/etc/rules.md", `C:\rules.md`, "../rules.md", `..\rules.md`} {
		if why := instructionsProblem(entry); why == "" {
			t.Errorf("instructions entry %q was accepted", entry)
		}
	}
	for _, entry := range []string{"docs/rules.md", `docs\rules.md`, "*.md"} {
		if why := instructionsProblem(entry); why != "" {
			t.Errorf("instructions entry %q was refused: %s", entry, why)
		}
	}
	for _, cwd := range []string{"/srv/x", `C:\srv\x`, "C:/srv/x"} {
		sc := MCPServerConfig{Command: "echo", Cwd: cwd}
		if err := sc.Validate(); err != nil {
			t.Errorf("an absolute cwd %q was refused: %v", cwd, err)
		}
	}
	for _, cwd := range []string{"sub", `.\sub`, "../sub"} {
		sc := MCPServerConfig{Command: "echo", Cwd: cwd}
		if err := sc.Validate(); err == nil {
			t.Errorf("a relative cwd %q was accepted", cwd)
		}
	}
}
