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
	"localcode/internal/provider"
	"localcode/internal/rules"
	"localcode/internal/session"
	"localcode/internal/skills"
	"localcode/internal/tools"
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
func buildDaemon(ctx context.Context, configPath string) (*daemon.Daemon, func(), error) {
	e, err := resolveEnv()
	if err != nil {
		return nil, nil, err
	}

	cfg, err := loadConfig(configPath, e)
	if err != nil {
		return nil, nil, err
	}

	providers, err := buildProviders(ctx, cfg, e)
	if err != nil {
		return nil, nil, err
	}

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

	systemPromptExtra, skillList, cmdList, memDir, err := buildSystemPrompt(cfg, registry, e)
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
		mcpManager, mcpTools, warnings = mcpclient.Connect(ctx, cfg.MCPServers)
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
	loop.SystemPrompt += systemPromptExtra
	loop.Skills = skillList
	loop.Commands = cmdList
	loop.ProjectDir = e.cwd
	loop.MemoryDir = memDir
	// Restores conversation history and /usage totals for every session
	// just loaded from disk — the event log survives a restart on its
	// own, but Loop's in-memory history/usage maps don't, so without this
	// a resumed session would replay its old transcript on screen while
	// the model itself had no memory of any of it.
	loop.RehydrateAll()
	tasks := agent.NewTaskManager(ctx, loop, cfg.MaxConcurrentTasks)

	// The Task tool only makes sense once there's more than one agent role
	// to delegate to — with a single agent it'd just be a slower way to
	// call yourself. Registered after the TaskManager exists (it needs
	// one), but registry is a live pointer already shared with loop, so
	// this still takes effect before any SendMessage call.
	if len(cfg.Agents) > 1 {
		registry.Register(agent.NewTaskTool(tasks, cfg.Agents))
	}

	return daemon.New(loop, broker, tasks, mcpManager, daemon.WebFS(), version), cleanup, nil
}

// buildRegistry constructs the tool registry and registers every built-in
// tool, wiring the permission broker and per-tool decision resolver from
// cfg.
func buildRegistry(cfg *config.Config, broker *agent.PermissionBroker) (*tools.Registry, error) {
	registry := tools.NewRegistry(broker.Func())
	registry.Resolver = func(toolName, subject string, staticRequiresPermission bool) tools.Decision {
		return tools.Decision(cfg.ResolvePermission(toolName, subject, staticRequiresPermission))
	}
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
func buildSystemPrompt(cfg *config.Config, registry *tools.Registry, e env) (extra string, skillList []skills.Skill, cmdList []commands.Command, memDir string, err error) {
	skillList, err = loadSkills(e)
	if err != nil {
		return "", nil, nil, "", err
	}
	if len(skillList) > 0 {
		registry.Register(tools.NewSkillTool(skillList))
		extra += "\n\n" + skills.SystemPromptSection(skillList)
	}

	cmdList, err = commands.LoadAll(filepath.Join(e.cwd, ".localcode", "commands"), filepath.Join(e.home, ".localcode", "commands"))
	if err != nil {
		return "", nil, nil, "", err
	}

	if rulesSection := rules.Load(e.cwd, e.home); rulesSection != "" {
		extra += "\n\n" + rulesSection
	}

	if cfg.MemoryEnabled() {
		memDir = memory.Dir(e.cwd, e.home)
		if err := os.MkdirAll(memDir, 0o755); err != nil {
			return "", nil, nil, "", fmt.Errorf("create memory dir: %w", err)
		}
		extra += "\n\n" + memory.SystemPromptSection(memDir, memory.LoadIndex(memDir))
	}

	return extra, skillList, cmdList, memDir, nil
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
			b, err := provider.NewBedrock(ctx, pc.Region, pc.Profile)
			if err != nil {
				return nil, fmt.Errorf("init bedrock provider %q: %w", name, err)
			}
			out[name] = b
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
