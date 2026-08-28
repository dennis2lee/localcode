package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// Leaving the project is a question about a place, and these are the two
// sizes it is answered at: this directory, or anywhere. Neither is
// something the old prompt could express — it offered "once" and "for the
// session", and "for the session" on a write_file path grants a tool
// rule, not a place.

// askOutside runs one boundary question through the broker and answers it
// with the given scope, returning whether the call was allowed.
func askOutside(t *testing.T, broker *PermissionBroker, sessionID string, class tools.OutsideClass, subject, dir string, allow bool, scope string) bool {
	t.Helper()
	ctx := WithSessionID(context.Background(), sessionID)
	before, err := broker.store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	result := make(chan bool, 1)
	go func() {
		ok, _ := broker.Func()(ctx, tools.Ask{
			Tool: "read_file", Subject: subject, Description: "read " + subject,
			Outside: class, Dir: dir, Workspace: "/project",
		})
		result <- ok
	}()
	id := waitForPermissionID(t, broker.store, sessionID, len(before))
	broker.Resolve(id, allow, scope)
	return <-result
}

// noAsk asserts that a call is allowed without a question being raised at
// all: the memory is checked before anything reaches the log, so an
// approved directory produces no prompt and no event.
func noAsk(t *testing.T, broker *PermissionBroker, sessionID string, class tools.OutsideClass, subject, dir string) bool {
	t.Helper()
	before, err := broker.store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	done := make(chan bool, 1)
	go func() {
		ok, _ := broker.Func()(WithSessionID(context.Background(), sessionID), tools.Ask{
			Tool: "read_file", Subject: subject, Description: "read " + subject,
			Outside: class, Dir: dir, Workspace: "/project",
		})
		done <- ok
	}()
	select {
	case ok := <-done:
		after, _ := broker.store.Events(sessionID, 0)
		if len(after) != len(before) {
			t.Errorf("a remembered directory still wrote %d event(s); it should ask nothing at all", len(after)-len(before))
		}
		return ok
	case <-time.After(2 * time.Second):
		t.Fatal("the call blocked, so it raised a question about a directory that was already approved")
		return false
	}
}

func TestApprovingADirectoryStopsTheQuestionForEverythingUnderIt(t *testing.T) {
	broker, _, _ := newPermissionTestBroker(t)
	other := "/Users/me/other-project"

	if !askOutside(t, broker, "s1", tools.OutsideRead, filepath.Join(other, "main.go"), other, true, ScopeOutsideDir) {
		t.Fatal("the approval did not allow the call it was given for")
	}
	// The tree, not the one file and not the one level. A model told to
	// read a sibling project reads forty files in it.
	for _, p := range []string{
		filepath.Join(other, "main.go"),
		filepath.Join(other, "README.md"),
		filepath.Join(other, "internal", "deep", "x.go"),
	} {
		if !noAsk(t, broker, "s1", tools.OutsideRead, p, filepath.Dir(p)) {
			t.Errorf("%s asked again after its directory was approved", p)
		}
	}
}

func TestApprovingADirectoryForReadingSaysNothingAboutWriting(t *testing.T) {
	broker, _, _ := newPermissionTestBroker(t)
	other := "/Users/me/other-project"
	askOutside(t, broker, "s1", tools.OutsideRead, filepath.Join(other, "main.go"), other, true, ScopeOutsideDir)

	// The two halves are separate because they are not the same risk.
	// This one must still ask, so the call blocks until it is answered.
	if askOutside(t, broker, "s1", tools.OutsideWrite, filepath.Join(other, "main.go"), other, false, ScopeOnce) {
		t.Error("a write was allowed by an approval given for reading")
	}
}

