// Package childproc keeps the processes localcode spawns from putting
// windows on the user's screen.
//
// This exists for one specific Windows behavior. The desktop build
// (localcode-gui.exe) is linked as a GUI-subsystem binary, so it has no
// console of its own — and when a process with no console starts a *console*
// application, Windows allocates a brand new console for the child, complete
// with a visible window. localcode starts console children constantly: one
// per stdio MCP server at startup, one per bash tool call, one per hook, one
// for `git rev-parse` when locating the memory directory. Left alone, the
// desktop app sprays black boxes across the screen — several at launch, then
// another on every shell command the model runs.
//
// Hide sets CREATE_NO_WINDOW so the child gets a console with no window
// attached. Nothing about how the child is used changes: localcode always
// wires a child's stdin/stdout to pipes and never expects it to interact
// with a terminal, so it has no use for a console window in the first place.
//
// On every other OS this is a no-op — Unix has no concept of a process
// spawning a terminal window — which is why it is a build-tagged package
// rather than a runtime check.
package childproc

// Call Hide immediately after building an exec.Cmd and before starting it:
//
//	cmd := exec.Command(name, args...)
//	childproc.Hide(cmd)
//
// See childproc_windows.go for the real implementation and childproc_other.go
// for the no-op.
