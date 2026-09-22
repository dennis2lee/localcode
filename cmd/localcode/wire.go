package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"localcode/internal/agent"
	"localcode/internal/commands"
	"localcode/internal/config"
	"localcode/internal/credentials"
	"localcode/internal/daemon"
	"localcode/internal/egress"
	mcpclient "localcode/internal/mcp"
	"localcode/internal/memory"
	"localcode/internal/prompt"
	"localcode/internal/provider"
	"localcode/internal/rules"
	"localcode/internal/session"
	"localcode/internal/shell"
	"localcode/internal/skills"
	"localcode/internal/tools"
	"localcode/internal/trace"
	"localcode/internal/userdirs"
	"strings"
	"sync"
	"time"
)

// env is the ambient machine state every builder below needs, resolved
// exactly once at startup and passed down. Each of these used to be
// re-derived — with its own error handling — at three or four separate call
// sites, which meant a build could half-succeed against two different
// answers if the process ever changed directory mid-startup.
type env struct {
	home string
	cwd  string
}

func resolveEnv() (env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return env{}, fmt.Errorf("resolve home dir: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return env{}, fmt.Errorf("resolve working dir: %w", err)
	}
	return env{home: home, cwd: cwd}, nil
}

// buildDaemon wires config -> providers -> tools -> agent.Loop -> Task
// Manager -> daemon.Daemon. Shared by both --headless and the default
// embedded-daemon path.
//
// The returned cleanup func must be called when the daemon is done with —
// it shuts down any MCP server subprocesses this build started. It is never
// nil, so callers can defer it unconditionally.
//
// progress, when non-nil, is called as each step starts. This takes long
// enough to be worth narrating — several seconds is normal with a few
// MCP servers configured — and the GUI shows it on a startup screen so
// the wait reads as work rather than as a hang. nil is the right value
// for every mode that has a terminal to log to instead.
func buildDaemon(ctx context.Context, configPath string, progress func(string)) (*daemon.Daemon, func(), error) {
	if progress == nil {
		progress = func(string) {}
	}

	e, err := resolveEnv()
	if err != nil {
		return nil, nil, err
	}

	progress("reading configuration")
	cfg, err := loadConfig(configPath, e)
	if err != nil {
		return nil, nil, err
	}

	// Before any client is built, because it works by replacing the
	// transport every one of them ends up using. Installed once per
	// process: a "/reset-mcp" that re-reads the config must not wrap the
	// transport a second time, and egress.Install is idempotent for that
	// reason.
	egress.Install(cfg.EgressPolicy())

	// Before anything runs a command, and before the first permission is
	// decided: whether the shell is POSIX is what decides whether a bash
	// allow rule can be trusted, and a rule resolved against the wrong
	// answer is the failure this exists to prevent.
	shell.Configure(cfg.Shell)

	progress("opening model providers")
	providers, err := buildProviders(ctx, cfg, e)
	if err != nil {
		return nil, nil, err
	}

	progress("loading sessions")
	sessionDir := filepath.Join(e.home, ".localcode", "sessions")
	store, sessionWarnings, err := session.LoadAllFromDisk(sessionDir)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range sessionWarnings {
		log.Printf("session restore: %v", w)
	}

	broker := agent.NewPermissionBroker(store)
	input := agent.NewInputBroker(store)
	configFilePath := ""
	if path, err := resolvedConfigPath(configPath); err != nil {
		// Not fatal: "always allow" just falls back to session-only
		// approvals (ConfigPath == "" disables persisting), same as
		// today's behavior before this feature existed.
		log.Printf("permission: could not resolve a config.json path for \"always allow\", falling back to session-only approvals: %v", err)
	} else {
		broker.ConfigPath = path
		// The same file the toggle commands persist to. One resolution,
		// so "always allow" and "/smart-agent on" cannot end up writing
		// to two different config.json files.
		configFilePath = path
	}
	registry, err := buildRegistry(cfg, broker, store)
	if err != nil {
		return nil, nil, err
	}

	// The default workspace: the directory this process started in. It is
	// the only project known this early — no session exists yet and no
	// workspace switch has named another — and every session created
	// later inherits it, so it is also what loop.GetProjectDir returns
	// until the first switch. Passed explicitly rather than re-derived
	// inside the loaders, so startup and every later reload resolve the
	// project root the same way.
	projectDir := e.cwd

	progress("loading skills and commands")
	skillsSection, memoryPolicy, memorySection, skillList, cmdList, memDir, err := buildSystemPrompt(cfg, registry, projectDir, e.home)
	if err != nil {
		return nil, nil, err
	}

	cleanup := func() {}
	var mcpManager *mcpclient.Manager
	if len(cfg.MCPServers) > 0 {
		// A server that fails to connect or list tools is skipped (logged as
		// a warning), not fatal: one bad MCP server shouldn't take down the
		// whole daemon. The Manager is kept both for GET /api/mcp-servers and
		// for the cleanup func — closing it is what stops the child MCP
		// server processes, which otherwise linger until this process exits.
		var mcpTools []tools.Tool
		var warnings []error
		// Named one at a time rather than as a count: connecting means
		// spawning a subprocess and waiting for a handshake, so a server
		// that is slow or dead is what the whole startup is stuck behind,
		// and its name is the useful thing to be showing while that
		// happens.
		// The pin file is the trust audit: each server's advertised tool
		// surface is fingerprinted, and a change since the last run is a
		// warning naming the server. See internal/mcp/pins.go.
		mcpManager, mcpTools, warnings = mcpclient.Connect(ctx, cfg.MCPServers, filepath.Join(e.home, ".localcode", "mcp-pins.json"), func(name string) {
			progress("connecting to MCP server " + name)
		})
		for _, w := range warnings {
			log.Printf("mcp: %v", w)
		}
		for _, t := range mcpTools {
			registry.Register(t)
		}
		if mcpManager != nil {
			// A server can die at any point in a long session, and a tool
			// call is the only other thing that would notice — which only
			// happens when the model reaches for it. Without this poll an
			// idle client's indicator would keep claiming a long-dead
			// server was fine.
			mcpManager.StartHealthChecks(ctx)
			// Shutdown goes through currentMCP below, which "/reset-mcp"
			// updates, so the servers closed are the ones actually
			// running rather than the ones that were at startup.
		}
	}

	loop := agent.New(store, registry, providers, cfg)
	loop.SkillsSection = skillsSection
	loop.MemoryPolicy = memoryPolicy
	loop.MemorySection = memorySection
	loop.Skills = skillList
	loop.Commands = cmdList
	loop.ProjectDir = projectDir
	loop.ConfigPath = configFilePath
	loop.MemoryDir = memDir
	loop.Version = version
	loop.Input = input
	// "/rewind" needs a copy of a file as it was before the turn changed
	// it, and the only place the resolved path exists is inside
	// internal/tools. Wired here rather than in buildOneShot: a run prints
	// one answer and exits, so there is no later turn to rewind from, and
	// its store keeps nothing on disk for a pre-image to live beside.
	registry.BeforeWrite = loop.CheckpointWrite
	// The structured turn log. Opened whatever the setting says, because
	// the setting is live and a daemon started with Smart Agent off can
	// have it turned on ten minutes later; nothing is written until then
	// (see Loop.tracer), and the file itself is not created until the
	// first record.
	if tw, err := trace.Open(filepath.Join(e.home, ".localcode", "trace")); err != nil {
		log.Printf("trace: %v (turn tracing is off for this run)", err)
	} else {
		// Retention is applied at open and at each day rotation, so a
		// daemon left running for months does not accumulate a file per
		// day forever. See trace.SetRetention for the defaults.
		tw.SetRetention(cfg.TraceMaxAgeDays, cfg.TraceMaxTotalMB)
		// The manifest store sits beside the trace and is bounded by the
		// same two settings: a manifest whose trace line has been
		// pruned is not worth keeping, and one whose trace line
		// survives has to be resolvable or the id in that line is
		// decoration. It used to take the age bound only, which left
		// the size bound applying to one of the two files a diagnostic
		// reads together.
		if ms, merr := prompt.OpenStore(filepath.Join(e.home, ".localcode", "manifests")); merr == nil {
			ms.SetRetention(cfg.TraceMaxAgeDays, cfg.TraceMaxTotalMB)
			loop.Manifests = ms
		}
		loop.Trace = tw
	}
	// Per turn, for the directory that turn is working in — see
	// Loop.WorkspaceRules. e.home is the only thing fixed at startup here.
	loop.WorkspaceRules = workspaceRules(e, cfg)
	// Ask the server how big a window it is serving, rather than guessing
	// from the model's name. Only the openai-compatible clients implement
	// it: a hosted model's name identifies it exactly, while a local
	// server serves whatever was loaded — usually with a smaller window
	// than the model supports, since the window is what costs VRAM, which
	// no name can express. Anything that does not answer falls back to the
	// name-based guess.
	loop.ProbeContextWindow = func(ctx context.Context, providerKey, model string) (int, bool) {
		prober, ok := providers[providerKey].(interface {
			ContextWindow(context.Context, string) (int, bool)
		})
		if !ok {
			return 0, false
		}
		return prober.ContextWindow(ctx, model)
	}
	// Restores conversation history and /usage totals for every session
	// just loaded from disk — the event log survives a restart on its
	// own, but Loop's in-memory history/usage maps don't, so without this
	// a resumed session would replay its old transcript on screen while
	// the model itself had no memory of any of it.
	progress("restoring conversation history")
	loop.RehydrateAll()
	tasks := agent.NewTaskManager(ctx, loop, cfg.MaxConcurrentTasks)
	// Work booked for later. Restored from the session logs, which is
	// what makes a row survive a restart; only the ones whose moment has
	// not yet come are re-armed, and the rest are marked missed rather
	// than fired late. See agent.Scheduler.Restore.
	scheduler := agent.NewScheduler(ctx, loop)
	// The model's way in: a request for later becomes a booking rather
	// than work done now. Registered after the scheduler exists, because
	// the tool is useless without one.
	registry.Register(agent.NewScheduleTool(loop))
	// Nothing under an archived conversation has its books rebuilt here,
	// and that is what keeps those logs on disk: reading a session's
	// schedule rows means reading its whole log, which is the cost
	// archiving is supposed to have put away. The tasks that ran inside
	// the conversation count as much as the conversation — they book
	// work of their own, under their own session ids — so the store's
	// own answer is used rather than a parent check here.
	//
	// Retrieving rebuilds them, and marks anything whose moment passed
	// on the shelf as missed at that point. See handleRetrieveSession.
	shelved := store.ShelvedIDs()
	var sessionIDs []string
	for _, sess := range store.AllSessions() {
		if shelved[sess.ID] {
			continue
		}
		sessionIDs = append(sessionIDs, sess.ID)
	}
	scheduler.Restore(sessionIDs, time.Now())

	// A debate reviewer's only way to answer. Registered unconditionally
	// and hidden from every turn that is not a review, which is the same
	// pattern the delegation tools use and for a sharper reason: a model
	// that could call this on its own work would be marking its own
	// homework. See Loop.hiddenTools and agent.reviewerToolNames.
	registry.Register(agent.VerdictTool{})

	// The natural-language way into a debate: the model separates who
	// reviews, how many rounds and the work, and localcode runs the loop.
	// Registered after the task manager, which the debate needs to give a
	// reviewer a session of its own.
	registry.Register(agent.NewDebateTool(loop))
	registry.Register(agent.NewUpdatePlanTool(loop))
	registry.Register(agent.NewAskUserTool(loop))

	// The delegation tools. Registered unconditionally, and hidden per
	// turn instead — see Loop.hiddenTools. They used to be
	// registered only when the config had more than one agent, which was
	// the same rule read at the only moment it could not change; Smart
	// Agent moves that rule to turn time, because turning it on adds six
	// agents to a config that has none and has to take effect on the next
	// message rather than the next restart.
	//
	// Registered after the TaskManager exists (they need one), but registry
	// is a live pointer already shared with loop, so this still takes
	// effect before any SendMessage call.
	registry.Register(agent.NewTaskTool(tasks, loop.DelegatableAgents))
	registry.Register(agent.NewTaskBackgroundTool(tasks, loop.DelegatableAgents))
	registry.Register(agent.NewTaskCollectTool(tasks))
	// The orchestrator and the tool a stage answers with. Registered
	// unconditionally, like the delegation tools beside them: whether they
	// are offered is a per-turn question (see Loop.hiddenTools), and a
	// registry read at startup cannot answer a switch that moves.
	registry.Register(agent.NewOrchestrateTool(loop))
	registry.Register(agent.NewAnswerTool())
	// Reading another conversation. Registered unconditionally, like the
	// tools beside it: whether it is offered is a per-turn question, and a
	// registry read at startup cannot answer a roster that moves.
	registry.Register(agent.NewSessionReadTool(loop))
	// The model running a command. Registered unconditionally, like the
	// tools beside it, and hidden per turn: whether it is offered depends
	// on a switch that moves and on a list of opt-ins that a
	// "/reset-skills" can change, neither of which a registry read at
	// startup can answer. See Loop.hiddenTools.
	registry.Register(agent.NewCommandTool(loop))

	d := daemon.New(loop, broker, tasks, mcpManager, daemon.WebFS(), version)

	// "/update": the same install the settings window's button performs,
	// reached from the prompt box so the TUI has it too. The daemon owns
	// it because everything the refusal is made of is the daemon's — which
	// sessions have a turn in flight, which have a background task, and
	// whether this process is one that may replace its own binary.
	loop.SelfUpdate = d.SelfUpdate

	// "/reset-skills": reload from disk, against the *live* workspace
	// rather than the directory the daemon started in, which is itself a
	// small fix — a workspace switched at runtime used to keep serving
	// the old project's skills until a restart.
	//
	// Custom commands come along: they are read from the same two roots,
	// so a switch that fixed skills and left commands stale would be the
	// same bug one directory over.
	loop.ReloadSkills = func() (string, error) {
		// The live workspace, not the one the daemon started in, so the
		// chain is re-run against the project actually being worked on.
		return reloadProjectAssets(loop, registry, loop.GetProjectDir(), e.home)
	}

	// The moment the real project becomes known on the desktop: the
	// daemon starts in its install directory and the client names the
	// project afterwards with POST /api/workspace, which lands in
	// SetProjectDir. Reloading there means the project's skills and
	// commands are loaded without anyone typing "/reset-skills".
	//
	// Never fatal: the switch itself already succeeded, so a reload
	// failure is a log line rather than an error for anyone to handle.
	loop.OnProjectDirChanged = func() {
		report, err := loop.ReloadSkills()
		if err != nil {
			log.Printf("skills and commands: reload after workspace change failed: %v", err)
			return
		}
		log.Printf("skills and commands: %s", report)
	}

	// "/reset-mcp": stop the servers, re-read their configuration from
	// disk, and reconnect. The whole point is picking up an edited
	// config.json without restarting, so the config is read fresh rather
	// than reusing the one this process started with.
	//
	// currentMCP is what cleanup closes, through the pointer, so a
	// daemon shut down after a reload stops the servers that are
	// actually running rather than the ones that were.
	currentMCP := mcpManager
	var mcpReloadMu sync.Mutex
	loop.ReloadMCP = func() (string, error) {
		mcpReloadMu.Lock()
		defer mcpReloadMu.Unlock()

		fresh, err := loadConfig(configPath, e)
		if err != nil {
			return "", fmt.Errorf("re-read config: %w", err)
		}

		// The old servers go first: two managers running the same
		// stdio server would be two child processes fighting over one
		// configuration.
		if currentMCP != nil {
			currentMCP.Close()
		}
		// And their tools go with them, so a server removed from the
		// config takes its tools out of the model's hands rather than
		// leaving calls that can only fail.
		for _, name := range registry.Names() {
			if strings.HasPrefix(name, "mcp__") {
				registry.Deregister(name)
			}
		}

		var report strings.Builder
		if len(fresh.MCPServers) == 0 {
			currentMCP = nil
			d.SwapMCP(nil)
			loop.Config.SetMCPServersRuntime(fresh.MCPServers)
			return "MCP reset: no servers configured", nil
		}
		manager, mcpTools, warnings := mcpclient.Connect(ctx, fresh.MCPServers,
			filepath.Join(e.home, ".localcode", "mcp-pins.json"), nil)
		for _, t := range mcpTools {
			registry.Register(t)
		}
		for _, w := range warnings {
			fmt.Fprintf(&report, "warning: %v\n", w)
		}
		if manager != nil {
			manager.StartHealthChecks(ctx)
		}
		currentMCP = manager
		d.SwapMCP(manager)
		loop.Config.SetMCPServersRuntime(fresh.MCPServers)
		fmt.Fprintf(&report, "MCP reset: %d server(s) connected, %d tool(s) registered", len(fresh.MCPServers), len(mcpTools))
		return report.String(), nil
	}
	// One shutdown for everything, whatever has changed since startup:
	// the MCP servers running *now* (a reload may have replaced or first
	// created them), then the trace file.
	// Published so a handoff can stop the servers before the successor
	// starts its own: see the note at the call site in handoff.go.
	closeMCPServers = func() {
		mcpReloadMu.Lock()
		defer mcpReloadMu.Unlock()
		if currentMCP != nil {
			currentMCP.Close()
			currentMCP = nil
		}
	}

	cleanup = func() {
		// Everything this daemon holds that outlives it if nobody says
		// otherwise. Two of these were added because a startup update
		// throws a fully built daemon away — runGUI builds one before it
		// knows whether a newer binary is waiting — and "throws away"
		// only ever meant the MCP servers and the trace file.
		//
		// The scheduler: still armed, in a process that goes on running
		// as the window. Every prompt booked for later then fired twice,
		// once in the discarded daemon and once in the successor.
		//
		// The session logs: still open, in that same process. On Windows
		// a file another process holds cannot be removed, so deleting a
		// conversation failed — the same fault as the one Retire now
		// avoids, by the other route into it.
		if loop.Schedules != nil {
			loop.Schedules.Disarm()
		}
		closeMCPServers()
		if loop.Trace != nil {
			loop.Trace.Close()
		}
		store.Close()
	}

	// The same path "always allow" persists to, and for the same reason:
	// a settings change the user makes should still be there next time.
	if path, err := resolvedConfigPath(configPath); err == nil {
		d.ConfigPath = path
	}

	return d, cleanup, nil
}