func TestApprovingADirectoryDoesNotLeakIntoAnotherConversation(t *testing.T) {
	broker, store, _ := newPermissionTestBroker(t)
	if _, err := store.CreateSession("s2", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	other := "/Users/me/other-project"
	askOutside(t, broker, "s1", tools.OutsideRead, filepath.Join(other, "main.go"), other, true, ScopeOutsideDir)

	// A different conversation is a different project and a different
	// decision. Answering "no" here proves it was asked.
	if askOutside(t, broker, "s2", tools.OutsideRead, filepath.Join(other, "main.go"), other, false, ScopeOnce) {
		t.Error("one conversation's approved directory allowed another conversation's read")
	}
}

// A background task works in its parent's project on work the parent
// authorized, so a directory the parent approved is one the task may use.
// Asking again would put the question in a log nobody is reading.
func TestATaskInheritsItsParentsApprovedDirectories(t *testing.T) {
	broker, store, _ := newPermissionTestBroker(t)
	if _, err := store.CreateSession("t1", "s1", "general-purpose", false); err != nil {
		t.Fatalf("create task session: %v", err)
	}
	other := "/Users/me/other-project"
	askOutside(t, broker, "s1", tools.OutsideRead, filepath.Join(other, "main.go"), other, true, ScopeOutsideDir)

	if !noAsk(t, broker, "t1", tools.OutsideRead, filepath.Join(other, "lib.go"), other) {
		t.Error("a background task was asked about a directory its parent had already approved")
	}
}

// "Yes, anywhere" turns this conversation's own switch on. Through the
// session rather than into a private map, so the answer is visible
// afterwards: an approval only the broker knew about would be a setting
// with no off switch and nowhere to see it.
func TestAllowingAnywhereTurnsTheSwitchOn(t *testing.T) {
	broker, store, _ := newPermissionTestBroker(t)
	cfg := &config.Config{}
	policy := NewPermissionPolicy(store, cfg)
	announced := 0
	broker.SetPolicy(policy, func(string) { announced++ })

	if policy.OnFor("s1", session.SwitchReadOutside) {
		t.Fatal("reading outside was allowed before anybody said so")
	}
	askOutside(t, broker, "s1", tools.OutsideRead, "/anywhere/x.go", "/anywhere", true, ScopeOutsideAll)

	if !policy.OnFor("s1", session.SwitchReadOutside) {
		t.Error("answering \"anywhere\" left the switch off, so the decision is invisible and cannot be undone")
	}
	if policy.OnFor("s1", session.SwitchWriteOutside) {
		t.Error("allowing reads anywhere also allowed writes")
	}
	if announced == 0 {
		t.Error("nothing was told the switch moved, so an open settings window still shows it off")
	}
}

// mem-clear is its own retraction. Somebody who approved one directory by
// mistake should not have to change a switch they never touched.
func TestForgettingApprovedDirectories(t *testing.T) {
	broker, _, _ := newPermissionTestBroker(t)
	other := "/Users/me/other-project"
	askOutside(t, broker, "s1", tools.OutsideRead, filepath.Join(other, "main.go"), other, true, ScopeOutsideDir)

	if got := broker.RememberedOutside("s1", tools.OutsideRead); len(got) != 1 {
		t.Fatalf("remembered %v, want the one directory that was approved", got)
	}
	if n := broker.ForgetOutside("s1", tools.OutsideRead); n != 1 {
		t.Errorf("mem-clear forgot %d, want 1", n)
	}
	if askOutside(t, broker, "s1", tools.OutsideRead, filepath.Join(other, "main.go"), other, false, ScopeOnce) {
		t.Error("a forgotten directory was still granting access")
	}
}

// The four switches belong to the conversation. Flipping one for a
// throwaway experiment used to flip it for the window editing something
// that mattered.
func TestTheSwitchesArePerConversation(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err := store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	yes := true
	cfg := &config.Config{}
	policy := NewPermissionPolicy(store, cfg)

	if err := policy.Set("a", session.SwitchSkipAll, &yes); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !policy.OnFor("a", session.SwitchSkipAll) {
		t.Error("the conversation that set it does not have it")
	}
	if policy.OnFor("b", session.SwitchSkipAll) {
		t.Error("a switch set in one conversation reached another")
	}

	// A conversation with no answer of its own follows config.json, and
	// unset is a real third state: clearing goes back to following the
	// default rather than pinning whatever it happens to be today.
	cfg.SkipToolPermissions = &yes
	if !policy.OnFor("b", session.SwitchSkipTools) {
		t.Error("a conversation with no answer of its own ignored the daemon default")
	}
	no := false
	if err := policy.Set("b", session.SwitchSkipTools, &no); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if policy.OnFor("b", session.SwitchSkipTools) {
		t.Error("an explicit off was overridden by the default it disagrees with")
	}
	if err := policy.Set("b", session.SwitchSkipTools, nil); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !policy.OnFor("b", session.SwitchSkipTools) {
		t.Error("clearing the session's answer did not go back to the default")
	}
}

// A task follows the conversation that started it, live rather than
// copied at spawn: the direction that matters is turning a switch off,
// and that has to reach work already running.
func TestATaskFollowsItsParentsSwitches(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.CreateSession("task", "parent", "explore", false); err != nil {
		t.Fatalf("create task session: %v", err)
	}
	policy := NewPermissionPolicy(store, &config.Config{})

	yes := true
	policy.Set("parent", session.SwitchReadOutside, &yes)
	if !policy.OnFor("task", session.SwitchReadOutside) {
		t.Error("a task did not inherit the conversation that started it")
	}
	if _, from := policy.Effective("task", session.SwitchReadOutside); from != SourceParent {
		t.Errorf("source = %q, want %q so a client can say where the answer came from", from, SourceParent)
	}

	no := false
	policy.Set("parent", session.SwitchReadOutside, &no)
	if policy.OnFor("task", session.SwitchReadOutside) {
		t.Error("turning it off in the parent did not reach the task already running under it")
	}
}

// The whole thing, wired the way the daemon wires it: a real registry
// with the real resolver and the real broker, a model that asks to read
// a file in another project, and the question that produces.
//
// The layers each have their own test above; this is the one that would
// catch them being connected wrongly, which is the failure that leaves
// every unit test green and the guard doing nothing.
func TestATurnReadingOutsideTheProjectAsksAndRemembers(t *testing.T) {
	project := t.TempDir()
	other := t.TempDir()
	outsideFile := filepath.Join(other, "notes.md")
	if err := os.WriteFile(outsideFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	// skip_tools on and nothing else, which is the example this feature
	// was specified with: tool prompts are gone and leaving the project
	// still asks.
	yes := true
	cfg.SkipToolPermissions = &yes

	broker := NewPermissionBroker(store)
	policy := NewPermissionPolicy(store, cfg)
	broker.SetPolicy(policy, func(string) {})
	reg := tools.NewRegistry(broker.Func())
	reg.Resolver = tools.ComposeResolver(
		func(ctx context.Context, toolName, subject string, static bool) tools.Decision {
			return tools.Decision(cfg.ResolvePermissionFor(ctx, toolName, subject, static))
		},
		policy.ToolsPolicy(),
	)
	reg.Register(tools.ReadFile{})

	call := func(path string) chan tools.Result {
		out := make(chan tools.Result, 1)
		go func() {
			ctx := WithSessionID(context.Background(), sid)
			ctx = tools.WithWorkingDir(ctx, project)
			input, _ := json.Marshal(map[string]string{"path": path})
			out <- reg.Call(ctx, "read_file", input, "")
		}()
		return out
	}

	// Inside the project: skip_tools means no question at all.
	select {
	case res := <-call(filepath.Join(project, "anything.md")):
		if res.Refused {
			t.Error("a read inside the project was refused under skip_tools")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a read inside the project asked a question under skip_tools")
	}

	// Outside it: asked, and the question says why and where.
	first := call(outsideFile)
	id := waitForPermissionID(t, store, sid, 0)
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var outside, dir string
	for _, ev := range evs {
		if ev.Type == events.TypePermissionRequest {
			outside, _ = ev.Data["outside"].(string)
			dir, _ = ev.Data["outside_dir"].(string)
		}
	}
	if outside != "read" {
		t.Errorf("the request does not say it is a boundary question (outside=%q)", outside)
	}
	if dir == "" {
		t.Error("the request carries no directory, so \"allow this directory\" has nothing to cover")
	}
	broker.Resolve(id, true, ScopeOutsideDir)
	if res := <-first; res.Refused {
		t.Fatalf("the approved read was refused: %s", res.Content)
	}

	// And the next file in the same directory is not asked about again.
	before := len(mustEvents(t, store, sid))
	select {
	case res := <-call(filepath.Join(other, "second.md")):
		_ = res // the file does not exist; what matters is that nothing asked
	case <-time.After(2 * time.Second):
		t.Fatal("a second file in an approved directory asked again")
	}
	if n := len(mustEvents(t, store, sid)) - before; n != 0 {
		t.Errorf("a second file in an approved directory wrote %d event(s); it should ask nothing", n)
	}
}

func mustEvents(t *testing.T, store *session.Store, id string) []events.Event {
	t.Helper()
	evs, err := store.Events(id, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return evs
}
