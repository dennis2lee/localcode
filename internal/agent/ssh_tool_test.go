package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/sshterm"
	"localcode/internal/tools"
)

// The ssh tool's offer and its refusals. A live connection is sshterm's
// to test; here the question is what the tool lets through, and under
// which rule.

func sshLoop(t *testing.T) *Loop {
	t.Helper()
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	home := t.TempDir()
	m := sshterm.NewManager(sshterm.Options{Home: home, DataDir: filepath.Join(home, "data"), LocalUser: "me"})
	loop.SSH = m
	t.Cleanup(func() { m.CloseAll("test over", true) })
	loop.Tools.Register(NewSSHTool(loop))
	return loop
}

func TestSSHIsOfferedWhileOnToATurnSomebodyCanAnswer(t *testing.T) {
	loop := sshLoop(t)
	ctx := WithSessionID(context.Background(), "s1")
	offered := func(ctx context.Context) bool { return !loop.hiddenTools(ctx)[SSHToolName] }
	loop.Config.SetSSHRuntime(false)
	if offered(ctx) {
		t.Error("offered with the setting off")
	}
	loop.Config.SetSSHRuntime(true)
	if !offered(ctx) {
		t.Error("not offered with the setting on")
	}
	// Unlike the browser, no window has to be showing the conversation:
	// a terminal the model works in does not vanish because the person
	// looked elsewhere.
	if !offered(WithSessionID(context.Background(), "s2")) {
		t.Error("not offered to another conversation")
	}
	if offered(WithUnattended(ctx)) {
		t.Error("offered to an unattended turn, where nobody answers a password")
	}
	loop.SSH = nil
	if offered(ctx) {
		t.Error("offered with no SSH manager")
	}
}

func execSSH(t *testing.T, loop *Loop, ctx context.Context, in map[string]any) tools.Result {
	t.Helper()
	b, _ := json.Marshal(in)
	return loop.Tools.Call(ctx, SSHToolName, b, "")
}

// allowAll answers every permission question yes, so a refusal in a
// test is a rule's.
func allowAll(loop *Loop) {
	reg := tools.NewRegistry(func(context.Context, tools.Ask) (bool, error) { return true, nil })
	// The rules in config decide, as cmd/localcode wires them.
	reg.Resolver = tools.ComposeResolver(
		func(ctx context.Context, toolName, subject string, static bool) tools.Decision {
			return tools.Decision(loop.Config.ResolvePermissionFor(ctx, toolName, subject, static))
		},
		NewPermissionPolicy(loop.Store, loop.Config).ToolsPolicy(),
	)
	reg.Register(NewSSHTool(loop))
	loop.Tools = reg
}

func TestADenyRuleKeepsTheModelOffAHostByEitherName(t *testing.T) {
	loop := sshLoop(t)
	allowAll(loop)
	loop.Config.SetSSHRuntime(true)
	ctx := WithSessionID(context.Background(), "s1")
	// Without the rule, the same connect goes ahead (to nowhere).
	if res := execSSH(t, loop, ctx, map[string]any{"action": "connect", "host": "127.0.0.1:1"}); res.Refused {
		t.Fatalf("refused with no rule: %+v", res)
	}
	loop.Config.Permissions = map[string]config.ToolPermission{
		config.SSHConnectToolName: {Rules: []config.PermissionRule{{Match: "*prod*", Decision: config.DecisionDeny}}},
	}
	before := len(loop.SSH.List("s1"))
	// The host as an alias, as a name, and in capitals: host names are not
	// case sensitive, and a rule written in lower case covers them.
	for _, host := range []string{"prod-db", "deploy@prod.example.com:2222", "PROD.example.com", "Prod-db"} {
		res := execSSH(t, loop, ctx, map[string]any{"action": "connect", "host": host})
		if !res.Refused || len(loop.SSH.List("s1")) != before {
			t.Fatalf("%s: %+v", host, res)
		}
	}
}

