// Command localcode is the entrypoint for both the core daemon and its
// clients. By default it starts an embedded daemon on a loopback port and
// attaches a TUI to it (so a Web UI can attach to the same port too); pass
// --headless to run the daemon alone, or --server to attach a TUI to an
// already-running daemon instead of starting one locally.
package main

import (
	"flag"
	"fmt"
	"os"

	"localcode/internal/gui"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

// subcommands are dispatched before any flag parsing happens, the same way
// "git <subcommand>" or "go <subcommand>" works — each owns its own
// argument syntax rather than sharing run()'s flag.FlagSet.
var subcommands = map[string]func(args []string) error{
	"dictation": runDictation,
	"login":     runLogin,
	"mcp":       runMCP,
	"version":   runVersionCommand,
}

func runVersionCommand(args []string) error {
	fmt.Println(version)
	return nil
}

func main() {
	if len(os.Args) > 1 {
		if cmd, ok := subcommands[os.Args[1]]; ok {
			if err := cmd(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return
		}
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
	useGUI := flag.Bool("gui", gui.Available(), "open a native desktop window instead of the TUI (requires a build made with -tags gui; defaults to on for such a build, pass --gui=false to force the TUI instead)")
	showVersion := flag.Bool("version", false, "print version and exit (same as the \"localcode version\" subcommand)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	if *headless {
		return runDaemon(*configPath, *listen)
	}
	if *useGUI {
		return runGUI(*configPath)
	}
	printBanner()
	if *server != "" {
		return runTUIClient(*server, *agentName)
	}
	return runEmbedded(*configPath, *listen, *agentName)
}
