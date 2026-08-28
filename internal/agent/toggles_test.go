package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/events"
)

// replyTo runs one command and returns what the session was told.
func replyTo(t *testing.T, l *Loop, sid, text string) string {
	t.Helper()
	if err := l.SendMessage(context.Background(), sid, "general-purpose", text); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	evs, err := l.Store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == events.TypeMessagePartEnd {
			s, _ := evs[i].Data["text"].(string)
			return s
		}
	}
	return ""
}

// A bare command flips whatever the switch currently is, which is what
// makes these toggles rather than setters: the common use is one word at
// the prompt, not a word and a state.
func TestATogglesFlipsWithoutAnArgument(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	loop.SetSmartAgentEnabled(false)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if out := replyTo(t, loop, sid, "/smart-agent"); !strings.Contains(out, "smart_agent: on") {
		t.Errorf("first toggle said %q", out)
	}
	if !loop.SmartAgentEnabled() {
		t.Error("smart agent did not turn on")
	}
	if out := replyTo(t, loop, sid, "/smart-agent"); !strings.Contains(out, "smart_agent: off") {
		t.Errorf("second toggle said %q", out)
	}
	if loop.SmartAgentEnabled() {
		t.Error("smart agent did not turn off")
	}

	// And an explicit state is honoured however many times it is given,
	// so "/smart-agent off" twice is off, not off then on.
	replyTo(t, loop, sid, "/smart-agent off")
	replyTo(t, loop, sid, "/smart-agent off")
	if loop.SmartAgentEnabled() {
		t.Error("an explicit off was treated as a flip")
	}
}

// Every one of the three, since the point of the change is that all
// three are reachable from a prompt rather than only from a window.
func TestAllThreeSwitchesHaveACommand(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if out := replyTo(t, loop, sid, "/auto-delegate on"); !strings.Contains(out, "auto_delegate: on") {
		t.Errorf("/auto-delegate said %q", out)
	}
	if !loop.AutoDelegateEnabled() {
		t.Error("auto delegate did not turn on")
	}

	if out := replyTo(t, loop, sid, "/permission-skip-all on"); !strings.Contains(out, "skip_permissions: on") {
		t.Errorf("/permission-skip-all said %q", out)
	}
	if !loop.Config.PermissionsSkipped() {
		t.Error("skip permissions did not turn on")
	}
	// It says what it did and what it did not do. "Skip permissions"
	// reads like "skip all safety" and is not: a deny still denies.
	out := replyTo(t, loop, sid, "/permission-skip-all off")
	if strings.Contains(out, "deny") {
		t.Error("turning it off repeated the warning about turning it on")
	}
	if loop.Config.PermissionsSkipped() {
		t.Error("skip permissions did not turn off")
	}
}

// A toggle that forgets is not a toggle. The settings window has always
// written these to config.json and "/config" never did, so the same
// switch survived a restart or not depending on which you had used.
func TestATogglePersistsLikeTheSettingsWindowDoes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loop := newSmartLoop(t, "http://127.0.0.1:1")
	loop.ConfigPath = path
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if out := replyTo(t, loop, sid, "/smart-agent on"); strings.Contains(out, "this run only") {
		t.Errorf("said it could not save when it had somewhere to save: %q", out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("config.json is not valid JSON after the write: %v", err)
	}
	if saved["smart_agent"] != true {
		t.Errorf("config.json = %s, want smart_agent true", raw)
	}
}

// With nowhere to save it, the change still applies and the reply says
// so. A change reported as not having happened, which did, is worse than
// one reported as unsaved.
func TestAToggleWithNoConfigFileStillAppliesAndSaysSo(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	loop.ConfigPath = ""
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	out := replyTo(t, loop, sid, "/smart-agent on")
	if !loop.SmartAgentEnabled() {
		t.Error("the change did not apply")
	}
	if !strings.Contains(out, "this run only") {
		t.Errorf("reply = %q, want it to say the change was not saved", out)
	}
}

// A word that is neither on nor off is a typo, not a flip. Treating it
// as one would turn the switch by accident.
func TestAnUnknownArgumentDoesNotFlipTheSwitch(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	loop.SetSmartAgentEnabled(false)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if out := replyTo(t, loop, sid, "/smart-agent yeah"); !strings.Contains(out, "usage:") {
		t.Errorf("reply = %q, want the usage line", out)
	}
	if loop.SmartAgentEnabled() {
		t.Error("a typo turned the switch on")
	}
}

// The list a client completes against has to be the list the router
// answers, or completion offers a command that does nothing.
func TestEverySlashCommandIsAnsweredByTheDaemon(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cmds := SlashCommands()
	if len(cmds) < 5 {
		t.Fatalf("only %d slash commands listed", len(cmds))
	}
	for _, c := range cmds {
		text := "/" + c.Name
		var answered bool
		for _, route := range loop.commandRoutes(context.Background(), sid, "general-purpose", text) {
			ok, _ := route()
			if ok {
				answered = true
				break
			}
		}
		if !answered {
			t.Errorf("%s is offered for completion and no route answers it", text)
		}
		if c.Description == "" {
			t.Errorf("%s has no description", text)
		}
	}
}

// The three toggles are in that list, since being completable is half of
// what was asked for.
func TestTheTogglesAreOfferedForCompletion(t *testing.T) {
	want := map[string]bool{"smart-agent": false, "auto-delegate": false, "permission-skip-all": false}
	for _, c := range SlashCommands() {
		if _, ok := want[c.Name]; ok {
			want[c.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("/%s is not offered for completion", name)
		}
	}
}

// A toggle typed in one client moves the switch in another, which is
// what the config.changed event is for.
func TestATogglePutsTheNewStateOnTheEventLog(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	replyTo(t, loop, sid, "/smart-agent on")

	evs, _ := loop.Store.Events(sid, 0)
	var announced bool
	for _, ev := range evs {
		if ev.Type == events.TypeConfigChanged && ev.Data["smart_agent"] == true {
			announced = true
		}
	}
	if !announced {
		t.Error("the switch changed and no client was told")
	}
}
