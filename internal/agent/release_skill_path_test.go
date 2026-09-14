package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/skills"
	"localcode/internal/tools"
)

// newSkillPathLoop wires a Loop the way the daemon does for reads: a
// registry with read_file, a resolver carrying the workspace boundary,
// and the broker that asks. A bare NewRegistry(nil) would let an outside
// file through without ever asking, the way an unwired read_file does.
// The model is the in-process scripted provider, so no test here needs
// the network.
func newSkillPathLoop(t *testing.T, p provider.Provider, project string) (*Loop, *session.Store, *PermissionBroker) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"},
		},
		Profiles: map[string]config.Profile{
			"balanced": {Provider: "local", Model: "test-model"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "balanced"},
		},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	broker := NewPermissionBroker(store)
	policy := NewPermissionPolicy(store, cfg)
	broker.SetPolicy(policy, func(string) {})
	registry := tools.NewRegistry(broker.Func())
	registry.Resolver = tools.ComposeResolver(
		func(ctx context.Context, toolName, subject string, static bool) tools.Decision {
			return tools.Decision(cfg.ResolvePermissionFor(ctx, toolName, subject, static))
		},
		policy.ToolsPolicy(),
	)
	registry.Register(tools.ReadFile{})

	loop := New(store, registry, map[string]provider.Provider{"local": p}, cfg)
	loop.Skills = []skills.Skill{
		{Name: "pdf-tools", Description: "Work with PDF files", Body: "# PDF Tools\nMerge and split PDFs."},
	}
	loop.SetProjectDir(project)
	return loop, store, broker
}

// newNilRegistrySkillPathLoop wires a Loop with no tool registry at all:
// the nil-registry fallback in runSkillPath decides inside-vs-outside on
// its own, because there is nothing to ask with.
func newNilRegistrySkillPathLoop(t *testing.T, p provider.Provider, project string) (*Loop, *session.Store) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"},
		},
		Profiles: map[string]config.Profile{
			"balanced": {Provider: "local", Model: "test-model"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "balanced"},
		},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	loop := New(store, nil, map[string]provider.Provider{"local": p}, cfg)
	loop.Skills = []skills.Skill{
		{Name: "pdf-tools", Description: "Work with PDF files", Body: "# PDF Tools\nMerge and split PDFs."},
	}
	loop.SetProjectDir(project)
	return loop, store
}

// scriptedReply answers one turn with fixed text, so what the model was
// sent is what the test asserts on.
func scriptedReply(text string) *scriptedProvider {
	return &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventTextDelta, TextDelta: text},
			{Type: provider.EventMessageStop, StopReason: "end_turn"},
		},
	}}
}

// sentText is everything the model was sent across all its requests.
func sentText(p *scriptedProvider) string {
	var b strings.Builder
	for _, req := range p.sentRequests() {
		for _, m := range req.Messages {
			for _, blk := range m.Content {
				b.WriteString(blk.Text)
			}
		}
	}
	return b.String()
}

// sentSources collects the source tags on the blocks the model was sent,
// which is where the skill.frame origin lands.
func sentSources(p *scriptedProvider) []string {
	var out []string
	for _, req := range p.sentRequests() {
		for _, m := range req.Messages {
			for _, blk := range m.Content {
				if blk.Source != "" {
					out = append(out, blk.Source)
				}
			}
		}
	}
	return out
}

func writeSkillFile(t *testing.T, path, frontmatter, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\n" + frontmatter + "\n---\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
}

func lastLocalError(t *testing.T, store *session.Store, sid string) string {
	t.Helper()
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == events.TypeError {
			s, _ := evs[i].Data["error"].(string)
			return s
		}
	}
	return ""
}

func permissionRequests(t *testing.T, store *session.Store, sid string, from int) []map[string]any {
	t.Helper()
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var out []map[string]any
	for i := from; i < len(evs); i++ {
		if evs[i].Type == events.TypePermissionRequest {
			out = append(out, evs[i].Data)
		}
	}
	return out
}

// A registered name wins over a file of the same spelling: the ambiguity
// resolves toward the installed skill, not an arbitrary file.
func TestARegisteredNameWinsOverASameSpelledFile(t *testing.T) {
	project := t.TempDir()
	p := scriptedReply("done.")
	loop, store, _ := newSkillPathLoop(t, p, project)

	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	loop.Skills = append(loop.Skills, skills.Skill{Name: "thing.md", Description: "installed", Body: "REGISTERED BODY"})
	writeSkillFile(t, filepath.Join(project, "thing.md"), "name: thing\ndescription: from file", "FILE BODY")

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/skill thing.md"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := sentText(p); !strings.Contains(got, "REGISTERED BODY") {
		t.Errorf("registered skill did not win; model was sent: %s", got)
	}
	if got := sentText(p); strings.Contains(got, "FILE BODY") {
		t.Errorf("the file overrode the registered skill: %s", got)
	}
}

