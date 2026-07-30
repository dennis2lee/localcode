package childproc

import (
	"os/exec"
	"testing"
)

func TestHideIsSafeOnNilAndRepeatable(t *testing.T) {
	Hide(nil) // must not panic

	cmd := exec.Command("echo", "hi")
	Hide(cmd)
	Hide(cmd) // idempotent: calling twice must not corrupt SysProcAttr
	if cmd.Path == "" {
		t.Error("Hide damaged the command")
	}
}
