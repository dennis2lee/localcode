// Command localcode is the entrypoint for both the core daemon and its
// clients. By default it starts an embedded daemon on a loopback port and
// attaches a TUI to it (so a Web UI can attach to the same port too); pass
// --headless to run the daemon alone, or --server to attach a TUI to an
// already-running daemon instead of starting a local one.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"localcode/internal/agent"
	"localcode/internal/client"
	"localcode/internal/config"
	"localcode/internal/daemon"
	mcpclient "localcode/internal/mcp"
	"localcode/internal/provider"
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
	flag.Parse()

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

	if len(cfg.MCPServers) > 0 {
		_, mcpTools, err := mcpclient.Connect(ctx, cfg.MCPServers)
		if err != nil {
			return nil, fmt.Errorf("connect mcp servers: %w", err)
		}
		for _, t := range mcpTools {
			registry.Register(t)
		}
		// The Manager (session handles) is intentionally not tracked for a
		// clean shutdown here: this MVP has no signal handling yet, and the
		// child MCP server processes exit when this process does.
	}

	loop := agent.New(store, registry, providers, cfg)
	loop.SystemPrompt += skillPromptSection
	tasks := agent.NewTaskManager(ctx, loop, cfg.MaxConcurrentTasks)

	return daemon.New(loop, broker, tasks, daemon.WebFS()), nil
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
	sess, err := c.CreateSession(ctx, agentName)
	if err != nil {
		return fmt.Errorf("create session on %s: %w", serverURL, err)
	}

	eventCh, err := c.SubscribeEvents(ctx, sess.ID, 0)
	if err != nil {
		return fmt.Errorf("subscribe to events: %w", err)
	}

	model := tui.New(c, sess.ID, eventCh)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err = p.Run()
	return err
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
	out := map[string]provider.Provider{}
	for name, pc := range cfg.Providers {
		switch pc.Type {
		case config.ProviderBedrock:
			b, err := provider.NewBedrock(ctx, pc.Region)
			if err != nil {
				return nil, fmt.Errorf("init bedrock provider %q: %w", name, err)
			}
			out[name] = b
		case config.ProviderOpenAICompat:
			out[name] = provider.NewOpenAICompat(pc.BaseURL, pc.APIKey)
		default:
			return nil, fmt.Errorf("provider %q has unknown type %q", name, pc.Type)
		}
	}
	return out, nil
}