// A path outside any skills directory runs: the user is pointing at a
// file, and only the file's own parse decides whether it is a skill.
func TestASkillPathRunsOutsideAnySkillsDirectory(t *testing.T) {
	project := t.TempDir()
	p := scriptedReply("done.")
	loop, store, _ := newSkillPathLoop(t, p, project)

	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	writeSkillFile(t, filepath.Join(project, "notes", "scratch.md"),
		"name: scratch\ndescription: pointed at directly", "SCRATCH BODY FROM A PATH")

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/skill notes/scratch.md merge this"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := sentText(p); !strings.Contains(got, "SCRATCH BODY FROM A PATH") {
		t.Errorf("skill file body was not sent to the model: %s", got)
	}
	if got := sentText(p); !strings.Contains(got, "merge this") {
		t.Errorf("trailing arguments were dropped: %s", got)
	}
	// The same origin a registered skill runs under, naming the file.
	want := "skill.frame." + filepath.Join(project, "notes", "scratch.md")
	var saw bool
	for _, s := range sentSources(p) {
		if s == want {
			saw = true
		}
	}
	if !saw {
		t.Errorf("no block carried the one-shot origin %q (saw %q)", want, sentSources(p))
	}
}

// ~ expands to home and a relative path resolves against the session's
// workspace, the same claim a tool makes about a relative path.
func TestSkillPathsExpandTildeAndResolveRelative(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	p := scriptedReply("done.")
	loop, store, _ := newSkillPathLoop(t, p, project)

	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	// Home is outside the workspace; the read_outside switch authorizes
	// it up front, so neither turn below can block on a question.
	yes := true
	if err := loop.Permissions.Set(sid, session.SwitchReadOutside, &yes); err != nil {
		t.Fatalf("allow outside reads: %v", err)
	}
	writeSkillFile(t, filepath.Join(home, "fromhome.md"),
		"name: fromhome\ndescription: home file", "HOME BODY")
	writeSkillFile(t, filepath.Join(project, "rel.md"),
		"name: rel\ndescription: workspace file", "RELATIVE BODY")

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/skill ~/fromhome.md"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := sentText(p); !strings.Contains(got, "HOME BODY") {
		t.Errorf("~/ did not expand to home: %s", got)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/skill ./rel.md"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := sentText(p); !strings.Contains(got, "RELATIVE BODY") {
		t.Errorf("./ did not resolve against the workspace: %s", got)
	}
}

// A skill run from a path is not registered: the next turn does not have
// it, /skill does not list it, and completion has nothing new to offer.
func TestASkillRunFromAPathIsNotRegistered(t *testing.T) {
	project := t.TempDir()
	p := scriptedReply("done.")
	loop, store, _ := newSkillPathLoop(t, p, project)

	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	writeSkillFile(t, filepath.Join(project, "oneshot.md"),
		"name: oneshot\ndescription: one turn only", "ONESHOT BODY")

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/skill ./oneshot.md"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := sentText(p); !strings.Contains(got, "ONESHOT BODY") {
		t.Fatalf("the one-shot skill did not run: %s", got)
	}

	if got := len(loop.SkillList()); got != 1 {
		t.Errorf("SkillList holds %d skills after a path run, want 1", got)
	}
	listing := replyTo(t, loop, sid, "/skill")
	if !strings.Contains(listing, "pdf-tools") {
		t.Errorf("/skill no longer lists the installed skill: %q", listing)
	}
	if strings.Contains(listing, "oneshot") {
		t.Errorf("/skill lists the one-shot file, so it leaked into the registry: %q", listing)
	}
	for _, n := range loop.knownCommandNames() {
		if strings.Contains(n, "oneshot") {
			t.Errorf("the one-shot file is completable as %q", n)
		}
	}
}