// buildRegistry constructs the tool registry and registers every built-in
// tool, wiring the permission broker and per-tool decision resolver from
// cfg.
func buildRegistry(cfg *config.Config, broker *agent.PermissionBroker, store *session.Store) (*tools.Registry, error) {
	registry := tools.NewRegistry(broker.Func())
	// The pipeline's order lives in ComposeResolver, where it is a
	// stated contract with its own test: rules and guards, then the
	// workspace boundary, then the two skips. The four switches are
	// per session, so the policy takes the session off the context and
	// falls back to this config's defaults.
	registry.Resolver = tools.ComposeResolver(
		func(ctx context.Context, toolName, subject string, staticRequiresPermission bool) tools.Decision {
			return tools.Decision(cfg.ResolvePermissionFor(ctx, toolName, subject, staticRequiresPermission))
		},
		agent.NewPermissionPolicy(store, cfg).ToolsPolicy(),
	)
	registry.Hooks = cfg.Hooks
	registry.Register(tools.ReadFile{})
	registry.Register(tools.WriteFile{})
	registry.Register(tools.Edit{})
	registry.Register(tools.Bash{})
	registry.Register(tools.Glob{})
	registry.Register(tools.Grep{})
	// Only when the project has said how it is checked. Registering it
	// regardless would advertise a tool whose every call is an error, and
	// the model would keep trying it.
	if strings.TrimSpace(cfg.VerifyCommand) != "" {
		registry.Register(tools.NewCheck(func() string { return cfg.VerifyCommand }))
	}
	return registry, nil
}

