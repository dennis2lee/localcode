package shell

import "testing"

// Whether a named shell is treated as POSIX decides whether a bash
// permission rule means what it appears to mean: internal/config splits a
// command at POSIX operators and trusts an allow only where that split is
// the shell's own. Anything not on the list is not POSIX, which is the
// safe direction — it makes localcode ask more often rather than less.
func TestAConfiguredShellIsPOSIXOnlyByName(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/bin/bash", true}, {"bash", true}, {"C:\\Program Files\\Git\\bin\\bash.EXE", true},
		{"/opt/homebrew/bin/fish", false}, {"pwsh", false}, {"cmd.exe", false},
	} {
		got := resolve("linux", tc.path, nil, nil, nil)
		if got.posix != tc.want {
			t.Errorf("resolve(configured=%q).posix = %v, want %v", tc.path, got.posix, tc.want)
		}
	}
}
