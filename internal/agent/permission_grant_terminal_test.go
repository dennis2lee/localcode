package agent

import (
	"context"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/tools"
)

// askedOrAllowedIn is askedOrAllowed for a line of tool, asked under ctx.
func askedOrAllowedIn(t *testing.T, ctx context.Context, broker *PermissionBroker, sessionID, tool, command string) (allowedSilently bool) {
	t.Helper()
	before, _ := broker.store.Events(sessionID, 0)
	got := make(chan bool, 1)
	go func() {
		allowed, _ := broker.Func()(WithSessionID(ctx, sessionID),
			tools.Ask{Tool: tool, Subject: command, Description: "test call"})
		got <- allowed
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case allowed := <-got:
			return allowed
		default:
		}
		evs, _ := broker.store.Events(sessionID, 0)
		for i := len(evs) - 1; i >= len(before); i-- {
			if evs[i].Type == events.TypePermissionRequest {
				id, _ := evs[i].Data["id"].(string)
				broker.Resolve(id, false, "")
				<-got
				return false
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%q was neither allowed nor asked", command)
	return false
}

// A session grant for a PowerShell line, or an SSH line for a shell that
// is not bash or zsh, covers another line only when that line is plain
// text, whole: the line is that shell's, not the host's, and the host's
// split is not that shell's. This test host's shell is POSIX, which is
// the case that let "echo a\;Remove-Item x" through: one command to a
// POSIX split, two to PowerShell, whose backslash escapes nothing.
func TestATerminalGrantIsSplitAsTheTerminalsShellSplits(t *testing.T) {
	for _, tc := range []struct {
		tool string
		ctx  context.Context
	}{
		{config.PowerShellToolName, context.Background()},
		{config.SSHToolName, config.WithRemoteShell(context.Background(), false)},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			broker, _, _ := newPermissionTestBroker(t)
			if !callAndResolve(t, broker, "s1", tc.tool, "echo a", true, ScopeSession) {
				t.Fatal("the first call should be allowed")
			}
			for _, c := range []struct {
				command string
				allowed bool
			}{
				{"echo a", true},
				{"echo b", true},
				{`echo a\;Remove-Item x`, false},
				{`echo a\|Remove-Item x`, false},
				{"echo a; Remove-Item x", false},
				{"echo (Remove-Item x)", false},
			} {
				if got := askedOrAllowedIn(t, tc.ctx, broker, "s1", tc.tool, c.command); got != c.allowed {
					t.Errorf("after a session grant for `echo a`, %q allowed without a question = %v, want %v", c.command, got, c.allowed)
				}
			}
		})
	}
	// A bash or zsh terminal splits as bash does.
	t.Run("ssh to bash", func(t *testing.T) {
		broker, _, _ := newPermissionTestBroker(t)
		if !callAndResolve(t, broker, "s1", config.SSHToolName, "echo a", true, ScopeSession) {
			t.Fatal("the first call should be allowed")
		}
		ctx := config.WithRemoteShell(context.Background(), true)
		if !askedOrAllowedIn(t, ctx, broker, "s1", config.SSHToolName, "echo a && echo b") {
			t.Error("a bash line of granted commands was asked about")
		}
	})
}