// buildSystemPrompt loads skills, custom commands, project rules, and the
// auto-memory section, registers the Skill tool if any skills were found,
// and returns the combined text to append to Loop.SystemPrompt alongside
// the loaded skills/commands/memory-dir Loop needs directly.
//
// projectDir is the project the assets are read for — the daemon's
// default workspace at startup, the live one on every reload — passed in
// rather than re-derived from the process, so a daemon started in one
// directory and working in another reads the project it works in.
func buildSystemPrompt(cfg *config.Config, registry *tools.Registry, projectDir, home string) (skillsSection, memoryPolicy, memorySection string, skillList []skills.Skill, cmdList []commands.Command, memDir string, err error) {
	project, global := assetsFor(projectDir, home)
	if project.Chosen != ".localcode" || global.Chosen != ".localcode" {
		// Worth a line: an empty winner still wins, so "where did my
		// skills go" is answered by the log rather than by reading this
		// package's source.
		//
		// One path when the two roots are one directory, which they are
		// whenever localcode is run in a home directory. "reading X and X"
		// reads as a bug in the line rather than as the fact it is.
		if project.Path == global.Path {
			log.Printf("skills and commands: reading %s, which is both the project root and yours (config.json is always ~/.localcode/config.json)",
				project.Path)
		} else {
			log.Printf("skills and commands: reading %s and %s (config.json is always ~/.localcode/config.json)",
				project.Path, global.Path)
		}
	}
	// And a second line only when something was actually lost. The first
	// says where the assets came from; this one says where they did not,
	// which is the half somebody is looking for when a skill they wrote
	// has stopped appearing. See internal/userdirs: first root wins
	// whole, and running another agent once in a repository is enough to
	// change which root that is.
	for _, r := range []userdirs.Root{project, global} {
		for _, name := range r.Shadowed {
			log.Printf("skills and commands: %s has skills or commands and is not read, because %s comes first",
				filepath.Join(filepath.Dir(r.Path), name), r.Chosen)
		}
	}

	skillList, err = loadSkills(projectDir, home)
	if err != nil {
		return "", "", "", nil, nil, "", err
	}
	skillsSection = setSkillAssets(nil, registry, skillList)

	cmdList, err = commands.LoadAll(project.Commands, global.Commands)
	if err != nil {
		return "", "", "", nil, nil, "", err
	}

	// Project rules are deliberately NOT folded in here. They depend on
	// which directory a turn runs in, and this prompt is built once for the
	// whole daemon — see Loop.WorkspaceRules.

	if cfg.MemoryEnabled() {
		memDir = memory.Dir(projectDir, home)
		if err := os.MkdirAll(memDir, 0o755); err != nil {
			return "", "", "", nil, nil, "", fmt.Errorf("create memory dir: %w", err)
		}
		memoryPolicy = memory.PolicySection(memDir)
		memorySection = memory.IndexSection(memory.LoadIndex(memDir))
	}

	return skillsSection, memoryPolicy, memorySection, skillList, cmdList, memDir, nil
}

