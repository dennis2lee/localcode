package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/events"
	"localcode/internal/session"
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

	// Per session as of v0.63.0: the switch is a sentence about a
	// project, and flipping it for one task used to flip it for the
	// window editing something that mattered.
	if out := replyTo(t, loop, sid, "/permission-skip-all on"); !strings.Contains(out, "skip_all: on") {
		t.Errorf("/permission-skip-all said %q", out)
	}
	if !loop.Permissions.OnFor(sid, session.SwitchSkipAll) {
		t.Error("skip permissions did not turn on for this session")
	}
	if loop.Config.PermissionsSkipped() {
		t.Error("a session's own answer was written into the daemon default")
	}
	// It says what it did and what it did not do. "Skip permissions"
	// reads like "skip all safety" and is not: a deny still denies.
	out := replyTo(t, loop, sid, "/permission-skip-all off")
	if strings.Contains(out, "deny") {
		t.Error("turning it off repeated the warning about turning it on")
	}
	if loop.Permissions.OnFor(sid, session.SwitchSkipAll) {
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

// "/keep-going" flips the carry-on switch and says its scope, which is
// the part worth a sentence: the switch is daemon-wide and the feature
// is one family's.
func TestKeepGoingCommandTogglesAndStatesItsScope(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	out := replyTo(t, loop, sid, "/keep-going off")
	if !strings.Contains(out, "keep_going: off") {
		t.Errorf("reply = %q", out)
	}
	if loop.KeepGoingEnabled() {
		t.Error("the switch did not turn off")
	}
	if !strings.Contains(out, "muse") {
		t.Errorf("the reply does not state the muse-only scope: %q", out)
	}

	// No configured profile runs a muse model here, and turning it on
	// should say that rather than read as a change that took effect.
	out = replyTo(t, loop, sid, "/keep-going on")
	if !strings.Contains(out, "no configured profile currently runs a muse model") {
		t.Errorf("reply = %q, want the no-muse-profile note", out)
	}
}

// "/auto-compact 70" sets the threshold and turns the feature on: the
// number is somebody asking for compaction at 70%, and honouring it while
// leaving the switch off would honour the letter and not the request.
func TestAutoCompactTakesAThreshold(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	loop.SetAutoCompactEnabled(false)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	out := replyTo(t, loop, sid, "/auto-compact 70")
	if !strings.Contains(out, "70%") {
		t.Errorf("reply = %q", out)
	}
	if !loop.AutoCompactEnabled() || loop.CompactPercent() != 70 {
		t.Errorf("enabled=%v percent=%d, want on at 70", loop.AutoCompactEnabled(), loop.CompactPercent())
	}

	// A bare toggle keeps the threshold.
	replyTo(t, loop, sid, "/auto-compact")
	replyTo(t, loop, sid, "/auto-compact")
	if loop.CompactPercent() != 70 {
		t.Errorf("toggling changed the threshold to %d", loop.CompactPercent())
	}

	// A threshold that cannot mean anything is refused with the reason.
	out = replyTo(t, loop, sid, "/auto-compact 5")
	if !strings.Contains(out, "between 10 and 95") {
		t.Errorf("reply = %q", out)
	}
	if loop.CompactPercent() != 70 {
		t.Errorf("a refused threshold still changed the setting to %d", loop.CompactPercent())
	}
}

// The default threshold is 50, which is what the command's own reply and
// the automatic behaviour both key on.
func TestAutoCompactDefaultsToFiftyPercent(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	if got := loop.CompactPercent(); got != 50 {
		t.Errorf("default threshold = %d, want 50", got)
	}
}

// The reset commands answer even in a build with nothing wired, because
// a command that is offered for completion and then silently does
// nothing is worse than one that says why.
func TestResetCommandsSayWhenNothingIsWired(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if out := replyTo(t, loop, sid, "/reset-mcp"); !strings.Contains(out, "restart") {
		t.Errorf("/reset-mcp with no hook: %q", out)
	}
	if out := replyTo(t, loop, sid, "/reset-skills"); !strings.Contains(out, "restart") {
		t.Errorf("/reset-skills with no hook: %q", out)
	}

	// And with hooks wired, the hook's own report is the answer.
	loop.ReloadSkills = func() (string, error) { return "skills reloaded: 2 (a, b)", nil }
	if out := replyTo(t, loop, sid, "/reset-skills"); !strings.Contains(out, "skills reloaded: 2") {
		t.Errorf("/reset-skills with a hook: %q", out)
	}
}
