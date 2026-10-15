package userdirs

import (
	"os"
	"testing"
)

// TestMain clears XDG_CONFIG_HOME before any test runs. OpencodeConfigDir
// reads it, and these tests name directories under a temporary home: a
// runner or a developer's shell that sets it would put a real directory in
// their answers. A test about it sets its own with t.Setenv.
func TestMain(m *testing.M) {
	os.Unsetenv("XDG_CONFIG_HOME")
	os.Exit(m.Run())
}
