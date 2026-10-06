package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// TestUpdateRawConfigWritesMode0600 pins that every writer built on
// updateRawConfig creates the file at 0o600, not the world/group-readable
// 0o644 the ad hoc writers used before — this file can hold provider API
// keys and MCP auth headers.
func TestUpdateRawConfigWritesMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode bits don't apply on Windows")
	}
	path := filepath.Join(t.TempDir(), "config.json")

	if err := updateRawConfig(path, func(raw map[string]json.RawMessage) error {
		raw["default_profile"] = json.RawMessage(`"main"`)
		return nil
	}); err != nil {
		t.Fatalf("updateRawConfig: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
}

// TestUpdateRawConfigMutateErrorLeavesFileUntouched pins that a mutate
// error aborts before anything is written — the whole point of returning an
// error from the callback (e.g. "mcp server not found") is that it must not
// also rewrite (reformat, or worse, corrupt) the file as a side effect.
func TestUpdateRawConfigMutateErrorLeavesFileUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{"default_profile": "main"}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	wantErr := errors.New("boom")
	err := updateRawConfig(path, func(raw map[string]json.RawMessage) error {
		raw["default_profile"] = json.RawMessage(`"changed"`)
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("updateRawConfig error = %v, want %v", err, wantErr)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != original {
		t.Errorf("file = %s, want the original left untouched after a mutate error", data)
	}

	// No stray temp file left behind in the directory either.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("dir entries = %v, want only config.json (no leftover temp file)", entries)
	}
}

// TestUpdateRawConfigRoundTripsThroughRename confirms the happy path still
// produces valid, readable JSON after the switch to temp-file+rename.
func TestUpdateRawConfigRoundTripsThroughRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")

	if err := updateRawConfig(path, func(raw map[string]json.RawMessage) error {
		raw["default_profile"] = json.RawMessage(`"main"`)
		return nil
	}); err != nil {
		t.Fatalf("updateRawConfig: %v", err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.DefaultProfile != "main" {
		t.Errorf("DefaultProfile = %q, want main", cfg.DefaultProfile)
	}
}

// The loader reads "mcp" and "mcpServers" as one list with mcp_servers, so
// the writer has to. Otherwise "mcp remove" cannot find a server the loader
// sees, and "mcp add" starts a second block beside it.
func TestTheMCPWriterSeesServersWrittenUnderAnotherSpelling(t *testing.T) {
	for _, spelling := range []string{"mcp", "mcpServers"} {
		t.Run(spelling, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			body := `{"keep":"me","` + spelling + `":{"old":{"command":"old-cmd"},"other":{"command":"other-cmd"}}}`
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			// remove a server that lives under the other spelling
			if err := UpdateMCPServersInFile(path, func(servers map[string]MCPServerConfig) error {
				if _, ok := servers["old"]; !ok {
					return errors.New("old is not in the list the writer sees")
				}
				delete(servers, "old")
				servers["new"] = MCPServerConfig{Command: "new-cmd"}
				return nil
			}); err != nil {
				t.Fatalf("UpdateMCPServersInFile: %v", err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if _, still := doc[spelling]; still {
				t.Errorf("the %q block is still there beside mcp_servers: %s", spelling, data)
			}
			if string(doc["keep"]) != `"me"` {
				t.Errorf("another key was changed: %s", data)
			}
			var servers map[string]MCPServerConfig
			if err := json.Unmarshal(doc["mcp_servers"], &servers); err != nil {
				t.Fatal(err)
			}
			if len(servers) != 2 || servers["other"].Command != "other-cmd" || servers["new"].Command != "new-cmd" {
				t.Errorf("servers = %+v, want other and new only", servers)
			}
		})
	}
}

func TestTheMCPWriterRefusesAServerNamedUnderTwoSpellings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	before := `{"mcp_servers":{"same":{"command":"a"}},"mcpServers":{"same":{"command":"b"}}}`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	err := UpdateMCPServersInFile(path, func(map[string]MCPServerConfig) error { return nil })
	if err == nil {
		t.Fatal("one server named under two spellings was accepted, so one of them would be dropped")
	}
	after, _ := os.ReadFile(path)
	if string(after) != before {
		t.Errorf("the file was changed by a refused write: %s", after)
	}
}

// A rewrite that changes nothing must not change what the loader sees. The
// entries below are what each program writes: opencode's command array and
// environment map, Claude Code's type "http" with headers, and localcode's
// own shape. They are read from one spelling and written under mcp_servers,
// so a field the writer did not carry across would be a server that starts
// differently after "mcp add" on some other server.
func TestTheMCPWriterRewritesEveryShapeWithoutChangingWhatTheLoaderSees(t *testing.T) {
	blocks := map[string]string{
		"mcp": `{"files":{"type":"local","command":["npx","-y","pkg","--flag"],"environment":{"K":"V"},"enabled":false},` +
			`"docs":{"type":"remote","url":"https://example.test/mcp","headers":{"Authorization":"Bearer x"},"timeout":4000}}`,
		"mcpServers": `{"files":{"command":"npx","args":["-y","pkg"],"env":{"K":"V"}},` +
			`"docs":{"type":"http","url":"https://example.test/mcp","headers":{"Authorization":"Bearer x"}},` +
			`"events":{"type":"sse","url":"https://example.test/sse"}}`,
		"mcp_servers": `{"files":{"command":"npx","args":["-y","pkg"],"env":{"K":"V"},"cwd":"/work","enabled":true}}`,
	}
	for spelling, block := range blocks {
		t.Run(spelling, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			body := strings.TrimSuffix(workingLocalcode, "}") + `,"` + spelling + `":` + block + `}`
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			before, _, err := Load(path)
			if err != nil {
				t.Fatalf("the file does not load before the rewrite: %v", err)
			}
			if len(before.MCPServers) == 0 {
				t.Fatalf("no servers were read from %q", spelling)
			}

			if err := UpdateMCPServersInFile(path, func(map[string]MCPServerConfig) error { return nil }); err != nil {
				t.Fatalf("UpdateMCPServersInFile: %v", err)
			}
			after, _, err := Load(path)
			if err != nil {
				data, _ := os.ReadFile(path)
				t.Fatalf("the file does not load after the rewrite: %v\n%s", err, data)
			}
			if !reflect.DeepEqual(before.MCPServers, after.MCPServers) {
				data, _ := os.ReadFile(path)
				t.Errorf("the rewrite changed the servers\nbefore: %+v\nafter:  %+v\nfile:   %s", before.MCPServers, after.MCPServers, data)
			}
		})
	}
}
