package daemon

import "testing"

// "/workspace <path>" joined the commands that wait for an idle session.
//
// It moves the directory relative paths resolve against, so a turn that
// was mid-tool-call would finish somewhere other than where it started —
// the same hazard "/clear" and "/rewind" are held for, arriving from a
// different direction.
func TestWorkspaceIsHeldOnlyWhenItWouldMove(t *testing.T) {
	for _, c := range []struct {
		text string
		want string
	}{
		{"/workspace /tmp/elsewhere", "/workspace"},
		{"/workspace ~/work/thing", "/workspace"},
		// Bare, it reads the directory back. Refusing a read because a
		// turn is running would be a refusal with nothing behind it.
		{"/workspace", ""},
		{"/workspace   ", ""},
		// The two that were already held stay held.
		{"/clear", "/clear"},
		{"/rewind", "/rewind"},
		// And an ordinary prompt is not a command at all.
		{"what does the workspace command do", ""},
		{"/status", ""},
	} {
		if got := heldUntilIdle(c.text); got != c.want {
			t.Errorf("heldUntilIdle(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
