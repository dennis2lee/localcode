// Command localcode is the entrypoint for both the core daemon and its
// clients. By default it starts an embedded daemon on a loopback port and
// attaches a TUI to it (so a Web UI can attach to the same port too); pass
// --headless to run the daemon alone, or --server to attach a TUI to an
// already-running daemon instead of starting a local one.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"localcode/internal/agent"
	"localcode/internal/client"
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
	"localcode/internal/tui"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "login" {
		if err := runLogin(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		if err := runMCP(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "path to a single config.json (default: merge ~/.localcode/config.json + ./.localcode/config.json)")
	agentName := flag.String("agent", "general-purpose", "agent/task type name to resolve a model profile for")
	listen := flag.String("listen", "127.0.0.1:4096", "address the daemon listens on (also where the Web UI is served)")
	server := flag.String("server", "", "connect the TUI to an already-running daemon at this URL instead of starting one locally (e.g. http://localhost:4096, or an SSH-tunneled remote core)")
	headless := flag.Bool("headless", false, "run only the daemon (HTTP API + Web UI), no TUI — for a remote box you'll attach to over SSH or the network")
	showVersion := flag.Bool("version", false, "print version and exit (same as the \"localcode version\" subcommand)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	if *headless {
		return runDaemon(*configPath, *listen)
	}
	if *server != "" {
		return runTUIClient(*server, *agentName)
	}
	return runEmbedded(*configPath, *listen, *agentName)
}

// buildDaemon wires config -> providers -> tools -> agent.Loop -> Task
// Manager -> daemon.Daemon. Shared by both --headless and the default
// embedded-daemon path.
func buildDaemon(ctx context.Context, configPath string) (*daemon.Daemon, error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, err
	}

	providers, err := buildProviders(ctx, cfg)
	if err != nil {
		return nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	sessionDir := filepath.Join(home, ".localcode", "sessions")
	store, err := session.NewStore(sessionDir)
	if err != nil {
		return nil, err
	}

	broker := agent.NewPermissionBroker(store)
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

	skillList, err := loadSkills(home)
	if err != nil {
		return nil, err
	}
	var skillPromptSection string
	if len(skillList) > 0 {
		registry.Register(tools.NewSkillTool(skillList))
		skillPromptSection = "\n\n" + skills.SystemPromptSection(skillList)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	cmdList, err := commands.LoadAll(filepath.Join(cwd, ".localcode", "commands"), filepath.Join(home, ".localcode", "commands"))
	if err != nil {
		return nil, err
	}
	rulesSection := rules.Load(cwd, home)

	var memDir, memorySection string
	if cfg.MemoryEnabled() {
		memDir = memory.Dir(cwd, home)
		if err := os.MkdirAll(memDir, 0o755); err != nil {
			return nil, fmt.Errorf("create memory dir: %w", err)
		}
		memorySection = memory.SystemPromptSection(memDir, memory.LoadIndex(memDir))
	}

	var mcpManager *mcpclient.Manager
	if len(cfg.MCPServers) > 0 {
		// A server that fails to connect or list tools is skipped (logged as
		// a warning), not fatal: one bad MCP server shouldn't take down the
		// whole daemon. The Manager is kept (for GET /api/mcp-servers) but
		// not otherwise tracked for a clean shutdown — this MVP has no
		// signal handling yet, and the child MCP server processes exit when
		// this process does.
		var mcpTools []tools.Tool
		var warnings []error
		mcpManager, mcpTools, warnings = mcpclient.Connect(ctx, cfg.MCPServers)
		for _, w := range warnings {
			log.Printf("mcp: %v", w)
		}
		for _, t := range mcpTools {
			registry.Register(t)
		}
	}

	loop := agent.New(store, registry, providers, cfg)
	loop.SystemPrompt += skillPromptSection
	if rulesSection != "" {
		loop.SystemPrompt += "\n\n" + rulesSection
	}
	if memorySection != "" {
		loop.SystemPrompt += "\n\n" + memorySection
	}
	loop.Skills = skillList
	loop.Commands = cmdList
	loop.ProjectDir = cwd
	loop.MemoryDir = memDir
	tasks := agent.NewTaskManager(ctx, loop, cfg.MaxConcurrentTasks)

	// The Task tool only makes sense once there's more than one agent role
	// to delegate to — with a single agent it'd just be a slower way to
	// call yourself. Registered after the TaskManager exists (it needs
	// one), but registry is a live pointer already shared with loop, so
	// this still takes effect before any SendMessage call.
	if len(cfg.Agents) > 1 {
		registry.Register(agent.NewTaskTool(tasks, cfg.Agents))
	}

	return daemon.New(loop, broker, tasks, mcpManager, daemon.WebFS(), version), nil
}

// loadSkills scans the project-local skills dir (if run from within a
// project) before the global one, so a project can override a same-named
// global skill.
func loadSkills(home string) ([]skills.Skill, error) {
	var dirs []string
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(cwd, ".localcode", "skills"))
	}
	dirs = append(dirs, filepath.Join(home, ".localcode", "skills"))
	return skills.LoadAll(dirs...)
}