// Every failure names the path that failed and what was wrong, the way
// the unknown-skill message names the available skills.
func TestSkillPathFailuresNameThePath(t *testing.T) {
	project := t.TempDir()
	loop, store, _ := newSkillPathLoop(t, scriptedReply("done."), project)

	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	unreadable := filepath.Join(project, "locked.md")
	writeSkillFile(t, unreadable, "name: locked\ndescription: locked", "LOCKED BODY")
	unreadableOK := true
	if err := os.Chmod(unreadable, 0); err == nil {
		if _, err := os.ReadFile(unreadable); err == nil {
			unreadableOK = false // root reads through permissions; nothing to assert here
		}
	}

	emptyBody := filepath.Join(project, "empty.md")
	writeSkillFile(t, emptyBody, "name: empty\ndescription: no body", "")
	noFrontmatter := filepath.Join(project, "plain.md")
	if err := os.WriteFile(noFrontmatter, []byte("just some text"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	subdir := filepath.Join(project, "adir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cases := []struct {
		name string
		text string
		want []string
	}{
		{"missing", "/skill ./nope/missing.md", []string{"./nope/missing.md", "pdf-tools"}},
		{"directory", "/skill ./adir", []string{"./adir", "directory", "pdf-tools"}},
		{"empty body", "/skill ./empty.md", []string{"./empty.md", "no usable body", "pdf-tools"}},
		{"no frontmatter", "/skill ./plain.md", []string{"./plain.md", "pdf-tools"}},
	}
	if unreadableOK {
		cases = append(cases, struct {
			name string
			text string
			want []string
		}{"unreadable", "/skill ./locked.md", []string{"./locked.md", "pdf-tools"}})
	}
	for _, tc := range cases {
		if err := loop.SendMessage(context.Background(), sid, "general-purpose", tc.text); err != nil {
			t.Fatalf("%s: SendMessage: %v", tc.name, err)
		}
		msg := lastLocalError(t, store, sid)
		for _, w := range tc.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%s: error %q does not name %q", tc.name, msg, w)
			}
		}
	}
}

// A file inside the workspace runs with no prompt: it is a file in the
// project, like any other.
func TestASkillInsideTheWorkspaceRunsWithNoPrompt(t *testing.T) {
	project := t.TempDir()
	p := scriptedReply("done.")
	loop, store, _ := newSkillPathLoop(t, p, project)

	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	writeSkillFile(t, filepath.Join(project, "inside.md"),
		"name: inside\ndescription: in the project", "INSIDE BODY")

	before, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/skill ./inside.md"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := sentText(p); !strings.Contains(got, "INSIDE BODY") {
		t.Errorf("the inside skill did not run: %s", got)
	}
	if got := permissionRequests(t, store, sid, len(before)); len(got) != 0 {
		t.Errorf("an inside file raised %d permission question(s); it should ask nothing", len(got))
	}
}

// A file outside the workspace raises the outside-read question: the
// prompt names the path and says its contents will become instructions.
// Approved once, the turn runs.
func TestASkillOutsideTheWorkspaceAsksAndRunsOnceApproved(t *testing.T) {
	project := t.TempDir()
	other := t.TempDir()
	p := scriptedReply("done.")
	loop, store, broker := newSkillPathLoop(t, p, project)

	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	outside := filepath.Join(other, "outside.md")
	writeSkillFile(t, outside, "name: outside\ndescription: elsewhere", "OUTSIDE BODY")

	before, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- loop.SendMessage(context.Background(), sid, "general-purpose", "/skill "+outside)
	}()
	id := waitForPermissionID(t, store, sid, len(before))

	var question map[string]any
	for _, q := range permissionRequests(t, store, sid, len(before)) {
		question = q
	}
	if question == nil {
		t.Fatal("no permission question was shown for an outside skill file")
	}
	desc, _ := question["description"].(string)
	if !strings.Contains(desc, outside) {
		t.Errorf("the question does not name the path: %q", desc)
	}
	if !strings.Contains(desc, "instructions") {
		t.Errorf("the question does not say the file becomes instructions: %q", desc)
	}
	if outside, _ := question["outside"].(string); outside != "read" {
		t.Errorf("the question is not an outside-read one (outside=%q)", outside)
	}

	broker.Resolve(id, true, ScopeOutsideDir)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the turn stayed blocked after its directory was approved")
	}
	if got := sentText(p); !strings.Contains(got, "OUTSIDE BODY") {
		t.Errorf("the approved skill did not run: %s", got)
	}
}

// Saying no is the whole point of asking. A declined skill file must not
// become instructions, and the conversation must be told why rather than
// left with a turn that quietly did nothing.
//
// The approving test above passes whether or not the refusal is read, so
// this is the one that pins the answer actually being consulted.
func TestADeclinedSkillFileDoesNotBecomeInstructions(t *testing.T) {
	project := t.TempDir()
	other := t.TempDir()
	p := scriptedReply("done.")
	loop, store, broker := newSkillPathLoop(t, p, project)

	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	outside := filepath.Join(other, "outside.md")
	writeSkillFile(t, outside, "name: outside\ndescription: elsewhere", "OUTSIDE BODY")

	before, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- loop.SendMessage(context.Background(), sid, "general-purpose", "/skill "+outside)
	}()
	id := waitForPermissionID(t, store, sid, len(before))

	broker.Resolve(id, false, ScopeOnce)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the turn stayed blocked after the question was declined")
	}

	if p.sentCount() != 0 {
		t.Errorf("a declined skill file was still sent to the model")
	}
	if got := sentText(p); strings.Contains(got, "OUTSIDE BODY") {
		t.Errorf("the declined skill body reached the model: %s", got)
	}
	if msg := lastLocalError(t, store, sid); !strings.Contains(msg, outside) {
		t.Errorf("the refusal does not name the declined path: %q", msg)
	}
}

