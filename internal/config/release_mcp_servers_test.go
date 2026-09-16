package config

import (
	"sync"
	"testing"
)

// The "/reset-mcp" path replaces Config.MCPServers while the daemon runs.
// These tests pin the locked accessor that replacement goes through. The
// first one only means anything under the race detector: a race the test
// never executes is a race the detector never sees, so it hammers reads
// and writes from many goroutines at once. The gate runs it with `-race`
// (`go test ./... -race` in scripts/check.sh).
func TestMCPServerResetsAreVisibleToConcurrentReaders(t *testing.T) {
	c := &Config{}
	c.SetMCPServersRuntime(map[string]MCPServerConfig{"a": {Command: "a"}})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = c.MCPServersSnapshot()
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c.SetMCPServersRuntime(map[string]MCPServerConfig{"w": {Command: "w"}})
				_ = j
			}
			_ = i
		}(i)
	}
	wg.Wait()

	final := c.MCPServersSnapshot()
	if len(final) != 1 || final["w"].Command != "w" {
		t.Fatalf("final snapshot = %v, want the last written map", final)
	}
}

// The snapshot is a copy: working with what it returns must not move the
// live map, and neither must reusing the map that was passed in.
func TestMCPServersSnapshotIsACopy(t *testing.T) {
	c := &Config{}
	in := map[string]MCPServerConfig{"a": {Command: "a"}}
	c.SetMCPServersRuntime(in)

	in["a"] = MCPServerConfig{Command: "mutated after set"}
	in["b"] = MCPServerConfig{Command: "added after set"}
	if got := c.MCPServersSnapshot(); len(got) != 1 || got["a"].Command != "a" {
		t.Fatalf("snapshot after input reuse = %v, want the map as passed in", got)
	}

	out := c.MCPServersSnapshot()
	out["a"] = MCPServerConfig{Command: "mutated snapshot"}
	delete(out, "a")
	out["c"] = MCPServerConfig{Command: "added snapshot"}
	if got := c.MCPServersSnapshot(); len(got) != 1 || got["a"].Command != "a" {
		t.Fatalf("snapshot after output reuse = %v, want the live map unchanged", got)
	}
}

// Both "/reset-mcp" assignment sites in wire.go go through the one setter:
// an empty re-read clears the roster, and a populated one replaces it.
func TestBothResetPathsReplaceTheRoster(t *testing.T) {
	c := &Config{}
	c.SetMCPServersRuntime(map[string]MCPServerConfig{
		"a": {Command: "a"},
		"b": {Command: "b"},
	})
	if got := c.MCPServersSnapshot(); len(got) != 2 {
		t.Fatalf("snapshot = %v, want both servers", got)
	}

	c.SetMCPServersRuntime(map[string]MCPServerConfig{})
	if got := c.MCPServersSnapshot(); len(got) != 0 {
		t.Fatalf("snapshot after empty reset = %v, want no servers", got)
	}

	c.SetMCPServersRuntime(nil)
	if got := c.MCPServersSnapshot(); len(got) != 0 {
		t.Fatalf("snapshot after nil reset = %v, want no servers", got)
	}
}