func TestADelegatedSubAgentIsNotOfferedSSH(t *testing.T) {
	loop := sshLoop(t)
	loop.Config.SetSSHRuntime(true)
	if _, err := loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Store.CreateSession("child", "parent", "general-purpose", false); err != nil {
		t.Fatal(err)
	}
	if loop.hiddenTools(WithSessionID(context.Background(), "parent"))[SSHToolName] {
		t.Fatal("hidden from the conversation")
	}
	if !loop.hiddenTools(WithSessionID(context.Background(), "child"))[SSHToolName] {
		t.Fatal("offered to a sub-agent, whose terminals no panel shows")
	}
}

type fakePrompt struct {
	pending  string
	atPrompt bool
}

func (f fakePrompt) PendingLine() (string, bool) { return f.pending, f.atPrompt }
func (f fakePrompt) Info() sshterm.Info {
	return sshterm.Info{Integrated: true, Shell: "bash"}
}

// A command the rules deny is not built at the prompt with type and key.
func TestADeniedCommandCannotBeTyped(t *testing.T) {
	loop := sshLoop(t)
	loop.Config.Permissions = map[string]config.ToolPermission{
		config.SSHToolName: {Rules: []config.PermissionRule{{Match: "rm *", Decision: config.DecisionDeny}}},
	}
	loop.Config.SetSkipPermissionsRuntime(true)
	tool := NewSSHTool(loop)
	ctx := context.Background()
	for _, c := range []struct {
		term  fakePrompt
		text  string
		enter bool
		want  bool
	}{
		{fakePrompt{"", true}, "rm -rf x", true, true},
		{fakePrompt{"", true}, "rm -rf x", false, true},
		{fakePrompt{"r", true}, "m -rf x", false, true},
		{fakePrompt{"rm -rf x", true}, "", true, true},
		{fakePrompt{"", true}, "ls\nrm -rf x", false, true},
		{fakePrompt{"", false}, "rm -rf x", true, false},
		{fakePrompt{"", true}, "ls -la", true, false},
	} {
		got := tool.deniedAsCommand(ctx, c.term, c.text, c.enter) != ""
		if got != c.want {
			t.Errorf("pending %q + %q (enter %v, at prompt %v): denied %v", c.term.pending, c.text, c.enter, c.term.atPrompt, got)
		}
	}
}

func TestTheSSHToolRefusesWhatItCannotDoAsAsked(t *testing.T) {
	loop := sshLoop(t)
	loop.Config.SetSSHRuntime(true)
	ctx := WithSessionID(context.Background(), "s1")
	for _, c := range []struct {
		in   map[string]any
		want string
	}{
		{map[string]any{"action": "run", "command": "ls"}, "no SSH terminal"},
		{map[string]any{"action": "connect"}, "needs a host"},
		{map[string]any{"action": "connect", "host": "bad host"}, "printable ASCII"},
		{map[string]any{"action": "connect", "host": "호스트"}, "printable ASCII"},
		{map[string]any{"action": "connect", "host": "h:22", "port": 23}, "port once"},
		{map[string]any{"action": "read", "terminal": "t9"}, "no terminal"},
	} {
		res := execSSH(t, loop, ctx, c.in)
		if !res.IsError || !strings.Contains(res.Content, c.want) {
			t.Errorf("%v: %q", c.in, res.Content)
		}
	}
	loop.Config.SetSSHRuntime(false)
	if res := execSSH(t, loop, ctx, map[string]any{"action": "list"}); !res.Refused {
		t.Fatalf("with SSH off: %+v", res)
	}
}

func TestTheModelCannotCloseATerminalThePersonOpenedOrHolds(t *testing.T) {
	loop := sshLoop(t)
	loop.Config.SetSSHRuntime(true)
	ctx := WithSessionID(context.Background(), "s1")
	person, err := loop.SSH.Open("s1", "127.0.0.1:1", sshterm.ByPerson)
	if err != nil {
		t.Fatal(err)
	}
	res := execSSH(t, loop, ctx, map[string]any{"action": "close", "terminal": person.ID})
	if !res.Refused || !strings.Contains(res.Content, "opened by the user") {
		t.Fatalf("closing the person's terminal: %+v", res)
	}
	mine, _ := loop.SSH.Open("s1", "127.0.0.1:1", sshterm.ByModel)
	mine.SetHold(true)
	res = execSSH(t, loop, ctx, map[string]any{"action": "close", "terminal": mine.ID})
	if !res.Refused {
		t.Fatalf("closing a held terminal: %+v", res)
	}
	mine.SetHold(false)
	if res := execSSH(t, loop, ctx, map[string]any{"action": "close", "terminal": mine.ID}); res.IsError {
		t.Fatalf("closing its own: %+v", res)
	}
}

