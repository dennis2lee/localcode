package main

import (
	"os"
	"testing"
)

// TestMain clears XDG_CONFIG_HOME before any test runs. opencode's global
// directory, one of the roots skills and commands are read from, is found
// through it (see userdirs.OpencodeConfigDir), and these tests read those
// roots under a temporary home. A runner or a developer's shell that sets it
// would hand the tests a real directory of somebody's skills.
func TestMain(m *testing.M) {
	os.Unsetenv("XDG_CONFIG_HOME")
	os.Exit(m.Run())
}
