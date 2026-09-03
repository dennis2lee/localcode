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
	"sort"
	"strings"

	"localcode/internal/gui"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

// subcommands are dispatched before any flag parsing happens, the same way
// "git <subcommand>" or "go <subcommand>" works — each owns its own
// argument syntax rather than sharing run()'s flag.FlagSet.
var subcommands = map[string]func(args []string) error{
	"login":   runLogin,
	"mcp":     runMCP,
	"run":     runOneShot,
	"version": runVersionCommand,
}

// subcommandNames lists what main dispatches on, for the error above.
// Sorted so the message does not change shape between runs.
func subcommandNames() string {
	names := make([]string, 0, len(subcommands))
	for name := range subcommands {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// defaultAddr is where a daemon listens unless told otherwise, and
// therefore where anything looking for one looks first. Named because two
// places now need to agree on it: the daemon that binds it, and
// "localcode run --session", which asks whether anybody already has.
const defaultAddr = "127.0.0.1:4096"

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
	listen := flag.String("listen", defaultAddr, "address the daemon listens on (also where the Web UI is served)")
	server := flag.String("server", "", "connect the TUI to an already-running daemon at this URL instead of starting one locally (e.g. http://localhost:4096, or an SSH-tunneled remote core)")
	headless := flag.Bool("headless", false, "run only the daemon (HTTP API + Web UI), no TUI — for a remote box you'll attach to over SSH or the network")
	useGUI := flag.Bool("gui", gui.Available(), "open a native desktop window instead of the TUI (macOS and Windows only, and only in a build made with -tags gui; defaults to on for such a build, pass --gui=false to force the TUI instead). There is no desktop window on Linux: run without it and open the Web UI in a browser")
	showVersion := flag.Bool("version", false, "print version and exit (same as the \"localcode version\" subcommand)")
	flag.Parse()

	// A word that is not a flag and not a subcommand is a mistake, and it
	// used to be an invisible one: flag.Parse ignores leftovers, so
	// "localcode mpc add" — or any subcommand this build is too
	// old to have — started the agent exactly as if nothing had been
	// typed. The TUI comes up, the command never runs, and there is
	// nothing on screen to say why.
	if flag.NArg() > 0 {
		return fmt.Errorf("unknown command %q\n\nsubcommands: %s\nrun \"localcode --help\" for flags",
			flag.Arg(0), subcommandNames())
	}

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	// A process started by a handoff serves the listener it was handed,
	// whatever mode its arguments name: a successor to a desktop window
	// is started with the window's arguments and must not open a window,
	// and one started from a headless daemon must not bind a second port.
	if in, ok := takingOver(); ok {
		return runSuccessor(*configPath, in)
	}
	if *headless {
		return runDaemon(*configPath, *listen)
	}
	if *useGUI {
		return runGUI(*configPath)
	}
	printBanner()
	if *server != "" {
		// No restart hook: this TUI is attached to a daemon somewhere else,
		// and that daemon is not ours to replace.
		return runTUIClient(*server, *agentName, nil)
	}
	// Whether --listen was typed, not just what it holds. An address
	// somebody asked for by name is a request; the default is a
	// convention, and a convention can move out of the way when
	// something else already has the port.
	listenExplicit := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "listen" {
			listenExplicit = true
		}
	})
	return runEmbedded(*configPath, *listen, *agentName, listenExplicit)
}