// A remembered directory is not asked twice: the second file under it
// runs with no new question, the way read_file behaves.
func TestAnApprovedSkillDirectoryIsNotAskedTwice(t *testing.T) {
	project := t.TempDir()
	other := t.TempDir()
	p := scriptedReply("done.")
	loop, store, broker := newSkillPathLoop(t, p, project)

	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	first := filepath.Join(other, "first.md")
	second := filepath.Join(other, "second.md")
	writeSkillFile(t, first, "name: first\ndescription: first", "FIRST BODY")
	writeSkillFile(t, second, "name: second\ndescription: second", "SECOND BODY")

	before, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- loop.SendMessage(context.Background(), sid, "general-purpose", "/skill "+first)
	}()
	id := waitForPermissionID(t, store, sid, len(before))
	broker.Resolve(id, true, ScopeOutsideDir)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first turn stayed blocked after approval")
	}

	mid, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/skill "+second); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := sentText(p); !strings.Contains(got, "SECOND BODY") {
		t.Errorf("the second skill in an approved directory did not run: %s", got)
	}
	if got := permissionRequests(t, store, sid, len(mid)); len(got) != 0 {
		t.Errorf("an approved directory asked again (%d question(s))", len(got))
	}
}

// A turn nobody is watching cannot answer, so an outside skill file is
// refused rather than silently allowed.
func TestAnUnattendedSkillPathIsRefused(t *testing.T) {
	project := t.TempDir()
	other := t.TempDir()
	p := scriptedReply("done.")
	loop, store, _ := newSkillPathLoop(t, p, project)

	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	outside := filepath.Join(other, "unattended.md")
	writeSkillFile(t, outside, "name: unattended\ndescription: elsewhere", "UNATTENDED BODY")

	restore := SetUnattendedWait(0)
	defer restore()
	if err := loop.SendMessage(WithUnattended(context.Background()), sid, "general-purpose", "/skill "+outside); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if msg := lastLocalError(t, store, sid); !strings.Contains(msg, "nobody is watching") {
		t.Errorf("the refusal does not say nobody answered: %q", msg)
	}
	if p.sentCount() != 0 {
		t.Errorf("the outside skill reached the model unattended without approval")
	}
}

// A skill file only becomes instructions through the boundary that asks
// about it. Without the registry that enforces that boundary there is
// nothing to ask with, so nothing runs — inside the workspace or out.
//
// Production always wires a registry, which is exactly why this is a
// refusal rather than a special case: a gate a differently-assembled
// Loop can step around is not a gate. The rule has to hold for the
// shape of the Loop, not for the shape production happens to build.
func TestWithoutTheBoundaryNoFileBecomesInstructions(t *testing.T) {
	project := t.TempDir()
	other := t.TempDir()

	outside := filepath.Join(other, "outside.md")
	writeSkillFile(t, outside, "name: outside\ndescription: elsewhere", "OUTSIDE BODY")

	for _, tc := range []struct {
		name string
		arg  string
		body string
	}{
		{"outside the workspace", outside, "OUTSIDE BODY"},
		{"inside the workspace", "./inside.md", "INSIDE BODY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := scriptedReply("done.")
			loop, store := newNilRegistrySkillPathLoop(t, p, project)
			const sid = "s1"
			if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
				t.Fatalf("create session: %v", err)
			}
			writeSkillFile(t, filepath.Join(project, "inside.md"),
				"name: inside\ndescription: in the project", "INSIDE BODY")

			before, err := store.Events(sid, 0)
			if err != nil {
				t.Fatalf("events: %v", err)
			}
			if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/skill "+tc.arg); err != nil {
				t.Fatalf("SendMessage: %v", err)
			}
			msg := lastLocalError(t, store, sid)
			if !strings.Contains(msg, tc.arg) {
				t.Errorf("the refusal does not name the path: %q", msg)
			}
			if !strings.Contains(msg, "nothing to ask") {
				t.Errorf("the refusal does not say there was nothing to ask with: %q", msg)
			}
			if p.sentCount() != 0 {
				t.Errorf("the file reached the model with nobody asked")
			}
			if got := sentText(p); strings.Contains(got, tc.body) {
				t.Errorf("the skill body reached the model: %s", got)
			}
			if got := permissionRequests(t, store, sid, len(before)); len(got) != 0 {
				t.Errorf("raised %d permission question(s) with nothing to ask with", len(got))
			}
		})
	}
}
