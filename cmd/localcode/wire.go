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
	mcpclient "localcode/internal/mcp"
	"localcode/internal/memory"
	"localcode/internal/prompt"
	"localcode/internal/provider"
	"localcode/internal/rules"
	"localcode/internal/session"
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

	progress("loading skills and commands")
	skillsSection, memoryPolicy, memorySection, skillList, cmdList, memDir, err := buildSystemPrompt(cfg, registry, e)
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
	loop.ProjectDir = e.cwd
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
	loop.WorkspaceRules = workspaceRules(e)
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
	var sessionIDs []string
	for _, sess := range store.AllSessions() {
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
	loop.ReloadSkills = func() (string, error) {
		// The live workspace, not the one the daemon started in, so the
		// chain is re-run against the project actually being worked on.
		projectSkills := userdirs.At(loop.GetProjectDir()).Skills
		globalSkills := userdirs.At(e.home).Skills
		list, err := skills.LoadAll(projectSkills, globalSkills)
		if err != nil {
			return "", err
		}
		section := ""
		if len(list) > 0 {
			section = skills.SystemPromptSection(list)
			registry.Register(tools.NewSkillTool(list))
		} else {
			// No skills means no Skill tool: offering the model a tool
			// with nothing behind it is a call that can only fail.
			registry.Deregister("Skill")
		}
		loop.SetSkills(list, section)
		if len(list) == 0 {
			return "skills reloaded: none installed (looked in " + projectSkills + " and " + globalSkills + ")", nil
		}
		names := make([]string, len(list))
		for i, sk := range list {
			names[i] = sk.Name
		}
		return fmt.Sprintf("skills reloaded: %d (%s) from %s and %s",
			len(list), strings.Join(names, ", "), projectSkills, globalSkills), nil
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
			loop.Config.MCPServers = fresh.MCPServers
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
		loop.Config.MCPServers = fresh.MCPServers
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
func buildSystemPrompt(cfg *config.Config, registry *tools.Registry, e env) (skillsSection, memoryPolicy, memorySection string, skillList []skills.Skill, cmdList []commands.Command, memDir string, err error) {
	project, global := assetsFor(e)
	if project.Chosen != ".localcode" || global.Chosen != ".localcode" {
		// Worth a line: an empty winner still wins, so "where did my
		// skills go" is answered by the log rather than by reading this
		// package's source.
		log.Printf("skills and commands: reading %s and %s (config.json is always ~/.localcode/config.json)",
			project.Path, global.Path)
	}

	skillList, err = loadSkills(e)
	if err != nil {
		return "", "", "", nil, nil, "", err
	}
	if len(skillList) > 0 {
		registry.Register(tools.NewSkillTool(skillList))
		skillsSection = skills.SystemPromptSection(skillList)
	}

	cmdList, err = commands.LoadAll(project.Commands, global.Commands)
	if err != nil {
		return "", "", "", nil, nil, "", err
	}

	// Project rules are deliberately NOT folded in here. They depend on
	// which directory a turn runs in, and this prompt is built once for the
	// whole daemon — see Loop.WorkspaceRules.

	if cfg.MemoryEnabled() {
		memDir = memory.Dir(e.cwd, e.home)
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
func loadSkills(e env) ([]skills.Skill, error) {
	project, global := assetsFor(e)
	return skills.LoadAll(project.Skills, global.Skills)
}

// assetsFor is the project root and the home root this environment reads
// skills and custom commands out of. One place, so the loaders cannot
// drift, and two chains, because a repo's agent directory and a person's
// need not be the same one.
func assetsFor(e env) (project, global userdirs.Root) {
	return userdirs.At(e.cwd), userdirs.At(e.home)
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
	if explicitPath != "" {
		return config.Load(explicitPath)
	}
	return config.LoadMerged(e.cwd)
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

// workspaceRules is the AGENTS.md/CLAUDE.md loader a Loop is given.
//
// Named rather than written inline at each call site, because there are
// two of them now — the daemon and "localcode run" — and "what the
// project's rules are" must not be able to differ between them.
func workspaceRules(e env) func(string) string {
	return func(dir string) string { return rules.Load(dir, e.home) }
}
