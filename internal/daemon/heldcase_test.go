package daemon

import "testing"

// A command held while a turn runs is held in any case, because the
// router reads commands in any case: "/REWIND" typed during a turn is
// "/rewind", not text for the turn.
func TestHeldCommandsAreHeldInAnyCase(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"/REWIND", "/rewind"},
		{"/Clear", "/clear"},
		{"/Redo", "/redo"},
		{"/Workspace ../other", "/workspace"},
	} {
		if got := heldUntilIdle(c.text); got != c.want {
			t.Errorf("heldUntilIdle(%q) = %q, want %q: the router runs it when idle", c.text, got, c.want)
		}
	}
}