// loadSkills scans the project-local skills dir before the global one, so a
// project can override a same-named global skill. Which global one that is
// depends on what is installed: see internal/userdirs.
func loadSkills(projectDir, home string) ([]skills.Skill, error) {
	project, global := assetsFor(projectDir, home)
	return skills.LoadAll(project.Skills, global.Skills)
}

// assetsFor is the project root and the home root the assets are read out
// of. One place, so the loaders cannot drift, and two chains, because a
// repo's agent directory and a person's need not be the same one. Both
// roots are inputs: the project is the workspace actually being worked
// in, which on the desktop is not the directory the daemon started in.
func assetsFor(projectDir, home string) (project, global userdirs.Root) {
	return userdirs.At(projectDir), userdirs.At(home)
}

// reloadProjectAssets re-reads the project skills and custom commands for
// one project directory plus the global ones, and swaps them into the
// loop: the one answer both startup (through buildSystemPrompt) and
// "/reset-skills" use, so the two cannot drift. It reports what it read
// and where from, the way the startup log names both roots.
func reloadProjectAssets(loop *agent.Loop, registry *tools.Registry, projectDir, home string) (string, error) {
	project, global := assetsFor(projectDir, home)
	skillList, err := skills.LoadAll(project.Skills, global.Skills)
	if err != nil {
		return "", err
	}
	cmdList, err := commands.LoadAll(project.Commands, global.Commands)
	if err != nil {
		return "", err
	}
	setSkillAssets(loop, registry, skillList)
	loop.SetCommands(cmdList)
	return assetsReport(skillList, cmdList, project, global), nil
}

