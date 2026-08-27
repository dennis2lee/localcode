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
	if path, err := resolvedConfigPath(configPath); err != nil {
		// Not fatal: "always allow" just falls back to session-only
		// approvals (ConfigPath == "" disables persisting), same as
		// today's behavior before this feature existed.
		log.Printf("permission: could not resolve a config.json path for \"always allow\", falling back to session-only approvals: %v", err)
	} else {
		broker.ConfigPath = path
	}
	registry, err := buildRegistry(cfg, broker)
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
			cleanup = mcpManager.Close
		}
	}

	loop := agent.New(store, registry, providers, cfg)
	loop.SkillsSection = skillsSection
	loop.MemoryPolicy = memoryPolicy
	loop.MemorySection = memorySection
	loop.Skills = skillList
	loop.Commands = cmdList
	loop.ProjectDir = e.cwd
	loop.MemoryDir = memDir
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
	loop.WorkspaceRules = func(dir string) string { return rules.Load(dir, e.home) }
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

	// The delegation tools. Registered unconditionally, and hidden per
	// turn instead — see Loop.hiddenDelegationTools. They used to be
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

	// The trace file is closed with everything else. Wrapped rather than
	// assigned, because cleanup may already be the MCP manager's.
	if loop.Trace != nil {
		mcpCleanup := cleanup
		cleanup = func() {
			mcpCleanup()
			loop.Trace.Close()
		}
	}

	d := daemon.New(loop, broker, tasks, mcpManager, daemon.WebFS(), version)

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
func buildRegistry(cfg *config.Config, broker *agent.PermissionBroker) (*tools.Registry, error) {
	registry := tools.NewRegistry(broker.Func())
	// The pipeline's order lives in ComposeResolver, where it is a
	// stated contract with its own test: rules and guards, then the
	// workspace boundary, then skip_permissions over whatever ask is
	// left. The boundary reads Smart Agent off ctx, so a tool call is
	// judged by the rules its own turn was admitted under rather than
	// by whatever the switch says at the moment it runs.
	registry.Resolver = tools.ComposeResolver(
		func(ctx context.Context, toolName, subject string, staticRequiresPermission bool) tools.Decision {
			return tools.Decision(cfg.ResolvePermissionFor(ctx, toolName, subject, staticRequiresPermission))
		},
		cfg.SmartAgentFor,
		cfg.PermissionsSkipped,
	)
	registry.Hooks = cfg.Hooks
	registry.Register(tools.ReadFile{})
	registry.Register(tools.WriteFile{})
	registry.Register(tools.Edit{})
	registry.Register(tools.Bash{})
	registry.Register(tools.Glob{})
	registry.Register(tools.Grep{})
	return registry, nil
}

// buildSystemPrompt loads skills, custom commands, project rules, and the
// auto-memory section, registers the Skill tool if any skills were found,
// and returns the combined text to append to Loop.SystemPrompt alongside
// the loaded skills/commands/memory-dir Loop needs directly.
func buildSystemPrompt(cfg *config.Config, registry *tools.Registry, e env) (skillsSection, memoryPolicy, memorySection string, skillList []skills.Skill, cmdList []commands.Command, memDir string, err error) {
	skillList, err = loadSkills(e)
	if err != nil {
		return "", "", "", nil, nil, "", err
	}
	if len(skillList) > 0 {
		registry.Register(tools.NewSkillTool(skillList))
		skillsSection = skills.SystemPromptSection(skillList)
	}

	cmdList, err = commands.LoadAll(filepath.Join(e.cwd, ".localcode", "commands"), filepath.Join(e.home, ".localcode", "commands"))
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
// project can override a same-named global skill.
func loadSkills(e env) ([]skills.Skill, error) {
	return skills.LoadAll(
		filepath.Join(e.cwd, ".localcode", "skills"),
		filepath.Join(e.home, ".localcode", "skills"),
	)
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
