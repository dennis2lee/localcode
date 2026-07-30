package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"localcode/internal/config"
	mcpclient "localcode/internal/mcp"
)

// mcpPingTimeout bounds one server's connectivity check in `mcp list`. It
// covers process start plus the MCP handshake and a tools/list round trip;
// servers that fetch something over the network at startup (an `npx` package
// that isn't cached yet, say) are the slow case this has to leave room for.
const mcpPingTimeout = 20 * time.Second

func mcpList(args []string) error {
	test := true
	for _, a := range args {
		switch a {
		case "--no-test", "--no-connect":
			test = false
		default:
			return fmt.Errorf("unknown argument %q (usage: localcode mcp list [--no-test])", a)
		}
	}

	global, project, _, _, err := loadBothScopes()
	if err != nil {
		return err
	}
	if len(global.MCPServers) == 0 && len(project.MCPServers) == 0 {
		fmt.Println("No MCP servers registered. Add one with `localcode mcp add`.")
		return nil
	}

	names := map[string]bool{}
	for n := range global.MCPServers {
		names[n] = true
	}
	for n := range project.MCPServers {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	// One line per server: name, where it is registered, and whether it
	// answers. The command line, url, env/header keys, and the config file
	// path are all deliberately absent — this is a status view, and
	// `mcp get <name>` is the place that shows a server's full definition.
	width := 0
	for _, n := range sorted {
		if len(n) > width {
			width = len(n)
		}
	}

	failures := 0
	for _, n := range sorted {
		sc, inProject := project.MCPServers[n]
		scope := "global"
		if inProject {
			scope = "project"
		} else {
			sc = global.MCPServers[n]
		}

		if !test {
			fmt.Printf("%-*s  %s\n", width, n, scope)
			continue
		}
		ok, status := checkMCPServer(sc)
		if !ok {
			failures++
		}
		fmt.Printf("%-*s  %-8s  %s\n", width, n, scope, status)
	}

	if test && failures > 0 {
		// Reported, not returned as an error: the listing itself succeeded,
		// and a server being down is information about that server rather
		// than a failure of the command the user ran.
		fmt.Printf("\n%d of %d server(s) failed to connect.\n", failures, len(sorted))
	}
	return nil
}

// checkMCPServer brings one server up for real and returns whether it
// answered, plus a short status phrase for the listing. Registration in
// config.json says nothing about whether the command exists, the endpoint is
// reachable, or either speaks MCP — which is the whole point of connecting
// rather than trusting the file.
func checkMCPServer(sc config.MCPServerConfig) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), mcpPingTimeout)
	defer cancel()

	toolNames, err := mcpclient.Ping(ctx, sc)
	if err != nil {
		return false, "failed: " + oneLine(err.Error())
	}
	if len(toolNames) == 0 {
		return true, "ok (no tools advertised)"
	}
	return true, fmt.Sprintf("ok (%d tool%s)", len(toolNames), plural(len(toolNames)))
}

func mcpGet(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: localcode mcp get <name>")
	}
	name := args[0]

	global, project, globalPath, projectPath, err := loadBothScopes()
	if err != nil {
		return err
	}

	if sc, ok := project.MCPServers[name]; ok {
		printMCPServerDetail(name, "project", projectPath, sc)
		if _, alsoGlobal := global.MCPServers[name]; alsoGlobal {
			fmt.Println("  (a global setting also exists, but the project setting takes priority)")
		}
		return nil
	}
	if sc, ok := global.MCPServers[name]; ok {
		printMCPServerDetail(name, "global", globalPath, sc)
		return nil
	}
	return fmt.Errorf("mcp server %q not found", name)
}

func printMCPServerDetail(name, scope, path string, sc config.MCPServerConfig) {
	fmt.Printf("%s  [%s]  (%s)\n", name, scope, path)
	fmt.Printf("  transport: %s\n", sc.Transport())
	if sc.IsRemote() {
		fmt.Printf("  url:       %s\n", sc.URL)
		// Names only. A header value is almost always a bearer token, and
		// this output gets pasted into issues and chat windows.
		if keys := sortedKeys(sc.Headers); len(keys) > 0 {
			fmt.Printf("  headers:   %s (values hidden)\n", strings.Join(keys, ", "))
		}
		return
	}
	fmt.Printf("  command:   %s\n", sc.Command)
	if len(sc.Args) > 0 {
		fmt.Printf("  args:      %s\n", strings.Join(sc.Args, " "))
	}
	for _, k := range sortedKeys(sc.Env) {
		fmt.Printf("  env:       %s=%s\n", k, sc.Env[k])
	}
}
