package update

import (
	"strings"
	"testing"
)

// The flags the Windows installer is started with.
//
// A test that runs on every platform for something that only happens on
// one, because the flags are the whole of what makes an update apply and
// this machine cannot run them. What it pins is the reasoning, so that
// removing a flag has to be a decision rather than a tidy-up.
func TestTheInstallerIsStartedAtBasicUI(t *testing.T) {
	args := installerArgs(`C:\Users\u\AppData\Local\localcode\updates\localcode.msi`)

	if len(args) < 2 || args[0] != "/i" {
		t.Fatalf("args = %v, want an /i install", args)
	}
	if args[1] != `C:\Users\u\AppData\Local\localcode\updates\localcode.msi` {
		t.Errorf("the package is not the second argument: %v", args)
	}

	// /qb is the fix, not a preference. At full UI the engine looks for an
	// authored MsiRMFilesInUse dialog to offer closing the processes holding the
	// files; wixl cannot author dialogs, so this package has none, and the
	// documented fallback is that nothing is shown and a reboot is
	// scheduled instead. The install then reports success and changes
	// nothing until the machine restarts.
	if !has(args, "/qb") {
		t.Error("the installer runs at full UI, where a package with no files-in-use dialog schedules a reboot instead of replacing the files")
	}

	// Not silent. Replacing the program somebody is using is not something
	// to do behind a progress bar they cannot see or cancel, and /qn would
	// also close localcode with no warning at all.
	for _, quiet := range []string{"/qn", "/quiet", "/q"} {
		if has(args, quiet) {
			t.Errorf("%s: the installer would close localcode with nothing shown", quiet)
		}
	}

	// Nothing that suppresses the restart handling: /norestart would leave
	// the files in use unresolved, which is the state this fixes.
	for _, bad := range []string{"/norestart", "/forcerestart"} {
		if has(args, bad) {
			t.Errorf("%s changes what the Restart Manager is allowed to do", bad)
		}
	}
}

func has(args []string, want string) bool {
	for _, a := range args {
		if strings.EqualFold(a, want) {
			return true
		}
	}
	return false
}