// setSkillAssets swaps the loop's skills and the Skill tool behind them.
// A nil loop means startup, where there is nothing to swap yet — only the
// registry half applies, and the prompt section is returned for the
// caller to keep.
func setSkillAssets(loop *agent.Loop, registry *tools.Registry, skillList []skills.Skill) string {
	section := ""
	if len(skillList) > 0 {
		section = skills.SystemPromptSection(skillList)
		registry.Register(tools.NewSkillTool(skillList))
	} else {
		// No skills means no Skill tool: offering the model a tool
		// with nothing behind it is a call that can only fail.
		registry.Deregister("Skill")
	}
	if loop != nil {
		loop.SetSkills(skillList, section)
	}
	return section
}

// assetsReport says what a reload read and where it read it from. Both
// directories are always named, the way the startup log names both roots.
func assetsReport(skillList []skills.Skill, cmdList []commands.Command, project, global userdirs.Root) string {
	skillNames := make([]string, len(skillList))
	for i, sk := range skillList {
		skillNames[i] = sk.Name
	}
	cmdNames := make([]string, len(cmdList))
	for i, cmd := range cmdList {
		cmdNames[i] = cmd.Name
	}
	return fmt.Sprintf("skills and commands reloaded: %d skill(s) (%s), %d command(s) (%s) from %s and %s",
		len(skillList), strings.Join(skillNames, ", "),
		len(cmdList), strings.Join(cmdNames, ", "),
		project.Skills+", "+project.Commands, global.Skills+", "+global.Commands)
}

