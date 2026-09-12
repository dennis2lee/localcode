package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/client"
	"localcode/internal/commands"
	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/skills"
	"localcode/internal/tools"
)

// releaseTestHarness runs a real daemon and an HTTP test client connected to it,
// backed by a mock model server that counts requests so tests can assert that
// reporting commands are answered locally and never reach the model.
type releaseTestHarness struct {
	daemon    *Daemon
	httpSrv   *httptest.Server
	client    *client.Client
	modelReqs *atomic.Int32
	session   session.Session
	evCh      <-chan events.Event
	wsDir     string
	cfgPath   string
}

func newReleaseTestHarness(t *testing.T) *releaseTestHarness {
	t.Helper()

	var modelReqs atomic.Int32
	modelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelReqs.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	t.Cleanup(modelSrv.Close)

	wsDir := t.TempDir()
	cfgPath := filepath.Join(wsDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)

	broker := agent.NewPermissionBroker(store)
	broker.ConfigPath = cfgPath
	registry := tools.NewRegistry(broker.Func())
	registry.Register(tools.WriteFile{})
	registry.Register(tools.Glob{})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelSrv.URL},
		},
		Profiles: map[string]config.Profile{
			"balanced": {Provider: "local", Model: "test-model"},
			"fast":     {Provider: "local", Model: "quick-model"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "balanced"},
			"plan":            {Profile: "balanced", Description: "Read-only planning.", Tools: []string{"glob"}},
			"build":           {Profile: "fast", Description: "Implements changes."},
		},
		DefaultProfile:     "balanced",
		MaxConcurrentTasks: 2,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	providers := map[string]provider.Provider{
		"local": provider.NewOpenAICompat(modelSrv.URL, ""),
	}
	loop := agent.New(store, registry, providers, cfg)
	loop.Version = "0.82.0"
	loop.ProjectDir = wsDir
	loop.ConfigPath = cfgPath
	tasks := agent.NewTaskManager(context.Background(), loop, cfg.MaxConcurrentTasks)

	d := New(loop, broker, tasks, nil, nil, "0.82.0")
	httpSrv := httptest.NewServer(d.Handler())
	t.Cleanup(httpSrv.Close)

	c := client.New(httpSrv.URL)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	evCtx, cancelEv := context.WithCancel(ctx)
	t.Cleanup(cancelEv)

	evCh, err := c.SubscribeEvents(evCtx, sess.ID, 0)
	if err != nil {
		t.Fatalf("SubscribeEvents: %v", err)
	}

	return &releaseTestHarness{
		daemon:    d,
		httpSrv:   httpSrv,
		client:    c,
		modelReqs: &modelReqs,
		session:   sess,
		evCh:      evCh,
		wsDir:     wsDir,
		cfgPath:   cfgPath,
	}
}

// sendCommand sends a command over HTTP to the daemon and collects the local
// reply string emitted in events.TypeMessagePartEnd once events.TypeTurnDone arrives.
// It also verifies that the mock model was never contacted.
func (h *releaseTestHarness) sendCommand(t *testing.T, cmd string) string {
	t.Helper()
	h.modelReqs.Store(0)

	ctx := context.Background()
	if err := h.client.SendMessage(ctx, h.session.ID, cmd); err != nil {
		t.Fatalf("SendMessage(%q): %v", cmd, err)
	}

	var reply string
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-h.evCh:
			if !ok {
				t.Fatalf("events channel closed while waiting for command %q", cmd)
			}
			if ev.Type == events.TypeMessagePartEnd {
				if txt, ok := ev.Data["text"].(string); ok {
					reply = txt
				}
			}
			if ev.Type == events.TypeTurnDone {
				if reqs := h.modelReqs.Load(); reqs != 0 {
					t.Fatalf("command %q reached the model (%d requests)", cmd, reqs)
				}
				return reply
			}
		case <-timeout:
			t.Fatalf("timed out waiting for command %q to complete", cmd)
		}
	}
}

