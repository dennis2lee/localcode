package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What a command-parity review against opencode turned up, and what was
// done about it. Every test here stands for one finding.

// The finding that mattered most, because it was a regression this build
// shipped: the unknown-command guard answered for any slash nothing else
// claimed, without checking whether the name existed. Three built-ins
// take no argument, so "/init focus on tests" reached the guard with
// /init perfectly real — and was told there is no /init, directly above
// a list containing /init.
func TestAKnownCommandIsNeverCalledUnknown(t *testing.T) {
	for _, name := range []string{"/memory", "/usage"} {
		t.Run(name, func(t *testing.T) {
			loop, sid, bodies := effortLoop(t, "")

			if err := loop.SendMessage(context.Background(), sid, "boy", name+" and some argument"); err != nil {
				t.Fatalf("SendMessage: %v", err)
			}
			if n := len(bodies()); n != 0 {
				t.Fatalf("it reached the model (%d requests)", n)
			}
			reply := lastReply(t, loop, sid)
			if strings.Contains(reply, "There is no "+name) {
				t.Errorf("%s exists and was called unknown: %s", name, reply)
			}
			if !strings.Contains(reply, "does not take") {
				t.Errorf("the answer does not say the argument was refused: %s", reply)
			}
			// Still not handed to the model: a slash is somebody
			// addressing the program either way.
			if !strings.Contains(reply, "not sent to the model") {
				t.Errorf("the answer does not say it was withheld from the model: %s", reply)
			}
		})
	}
}

// A name that really is absent still gets the original answer, so
// tightening the guard did not blunt the thing it exists for.
func TestAnAbsentNameIsStillRefusedByName(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "")

	if err := loop.SendMessage(context.Background(), sid, "boy", "/clean everything"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if n := len(bodies()); n != 0 {
		t.Fatalf("it reached the model (%d requests)", n)
	}
	reply := lastReply(t, loop, sid)
	if !strings.Contains(reply, "no /clean") {
		t.Errorf("an absent command was not refused by name: %s", reply)
	}
}

// "/init <focus>" is the other half of that fix: opencode's takes
// arguments, so somebody arriving from it types them, and they used to
// land on the guard. Now they reach the model as part of the prompt.
func TestInitTakesWhatToFocusOn(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "")

	if err := loop.SendMessage(context.Background(), sid, "boy", "/init focus on the test commands"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reqs := bodies()
	if len(reqs) == 0 {
		t.Fatal("/init with an argument never reached the model")
	}
	raw, err := json.Marshal(reqs[0])
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	sent := string(raw)
	if !strings.Contains(sent, "AGENTS.md") {
		t.Errorf("the init prompt was not sent: %s", firstBytes(sent))
	}
	if !strings.Contains(sent, "focus on the test commands") {
		t.Errorf("what to focus on was dropped: %s", firstBytes(sent))
	}
}

// "/status" is the terminal's only way to see that an MCP server is
// down. Before it, that lived in a Web UI indicator and nowhere else.
func TestStatusNamesEveryMCPServerAndWhetherItWorks(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "")
	loop.MCPStates = func() []IntegrationState {
		return []IntegrationState{
			{Name: "zeta", Status: "connected"},
			{Name: "alpha", Status: "disconnected", Detail: "Connection closed"},
		}
	}

	if err := loop.SendMessage(context.Background(), sid, "boy", "/status"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if n := len(bodies()); n != 0 {
		t.Fatalf("/status reached the model (%d requests)", n)
	}
	reply := lastReply(t, loop, sid)
	for _, want := range []string{"alpha", "disconnected", "Connection closed", "zeta", "connected"} {
		if !strings.Contains(reply, want) {
			t.Errorf("/status does not report %q: %s", want, reply)
		}
	}
	// Sorted, so the same daemon does not print its servers in a
	// different order each time and read as though something moved.
	if strings.Index(reply, "alpha") > strings.Index(reply, "zeta") {
		t.Errorf("servers are not in a stable order: %s", reply)
	}
}