// resolvedConfigPath is where an "always allow" permission decision gets
// written: the explicit --config file if one was given (that's the only
// config in play, so there's no ambiguity about which file "always" means),
// otherwise the global ~/.localcode/config.json — not the project-local
// override — so an approval survives switching projects, matching what
// "always" reads like to someone answering the prompt.
func resolvedConfigPath(explicitPath string) (string, error) {
	if explicitPath != "" {
		return explicitPath, nil
	}
	return config.DefaultGlobalPath()
}

func loadConfig(explicitPath string, e env) (*config.Config, error) {
	var cfg *config.Config
	var notes []string
	var err error
	if explicitPath != "" {
		cfg, notes, err = config.Load(explicitPath)
	} else {
		cfg, notes, err = config.LoadMerged(e.cwd)
	}
	if err != nil {
		return nil, err
	}
	if len(notes) > 0 {
		// One per line. They are sentences — a key that was accepted and
		// not acted on, or a whole file that was set aside — and joining
		// sentences with commas reads as a list of key names, which is
		// what it used to say it was.
		for _, note := range notes {
			fmt.Fprintf(os.Stderr, "config: %s\n", note)
		}
	}
	return cfg, nil
}

// localMouseEnabled reports whether the TUI on this machine takes the
// mouse for its scrollbar. Read from the config on this machine rather
// than from the daemon the TUI attaches to: with --server pointed at
// another machine, that daemon's config must not decide whether this
// terminal gives up drag-to-select. Anything unreadable means off.
func localMouseEnabled(configPath string) bool {
	e, err := resolveEnv()
	if err != nil {
		return false
	}
	cfg, err := loadConfig(configPath, e)
	if err != nil {
		return false
	}
	return cfg.Mouse != nil && *cfg.Mouse
}