// "/status" names every configured MCP server with its state, and the
// skills, custom commands, agents and workspace beside them — answered
// here rather than at the model, so a server that is down is visible from
// a terminal at all.
func TestStatusReportsEverythingAttached(t *testing.T) {
	h := newReleaseTestHarness(t)

	h.daemon.Loop.MCPStates = func() []agent.IntegrationState {
		return []agent.IntegrationState{
			{Name: "sqlite", Status: "connected"},
			{Name: "github", Status: "disconnected", Detail: "connection refused"},
		}
	}
	h.daemon.Loop.SetSkills([]skills.Skill{
		{Name: "deploy-helper", Description: "deploys stuff"},
	}, "")
	h.daemon.Loop.Commands = []commands.Command{
		{Name: "run-tests", Description: "runs test suite"},
	}

	reply := h.sendCommand(t, "/status")

	for _, want := range []string{
		"MCP servers",
		"sqlite", "connected",
		"github", "disconnected", "connection refused",
		"Skills (1)", "deploy-helper",
		"Custom commands (1)", "run-tests",
		"Agents (2)", "balanced → test-model", "fast → quick-model",
		"Workspace: " + h.wsDir,
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("/status reply missing %q:\n%s", want, reply)
		}
	}
}

// "/debug" is the block somebody pastes into a bug report: version,
// platform, session, agent, model, workspace, config, and the counts. And
// it is "/debug" that answers it, not "/debug-log", which shares a prefix.
func TestDebugReportsWhatThisBuildIs(t *testing.T) {
	h := newReleaseTestHarness(t)

	h.daemon.Loop.MCPStates = func() []agent.IntegrationState {
		return []agent.IntegrationState{
			{Name: "sqlite", Status: "connected"},
			{Name: "github", Status: "disconnected"},
		}
	}

	reply := h.sendCommand(t, "/debug")

	for _, want := range []string{
		"localcode   0.82.0",
		fmt.Sprintf("platform    %s/%s", runtime.GOOS, runtime.GOARCH),
		"session     " + h.session.ID,
		"agent       balanced",
		"model       test-model",
		"workspace   " + h.wsDir,
		"config      " + h.cfgPath,
		"mcp         2 configured, 1 connected",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("/debug reply missing %q:\n%s", want, reply)
		}
	}

	// It must NOT be answered by /debug-log.
	if strings.Contains(reply, "debug_log:") || strings.Contains(reply, "localcode-debug-") {
		t.Errorf("/debug was answered by /debug-log:\n%s", reply)
	}
}

// "/debug-log" reaches its own route rather than "/debug"'s, toggles the
// logging locally, and never reaches the model.
//
// The pair is the reason to test either: "/debug" matches exactly, so a
// route that matched on a prefix would swallow the longer name.
func TestDebugLogIsNotAnsweredByDebug(t *testing.T) {
	h := newReleaseTestHarness(t)

	reply := h.sendCommand(t, "/debug-log")
	if !strings.Contains(reply, "debug_log: on") {
		t.Errorf("/debug-log reply missing 'debug_log: on':\n%s", reply)
	}
	if !strings.Contains(reply, "localcode-debug-") {
		t.Errorf("/debug-log reply missing log filename pattern:\n%s", reply)
	}
	if strings.Contains(reply, "Paste this into a bug report.") {
		t.Errorf("/debug-log was answered by /debug:\n%s", reply)
	}

	// Toggle it off
	replyOff := h.sendCommand(t, "/debug-log off")
	if !strings.Contains(replyOff, "debug_log: off") {
		t.Errorf("/debug-log off missing 'debug_log: off':\n%s", replyOff)
	}
}

// "/mcps" lists the configured servers and whether this conversation uses
// them, and turns one off or back on for this conversation alone.
func TestMCPsListsTheServersAndTurnsOneOff(t *testing.T) {
	h := newReleaseTestHarness(t)

	h.daemon.Loop.MCPStates = func() []agent.IntegrationState {
		return []agent.IntegrationState{
			{Name: "sqlite", Status: "connected"},
			{Name: "github", Status: "disconnected", Detail: "bad token"},
		}
	}

	reply := h.sendCommand(t, "/mcps")
	for _, want := range []string{
		"MCP servers in this conversation:",
		"sqlite", "connected", "on",
		"github", "disconnected", "bad token", "on",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("/mcps reply missing %q:\n%s", want, reply)
		}
	}

	// Turn sqlite off for this conversation
	replyOff := h.sendCommand(t, "/mcps off sqlite")
	if !strings.Contains(replyOff, "sqlite: off in this conversation.") {
		t.Errorf("/mcps off sqlite missing confirmation:\n%s", replyOff)
	}
	if !strings.Contains(replyOff, "off here") {
		t.Errorf("/mcps off sqlite summary missing 'off here':\n%s", replyOff)
	}

	// Confirm /mcps alone now reflects that sqlite is off here
	replyCheck := h.sendCommand(t, "/mcps")
	if !strings.Contains(replyCheck, "off here") {
		t.Errorf("/mcps reply did not remember off status:\n%s", replyCheck)
	}

	// Turn sqlite back on
	replyOn := h.sendCommand(t, "/mcps on sqlite")
	if !strings.Contains(replyOn, "sqlite: on in this conversation.") {
		t.Errorf("/mcps on sqlite missing confirmation:\n%s", replyOn)
	}
}

