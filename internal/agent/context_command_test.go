package agent

import (
	"context"
	"strings"
	"testing"
)

// "/context" is the payoff of the prompt registry: the question "what is
// actually in the request" answered from the record rather than by
// re-reading buildRun. It is local, so it must cost no model call, which
// is why the loop it runs on points at a port nothing is listening on.
func TestContextCommandReportsTheAssemblyWithoutAModelCall(t *testing.T) {
	loop := newFallbackLoop(t, "http://127.0.0.1:1")
	loop.SetSmartAgentEnabled(true)
	loop.WorkspaceRules = func(string) string { return "the project's own rules" }

	const sid = "s1"
	if _, err := loop.Store.CreateSessionIn(sid, "", "general-purpose", t.TempDir(), true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	// A provider call would fail against that address, so returning nil
	// is itself the evidence that no call was made.
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/context"); err != nil {
		t.Fatalf("/context reached the provider: %v", err)
	}

	out := lastMessagePartEnd(t, loop.Store, sid)
	for _, want := range []string{
		"Prompt assembly", AssetBaseSystem, AssetProjectRules,
		"project_instruction", "tokens", "estimates",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/context output is missing %q:\n%s", want, out)
		}
	}
	// Identities and sizes, never the bodies: this lands in a durable
	// transcript, and the assets include the project's own instructions.
	if strings.Contains(out, "the project's own rules") {
		t.Errorf("/context printed the project's rules into the transcript:\n%s", out)
	}
	// The short form says how many were excluded without listing them.
	if !strings.Contains(out, "/context all") {
		t.Errorf("the short form did not point at the long one:\n%s", out)
	}
}

// The long form answers "why is my thing not there", which is the
// question that actually gets asked.
func TestContextAllExplainsWhatWasLeftOut(t *testing.T) {
	loop := newFallbackLoop(t, "http://127.0.0.1:1")
	// Smart Agent off, so the bundle's own assets are the excluded ones.
	loop.SetSmartAgentEnabled(false)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/context all"); err != nil {
		t.Fatalf("/context all: %v", err)
	}

	out := lastMessagePartEnd(t, loop.Store, sid)
	if !strings.Contains(out, "Not included") {
		t.Fatalf("the long form did not list exclusions:\n%s", out)
	}
	if !strings.Contains(out, AssetTrustBoundary) || !strings.Contains(out, "smart agent is off") {
		t.Errorf("the trust boundary's absence was not explained:\n%s", out)
	}
}

// The command has to be recognized exactly, so an ordinary message that
// happens to start with the word is still a message to the model.
func TestContextCommandParsing(t *testing.T) {
	for _, tc := range []struct {
		text string
		all  bool
		ok   bool
	}{
		{"/context", false, true},
		{"  /context  ", false, true},
		{"/context all", true, true},
		{"/context excluded", true, true},
		{"/contextual switching", false, false},
		{"what does /context do", false, false},
		{"", false, false},
	} {
		all, ok := parseContextCommand(tc.text)
		if ok != tc.ok || all != tc.all {
			t.Errorf("parseContextCommand(%q) = (%v, %v), want (%v, %v)", tc.text, all, ok, tc.all, tc.ok)
		}
	}
}
