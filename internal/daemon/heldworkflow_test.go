package daemon

import "testing"

// "/workflow run" appends its line and its report outside any turn, the
// way "!" does, and save and delete write the workspace a running turn may
// be editing: all three wait for the turn to end. Listing and showing only
// read, and are injected like any other line.
func TestWorkflowWritesWaitForTheTurnAndReadsDoNot(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"/workflow run review src/", "/workflow"},
		{"/workflow save mine --user", "/workflow"},
		{"/workflow delete mine", "/workflow"},
		{"/workflow", ""},
		{"/workflow list", ""},
		{"/workflow show review", ""},
		{"/workflows run x", ""},
	} {
		if got := heldUntilIdle(c.text); got != c.want {
			t.Errorf("heldUntilIdle(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