// "/thinking [on|off]" toggles the setting locally, and says what it is
// about — what the clients paint, not what the model does. A person
// expecting the second and getting the first would read the reply as a
// failure.
func TestThinkingTogglesWhatTheClientsPaint(t *testing.T) {
	h := newReleaseTestHarness(t)

	const paintExplanation = "This is what the clients paint, not what the model does"

	// Initial toggle
	reply := h.sendCommand(t, "/thinking")
	if !strings.Contains(reply, "show_thinking:") {
		t.Errorf("/thinking missing 'show_thinking:':\n%s", reply)
	}
	if !strings.Contains(reply, paintExplanation) {
		t.Errorf("/thinking missing client paint explanation:\n%s", reply)
	}

	// Explicitly off
	replyOff := h.sendCommand(t, "/thinking off")
	if !strings.Contains(replyOff, "show_thinking: off") {
		t.Errorf("/thinking off missing 'show_thinking: off':\n%s", replyOff)
	}
	if !strings.Contains(replyOff, paintExplanation) {
		t.Errorf("/thinking off missing client paint explanation:\n%s", replyOff)
	}
	if h.daemon.Loop.ShowThinking() != false {
		t.Errorf("Loop.ShowThinking() = true, want false")
	}

	// Explicitly on
	replyOn := h.sendCommand(t, "/thinking on")
	if !strings.Contains(replyOn, "show_thinking: on") {
		t.Errorf("/thinking on missing 'show_thinking: on':\n%s", replyOn)
	}
	if !strings.Contains(replyOn, paintExplanation) {
		t.Errorf("/thinking on missing client paint explanation:\n%s", replyOn)
	}
	if h.daemon.Loop.ShowThinking() != true {
		t.Errorf("Loop.ShowThinking() = true, want true")
	}
}

// "/timestamps [on|off]" toggles the setting without contacting the
// model.
func TestTimestampsTogglesWithoutTheModel(t *testing.T) {
	h := newReleaseTestHarness(t)

	reply := h.sendCommand(t, "/timestamps")
	if !strings.Contains(reply, "show_timestamps:") {
		t.Errorf("/timestamps missing 'show_timestamps:':\n%s", reply)
	}

	replyOff := h.sendCommand(t, "/timestamps off")
	if !strings.Contains(replyOff, "show_timestamps: off") {
		t.Errorf("/timestamps off missing 'show_timestamps: off':\n%s", replyOff)
	}
	if h.daemon.Loop.ShowTimestamps() != false {
		t.Errorf("Loop.ShowTimestamps() = true, want false")
	}

	replyOn := h.sendCommand(t, "/timestamps on")
	if !strings.Contains(replyOn, "show_timestamps: on") {
		t.Errorf("/timestamps on missing 'show_timestamps: on':\n%s", replyOn)
	}
	if h.daemon.Loop.ShowTimestamps() != true {
		t.Errorf("Loop.ShowTimestamps() = false, want true")
	}
}