func runDaemon(configPath, listen string) error {
	d, err := buildDaemon(context.Background(), configPath)
	if err != nil {
		return err
	}
	log.Printf("localcode daemon listening on http://%s", listen)
	return http.ListenAndServe(listen, d.Handler())
}

// runEmbedded starts a daemon in-process (so a browser can also point at
// the same --listen address for the Web UI) and attaches a TUI client to
// it over real HTTP/SSE — the TUI and daemon are still separate,
// independently-addressable components, just sharing a process for
// single-binary convenience.
func runEmbedded(configPath, listen, agentName string) error {
	d, err := buildDaemon(context.Background(), configPath)
	if err != nil {
		return err
	}

	srv := &http.Server{Addr: listen, Handler: d.Handler()}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	// Give the listener a moment to come up before the client dials it.
	select {
	case err := <-errCh:
		return fmt.Errorf("daemon failed to start: %w", err)
	case <-time.After(150 * time.Millisecond):
	}

	return runTUIClient("http://"+listen, agentName)
}

func runTUIClient(serverURL, agentName string) error {
	c := client.New(serverURL)

	ctx := context.Background()
	sess, err := pickOrCreateSession(ctx, c, agentName)
	if err != nil {
		return fmt.Errorf("create session on %s: %w", serverURL, err)
	}

	eventCh, err := c.SubscribeEvents(ctx, sess.ID, 0)
	if err != nil {
		return fmt.Errorf("subscribe to events: %w", err)
	}

	model := tui.New(c, sess.ID, sess.Agent, eventCh)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// pickOrCreateSession lists existing (visible, resumable) sessions on the
// daemon and, if any exist, prompts on stdin before the TUI takes over the
// screen. This runs before tea.NewProgram's alt-screen, so plain
// stdin/stdout is fine here. A listing failure or an empty list falls
// back to creating a new session without prompting.
func pickOrCreateSession(ctx context.Context, c *client.Client, agentName string) (session.Session, error) {
	sessions, err := c.ListSessions(ctx)
	if err != nil || len(sessions) == 0 {
		return c.CreateSession(ctx, agentName)
	}

	fmt.Println("이어서 사용할 세션을 선택하세요:")
	for i, s := range sessions {
		fmt.Printf("  [%d] %s  (%s, %s)\n", i+1, s.ID, s.Agent, s.CreatedAt.Local().Format("2006-01-02 15:04"))
	}
	fmt.Print("  [n] 새 세션 시작\n선택 (번호 또는 n, 기본값 n): ")

	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)

	if line == "" || strings.EqualFold(line, "n") {
		return c.CreateSession(ctx, agentName)
	}
	idx, err := strconv.Atoi(line)
	if err != nil || idx < 1 || idx > len(sessions) {
		fmt.Println("잘못된 입력입니다 — 새 세션을 시작합니다.")
		return c.CreateSession(ctx, agentName)
	}
	return sessions[idx-1], nil
}

func loadConfig(explicitPath string) (*config.Config, error) {
	if explicitPath != "" {
		return config.Load(explicitPath)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return config.LoadMerged(cwd)
}

func buildProviders(ctx context.Context, cfg *config.Config) (map[string]provider.Provider, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}

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
				creds, err := credentials.Load(home)
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
