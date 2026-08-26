package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/hooks"
)

// The lifecycle points that are not a tool call.
//
// A tool hook can see everything a tool does and nothing else, which
// leaves the expensive half of an agent session unobservable and
// ungovernable: the model call itself, the decision to hand work to a
// sub-agent, the switch to another model when one will not answer.

// writeHook returns a shell command that appends its stdin to a file, and
// the path it appends to.
func writeHook(t *testing.T) (command, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "hook.log")
	return "cat >> " + path, path
}

func hookLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func TestAPreModelHookCanRefuseTheRequest(t *testing.T) {
	srv, recorded := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)
	loop.Config.Hooks = hooks.Config{
		hooks.EventPreModel: {{Command: `echo '{"decision":"block","reason":"not on this machine"}'`}},
	}

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello")
	if err == nil || !strings.Contains(err.Error(), "not on this machine") {
		t.Fatalf("the turn was not refused: %v", err)
	}
	if n := len(recorded()); n != 0 {
		t.Errorf("%d requests reached the model despite the block", n)
	}
}

// "before model: context inject" — the hook hands text back and it goes
// into the request. This is what makes pre_model an injection point
// rather than only a veto.
func TestAPreModelHookCanInjectContext(t *testing.T) {
	srv, recorded := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)
	loop.Config.Hooks = hooks.Config{
		hooks.EventPreModel: {{Command: `echo '{"context":"The deploy freeze is on until Friday."}'`}},
	}

	sendOne(t, loop, "s1", "general-purpose")

	reqs := recorded()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	if !strings.Contains(reqs[0].system, "deploy freeze is on until Friday") {
		t.Error("the injected context did not reach the model")
	}
}

// The one governance point a prompt cannot provide. "No agent of mine
// spawns another" is a rule about capability, and it is enforced where the
// capability is exercised.
func TestADelegateHookCanRefuseASubAgent(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)
	loop.Config.Hooks = hooks.Config{
		hooks.EventDelegate: {{Command: `echo '{"decision":"block","reason":"delegation is off here"}'`}},
	}
	tasks := NewTaskManager(context.Background(), loop, 2)

	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := tasks.SpawnSync(context.Background(), "s1", "explore", "find something")
	if err == nil || !strings.Contains(err.Error(), "delegation is off here") {
		t.Fatalf("the delegation was not refused: %v", err)
	}
	if kids := loop.Store.Children("s1"); len(kids) != 0 {
		t.Errorf("%d child sessions were created for a refused delegation", len(kids))
	}
}

// A retry hook is how "page me when we drop to the backup model" is
// written. It fires after the decision, because the decision is not a
// hook's to make: a model it does not want is a pre_model matter.
func TestARetryHookSeesTheModelSwitch(t *testing.T) {
	quickRetries(t)
	srv, _ := failingServer(t, 503, "service unavailable", 3)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)
	cmd, path := writeHook(t)
	loop.Config.Hooks = hooks.Config{hooks.EventRetry: {{Command: cmd}}}

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	logged := hookLog(t, path)
	if !strings.Contains(logged, "claude-opus-5") || !strings.Contains(logged, "qwen3-coder-30b") {
		t.Errorf("the retry hook was not told which way the switch went: %q", logged)
	}
}

func TestEveryNewLifecycleEventIsAcceptedByConfig(t *testing.T) {
	for _, event := range []string{
		hooks.EventPreModel, hooks.EventPostModel,
		hooks.EventDelegate, hooks.EventCompact, hooks.EventRetry,
	} {
		if !hooks.KnownEvents[event] {
			t.Errorf("%q is fired by the loop but config validation would reject it", event)
		}
	}
}