// "/usage" reports this conversation, and "/usage all|today|week|month"
// reports across every conversation the daemon holds, archived ones
// included — the tokens are spent whether or not the conversation is
// still open.
func TestUsageReportsOneConversationAndEveryOne(t *testing.T) {
	h := newReleaseTestHarness(t)
	ctx := context.Background()

	// 1. Current conversation usage
	if _, err := h.daemon.Loop.Store.Append(h.session.ID, events.TypeUsage, map[string]any{
		"model":         "test-model",
		"input_tokens":  150,
		"output_tokens": 40,
	}); err != nil {
		t.Fatalf("append usage to session: %v", err)
	}
	h.daemon.Loop.RehydrateSession(h.session.ID)

	// 2. Second active conversation
	sess2, err := h.client.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession sess2: %v", err)
	}
	if _, err := h.daemon.Loop.Store.Append(sess2.ID, events.TypeUsage, map[string]any{
		"model":         "test-model",
		"input_tokens":  250,
		"output_tokens": 60,
	}); err != nil {
		t.Fatalf("append usage to sess2: %v", err)
	}

	// 3. Third conversation, which gets archived
	sess3, err := h.client.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession sess3: %v", err)
	}
	if _, err := h.daemon.Loop.Store.Append(sess3.ID, events.TypeUsage, map[string]any{
		"model":         "quick-model",
		"input_tokens":  80,
		"output_tokens": 20,
	}); err != nil {
		t.Fatalf("append usage to sess3: %v", err)
	}
	if _, err := h.client.ArchiveSession(ctx, sess3.ID); err != nil {
		t.Fatalf("ArchiveSession sess3: %v", err)
	}

	// Test /usage alone: reports token usage for this conversation only
	replyAlone := h.sendCommand(t, "/usage")
	for _, want := range []string{
		"Token usage by model:",
		"test-model: input 150 · output 40 · total 190 (1 calls)",
		"Grand total: input 150 · output 40 · total 190 (1 calls)",
	} {
		if !strings.Contains(replyAlone, want) {
			t.Errorf("/usage alone missing %q:\n%s", want, replyAlone)
		}
	}
	if strings.Contains(replyAlone, "quick-model") {
		t.Errorf("/usage alone leaked other conversation's model:\n%s", replyAlone)
	}
	if strings.Contains(replyAlone, "every conversation") || strings.Contains(replyAlone, "across") {
		t.Errorf("/usage alone printed across-session report:\n%s", replyAlone)
	}

	// Test /usage all: reports across every conversation, archived ones included
	replyAll := h.sendCommand(t, "/usage all")
	for _, want := range []string{
		"Token usage across every conversation (3 conversations):",
		"test-model: input 400 · output 100 · total 500 (2 calls)",
		"quick-model: input 80 · output 20 · total 100 (1 calls)",
		"Grand total: input 480 · output 120 · total 600 (3 calls)",
		"Counted from the conversations' own logs, archived ones included.",
	} {
		if !strings.Contains(replyAll, want) {
			t.Errorf("/usage all missing %q:\n%s", want, replyAll)
		}
	}

	// Test /usage today
	replyToday := h.sendCommand(t, "/usage today")
	for _, want := range []string{
		"Token usage across today (3 conversations):",
		"test-model",
		"quick-model",
		"Grand total: input 480 · output 120 · total 600 (3 calls)",
	} {
		if !strings.Contains(replyToday, want) {
			t.Errorf("/usage today missing %q:\n%s", want, replyToday)
		}
	}

	// Test /usage week
	replyWeek := h.sendCommand(t, "/usage week")
	for _, want := range []string{
		"Token usage across the last 7 days (3 conversations):",
		"test-model",
		"quick-model",
		"Grand total: input 480 · output 120 · total 600 (3 calls)",
	} {
		if !strings.Contains(replyWeek, want) {
			t.Errorf("/usage week missing %q:\n%s", want, replyWeek)
		}
	}

	// Test /usage month
	replyMonth := h.sendCommand(t, "/usage month")
	for _, want := range []string{
		"Token usage across the last 30 days (3 conversations):",
		"test-model",
		"quick-model",
		"Grand total: input 480 · output 120 · total 600 (3 calls)",
	} {
		if !strings.Contains(replyMonth, want) {
			t.Errorf("/usage month missing %q:\n%s", want, replyMonth)
		}
	}
}

// "/model" names the model in force and every profile the config can
// reach; "/model <profile>" changes the model and leaves the agent
// alone.
func TestModelReportsAndChangesWhatAnswers(t *testing.T) {
	h := newReleaseTestHarness(t)
	ctx := context.Background()

	// 1. /model alone: reports model in force and all available profiles
	reply := h.sendCommand(t, "/model")
	for _, want := range []string{
		"model: test-model (profile \"balanced\" on \"local\"), chosen by the \"general-purpose\" agent.",
		"This config can reach:",
		"* balanced — test-model on local",
		"  fast — quick-model on local",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("/model reply missing %q:\n%s", want, reply)
		}
	}

	// 2. /model fast: switches model to profile fast, keeps the agent
	replyFast := h.sendCommand(t, "/model fast")
	for _, want := range []string{
		"model: quick-model (profile \"fast\" on \"local\"), chosen by this conversation.",
		"* fast — quick-model on local",
		"  balanced — test-model on local",
	} {
		if !strings.Contains(replyFast, want) {
			t.Errorf("/model fast reply missing %q:\n%s", want, replyFast)
		}
	}

	// Verify via client over HTTP that the model profile changed and agent was kept
	mv, err := h.client.GetModel(ctx, h.session.ID)
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if mv.Profile != "fast" {
		t.Errorf("GetModel profile = %q, want 'fast'", mv.Profile)
	}
	if mv.Model != "quick-model" {
		t.Errorf("GetModel model = %q, want 'quick-model'", mv.Model)
	}
	if mv.Agent != "general-purpose" {
		t.Errorf("GetModel agent = %q, want 'general-purpose' (agent must be kept)", mv.Agent)
	}

	// 3. /model default: reverts back to agent's default profile
	replyDefault := h.sendCommand(t, "/model default")
	if !strings.Contains(replyDefault, "model: back to what the agent says.") {
		t.Errorf("/model default reply missing confirmation:\n%s", replyDefault)
	}

	mvDefault, err := h.client.GetModel(ctx, h.session.ID)
	if err != nil {
		t.Fatalf("GetModel after default: %v", err)
	}
	if mvDefault.Profile != "balanced" {
		t.Errorf("GetModel profile = %q, want 'balanced'", mvDefault.Profile)
	}
	if mvDefault.Agent != "general-purpose" {
		t.Errorf("GetModel agent = %q, want 'general-purpose'", mvDefault.Agent)
	}
}