func buildProviders(ctx context.Context, cfg *config.Config, e env) (map[string]provider.Provider, error) {
	out := map[string]provider.Provider{}
	for name, pc := range cfg.Providers {
		switch pc.Type {
		case config.ProviderBedrock:
			// AWS configuration is intentionally deferred until the first
			// request through this provider. Config files are merged, so an
			// unused Bedrock entry inherited from the other scope must not
			// make a local-only daemon depend on ~/.aws or an AWS profile.
			out[name] = provider.NewBedrock(pc.Region, pc.Profile)
		case config.ProviderOpenAICompat:
			out[name] = provider.NewOpenAICompat(pc.BaseURL, pc.APIKey)
		case config.ProviderAnthropic:
			apiKey := pc.APIKey
			if apiKey == "" {
				creds, err := credentials.Load(e.home)
				if err != nil {
					return nil, fmt.Errorf("load credentials for anthropic provider %q: %w", name, err)
				}
				apiKey = creds.AnthropicAPIKey
			}
			if apiKey == "" {
				return nil, fmt.Errorf("provider %q (anthropic) has no api_key and none saved — run `localcode login anthropic` first", name)
			}
			ad := provider.NewAnthropicDirect(apiKey)
			if pc.BaseURL != "" {
				ad.BaseURL = pc.BaseURL
			}
			out[name] = ad
		default:
			return nil, fmt.Errorf("provider %q has unknown type %q", name, pc.Type)
		}
	}
	return out, nil
}