func TestConnectSubjectNamesTheAliasTheTargetAndTheHops(t *testing.T) {
	r := sshterm.Route{
		Asked:  sshterm.Spec{Host: "prod-db"},
		Target: sshterm.Target{Host: "10.0.0.5", User: "deploy", Port: 22},
		Hops:   []sshterm.Target{{Host: "bastion", User: "me", Port: 22}},
	}
	if got := connectSubject(r); got != "prod-db deploy@10.0.0.5:22 via me@bastion:22" {
		t.Fatalf("%q", got)
	}
	r.Asked.Host = "10.0.0.5"
	r.Hops = nil
	if got := connectSubject(r); got != "deploy@10.0.0.5:22" {
		t.Fatalf("%q", got)
	}
}

// A remote command is decided as a bash line is: each segment on its
// own, so an allow for "ls *" does not run what is chained after it. And
// a line split for a shell that is not known to split it that way is not
// allowed by the split.
func TestRemoteCommandsAreDecidedSegmentBySegment(t *testing.T) {
	cfg := &config.Config{Permissions: map[string]config.ToolPermission{
		config.SSHToolName: {Rules: []config.PermissionRule{
			{Match: "ls *", Decision: config.DecisionAllow},
			{Match: "rm *", Decision: config.DecisionDeny},
		}},
	}}
	posix := config.WithRemoteShell(context.Background(), true)
	cases := []struct {
		ctx  context.Context
		cmd  string
		want config.Decision
	}{
		{posix, "ls -la", config.DecisionAllow},
		{posix, "ls -la && rm -rf ~", config.DecisionDeny},
		{posix, "ls -la; whoami", config.DecisionAsk},
		{posix, "ls $(rm x)", config.DecisionDeny},
		{context.Background(), "ls -la", config.DecisionAsk},
		{posix, "", config.DecisionAllow},
	}
	for _, c := range cases {
		if got := cfg.ResolvePermissionFor(c.ctx, config.SSHToolName, c.cmd, true); got != c.want {
			t.Errorf("%q: %s, want %s", c.cmd, got, c.want)
		}
	}
	// The grant "allow for this session" writes is the program's.
	if r := config.PermissionRuleFor(config.SSHToolName, "systemctl status nginx"); r.Match != "systemctl *" {
		t.Errorf("grant %q", r.Match)
	}
	// A deny for everything over ssh refuses the call itself.
	cfg.Permissions[config.SSHToolName] = config.ToolPermission{Rules: []config.PermissionRule{{Match: "*", Decision: config.DecisionDeny}}}
	if got := cfg.ResolvePermissionFor(context.Background(), config.SSHToolName, "", false); got != config.DecisionDeny {
		t.Errorf("ssh denied outright: %s", got)
	}
}

func TestTheSSHCommandTurnsItOnAndOff(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	closed := 0
	loop.CloseSSH = func() { closed++ }
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	loop.Config.SetSSHRuntime(false)
	if out := replyTo(t, loop, sid, "/ssh on"); !strings.Contains(out, "ssh: on") || !strings.Contains(out, "SSH panel") {
		t.Errorf("/ssh on said %q", out)
	}
	if !loop.Config.SSHLive() {
		t.Error("the setting did not turn on")
	}
	if out := replyTo(t, loop, sid, "/ssh"); !strings.Contains(out, "ssh: off") || !strings.Contains(out, "closed") {
		t.Errorf("/ssh said %q", out)
	}
	if loop.Config.SSHLive() || closed != 1 {
		t.Errorf("off left the setting %v and closed connections %d time(s); want off, once", loop.Config.SSHLive(), closed)
	}
	if out := replyTo(t, loop, sid, "/ssh maybe"); !strings.Contains(out, "usage") {
		t.Errorf("a bad argument said %q", out)
	}
}