// The two shapes a client reads rather than renders: GET /api/settings
// carries show_thinking and show_timestamps, and GET /api/slash-commands
// carries a description for every entry and a usage string wherever the
// command takes an argument.
func TestTheSettingsAndTheCommandListKeepTheirShape(t *testing.T) {
	h := newReleaseTestHarness(t)

	// Check GET /api/settings
	respSettings, err := http.Get(h.httpSrv.URL + "/api/settings")
	if err != nil {
		t.Fatalf("GET /api/settings: %v", err)
	}
	defer respSettings.Body.Close()
	if respSettings.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/settings status = %d", respSettings.StatusCode)
	}

	var settings map[string]any
	if err := json.NewDecoder(respSettings.Body).Decode(&settings); err != nil {
		t.Fatalf("decode /api/settings: %v", err)
	}

	thinkingVal, hasThinking := settings["show_thinking"]
	if !hasThinking {
		t.Errorf("GET /api/settings missing 'show_thinking'")
	} else if _, ok := thinkingVal.(bool); !ok {
		t.Errorf("GET /api/settings 'show_thinking' is %T, want bool", thinkingVal)
	}

	timestampsVal, hasTimestamps := settings["show_timestamps"]
	if !hasTimestamps {
		t.Errorf("GET /api/settings missing 'show_timestamps'")
	} else if _, ok := timestampsVal.(bool); !ok {
		t.Errorf("GET /api/settings 'show_timestamps' is %T, want bool", timestampsVal)
	}

	// Check GET /api/slash-commands
	respCmds, err := http.Get(h.httpSrv.URL + "/api/slash-commands")
	if err != nil {
		t.Fatalf("GET /api/slash-commands: %v", err)
	}
	defer respCmds.Body.Close()
	if respCmds.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/slash-commands status = %d", respCmds.StatusCode)
	}

	var cmds []client.SlashCommandInfo
	if err := json.NewDecoder(respCmds.Body).Decode(&cmds); err != nil {
		t.Fatalf("decode /api/slash-commands: %v", err)
	}
	if len(cmds) < 10 {
		t.Fatalf("GET /api/slash-commands returned only %d commands", len(cmds))
	}

	// Commands that take no arguments
	noArgCommands := map[string]bool{
		"memory":              true,
		"show-scheduled-task": true,
		"debug-log":           true,
		"update":              true,
		"reset-mcp":           true,
		"reset-skills":        true,
		"status":              true,
		"debug":               true,
		"clear":               true,
		"rewind":              true,
		"redo":                true,
	}

	for _, c := range cmds {
		if strings.TrimSpace(c.Description) == "" {
			t.Errorf("slash command /%s has empty description", c.Name)
		}

		takesArg := !noArgCommands[c.Name]
		if takesArg && strings.TrimSpace(c.Usage) == "" {
			t.Errorf("slash command /%s takes arguments but has empty usage string", c.Name)
		}
		if !takesArg && strings.TrimSpace(c.Usage) != "" {
			t.Errorf("slash command /%s takes no arguments but has usage string %q", c.Name, c.Usage)
		}
		if c.Usage != "" && strings.Contains(c.Usage, "/"+c.Name) {
			t.Errorf("slash command /%s: usage %q repeats the command name", c.Name, c.Usage)
		}
	}
}