// workspaceRules is the AGENTS.md/CLAUDE.md and instructions loader a Loop is given.
//
// Named rather than written inline at each call site, because there are
// two of them now — the daemon and "localcode run" — and "what the
// project's rules are" must not be able to differ between them.
//
// cfg is a parameter rather than something this reads for itself, for the
// same reason. The config carries the instructions list, and a version of
// this that re-read the file when it was not handed one would give the two
// call sites two answers — which is the one thing the paragraph above says
// must not happen — and would re-parse the config on every turn besides.
func workspaceRules(e env, cfg *config.Config) func(string) string {
	return func(dir string) string { return loadWorkspaceRules(dir, e.home, cfg) }
}

// loadWorkspaceRules reads base project/user rules via rules.Load and appends
// any configured instruction files or patterns in order.
func loadWorkspaceRules(dir, home string, cfg *config.Config) string {
	baseRules := rules.Load(dir, home)
	if cfg == nil || len(cfg.Instructions) == 0 {
		return baseRules
	}

	var contents []string
	for _, entry := range cfg.Instructions {
		pattern := entry
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(dir, pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			log.Printf("instructions: pattern %q: %v", entry, err)
			continue
		}
		if len(matches) == 0 {
			// Literal file path without glob magic might still exist
			if info, serr := os.Stat(pattern); serr == nil && !info.IsDir() {
				matches = []string{pattern}
			} else {
				log.Printf("instructions: pattern %q matched no files in %s", entry, dir)
				continue
			}
		}
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil || info.IsDir() {
				continue
			}
			data, err := os.ReadFile(m)
			if err != nil {
				log.Printf("instructions: read %s: %v", m, err)
				continue
			}
			text := strings.TrimSpace(string(data))
			if text != "" {
				contents = append(contents, text)
			}
		}
	}

	if len(contents) == 0 {
		return baseRules
	}
	instructionsBlock := strings.Join(contents, "\n\n")
	if baseRules == "" {
		return "Project/user rules:\n\n" + instructionsBlock + "\n"
	}
	return strings.TrimRight(baseRules, "\n") + "\n\n" + instructionsBlock + "\n"
}