// With no hook wired — every build with no MCP servers, and every test —
// it says none rather than failing.
func TestStatusSurvivesHavingNoMCPAtAll(t *testing.T) {
	loop, sid, _ := effortLoop(t, "")

	if err := loop.SendMessage(context.Background(), sid, "boy", "/status"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if reply := lastReply(t, loop, sid); !strings.Contains(reply, "none configured") {
		t.Errorf("/status without a manager: %s", reply)
	}
}

// "/debug" is the block somebody pastes into a bug report.
func TestDebugNamesTheBuildAndWhereItIsPointed(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "")
	loop.Version = "9.9.9"

	if err := loop.SendMessage(context.Background(), sid, "boy", "/debug"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if n := len(bodies()); n != 0 {
		t.Fatalf("/debug reached the model (%d requests)", n)
	}
	reply := lastReply(t, loop, sid)
	for _, want := range []string{"9.9.9", "platform", sid, "workspace", "mcp"} {
		if !strings.Contains(reply, want) {
			t.Errorf("/debug does not report %q: %s", want, reply)
		}
	}
	// It must not collide with "/debug-log", which is a different
	// command and was there first.
	if strings.Contains(reply, "every model request") {
		t.Errorf("/debug answered as /debug-log: %s", reply)
	}
}

// "/debug-log" still reaches its own route rather than being swallowed by
// the shorter name registered near it.
func TestDebugLogIsNotShadowedByDebug(t *testing.T) {
	loop, sid, _ := effortLoop(t, "")

	if err := loop.SendMessage(context.Background(), sid, "boy", "/debug-log"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reply := lastReply(t, loop, sid)
	if strings.Contains(reply, "Paste this into a bug report") {
		t.Errorf("/debug-log was answered by /debug: %s", reply)
	}
}

// "/workspace" is what a terminal had no way to do at all: the directory
// a conversation works in was reachable only from the Web UI's button.
func TestWorkspaceReportsAndMoves(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "")
	dir := t.TempDir()

	if err := loop.SendMessage(context.Background(), sid, "boy", "/workspace"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if n := len(bodies()); n != 0 {
		t.Fatalf("/workspace reached the model (%d requests)", n)
	}
	if reply := lastReply(t, loop, sid); !strings.Contains(reply, "workspace:") {
		t.Errorf("/workspace did not report the directory: %s", reply)
	}

	if err := loop.SendMessage(context.Background(), sid, "boy", "/workspace "+dir); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if reply := lastReply(t, loop, sid); !strings.Contains(reply, dir) {
		t.Errorf("/workspace <path> did not report the new directory: %s", reply)
	}
	// And the move actually took, rather than only being announced.
	if got := loop.SessionDir(sid); got != dir {
		t.Errorf("session directory = %q, want %q", got, dir)
	}
}

// A path that is not a directory changes nothing. The failure worth
// guarding is a move that half-happens: announced, and not made.
func TestWorkspaceRefusesWhatIsNotADirectory(t *testing.T) {
	loop, sid, _ := effortLoop(t, "")
	before := loop.SessionDir(sid)

	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, arg := range []string{file, filepath.Join(t.TempDir(), "does-not-exist")} {
		if err := loop.SendMessage(context.Background(), sid, "boy", "/workspace "+arg); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		if reply := lastReply(t, loop, sid); !strings.Contains(reply, "workspace unchanged") {
			t.Errorf("%s was not refused: %s", arg, reply)
		}
		if got := loop.SessionDir(sid); got != before {
			t.Errorf("a refused move still changed the directory to %q", got)
		}
	}
}

// ResolveWorkspace is shared with the Web UI's handler so a path the
// button takes and the command refuses cannot be two answers to one
// question. Tilde expansion is here because it is what people type.
func TestResolveWorkspaceExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	got, err := ResolveWorkspace("~")
	if err != nil {
		t.Fatalf("ResolveWorkspace(~): %v", err)
	}
	if got != home {
		t.Errorf("ResolveWorkspace(~) = %q, want %q", got, home)
	}
	if _, err := ResolveWorkspace(""); err == nil {
		t.Error("an empty path was accepted")
	}
}

// firstBytes keeps a failure message readable when the thing that failed
// is a whole request body.
func firstBytes(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}
