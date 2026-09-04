package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"localcode/internal/hooks"
)

// DefaultGlobalPath returns ~/.localcode/config.json.
func DefaultGlobalPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".localcode", "config.json"), nil
}

// LoadMerged loads the global config, then merges a project-local
// .localcode/config.json on top (project entries win). Either file may be
// absent; at least one must exist.
func LoadMerged(projectDir string) (*Config, error) {
	globalPath, err := DefaultGlobalPath()
	if err != nil {
		return nil, err
	}

	cfg, err := loadOptional(globalPath)
	if err != nil {
		return nil, err
	}

	projectPath := filepath.Join(projectDir, ".localcode", "config.json")
	projectCfg, err := loadOptional(projectPath)
	if err != nil {
		return nil, err
	}

	switch {
	case cfg == nil && projectCfg == nil:
		return nil, fmt.Errorf("no config found at %s or %s", globalPath, projectPath)
	case cfg == nil:
		cfg = projectCfg
	case projectCfg != nil:
		cfg.merge(projectCfg)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid merged config: %w", err)
	}
	return cfg, nil
}

// Load reads and validates a single config file from path.
func Load(path string) (*Config, error) {
	cfg, err := loadOptional(path)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("config file not found: %s", path)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// LoadFile reads a single config file for editing (e.g. by `localcode
// mcp`). Unlike Load, a missing file is not an error — it returns an
// empty, unvalidated Config ready to be filled in and saved.
func LoadFile(path string) (*Config, error) {
	cfg, err := loadOptional(path)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return &Config{}, nil
	}
	return cfg, nil
}

func loadOptional(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	// Comments out first, so everything after this is ordinary JSON and
	// nothing downstream has to know the file could carry them. Blanked
	// rather than deleted, so a parse error's offset still points at the
	// line it came from. See jsonc.go.
	data = stripComments(data)
	// {env:NAME} next, so every field of every version of this struct
	// gets it without anything here having to know which fields are
	// secrets. See env.go.
	expanded, err := expandEnv(data, osLookup)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &cfg, nil
}

// mergeMap copies every entry of src into *dst, creating *dst if it was nil.
// Used by merge for each of Config's map-typed fields so they don't each
// need their own copy of the same three lines — and so a future map field
// can't quietly reuse copy-pasted merge logic that's subtly wrong.
func mergeMap[K comparable, V any](dst *map[K]V, src map[K]V) {
	if len(src) == 0 {
		return
	}
	if *dst == nil {
		*dst = map[K]V{}
	}
	for k, v := range src {
		(*dst)[k] = v
	}
}

// merge overlays other on top of c, with other's entries taking priority.
//
// Every field of Config must be handled here or it is silently dropped when
// both a global and a project config exist — see TestMergeFieldsGuard,
// which fails if a new Config field is added without a conscious decision
// about how (or whether) it merges.
func (c *Config) merge(other *Config) {
	if other == nil {
		return
	}
	mergeMap(&c.Providers, other.Providers)
	mergeMap(&c.Profiles, other.Profiles)
	mergeMap(&c.Agents, other.Agents)
	mergeMap(&c.MCPServers, other.MCPServers)
	mergeMap(&c.Permissions, other.Permissions)
	if other.DefaultProfile != "" {
		c.DefaultProfile = other.DefaultProfile
	}
	if other.MaxConcurrentTasks != 0 {
		c.MaxConcurrentTasks = other.MaxConcurrentTasks
	}
	if other.AutoMemoryEnabled != nil {
		c.AutoMemoryEnabled = other.AutoMemoryEnabled
	}
	if other.AutoCompactEnabled != nil {
		c.AutoCompactEnabled = other.AutoCompactEnabled
	}
	// Merged, so a project can say "not while working here" — a checkout
	// pinned to a version, or one whose tooling is being bisected, has a
	// real reason not to be moved off the build it was left on.
	if other.AutoUpdate != nil {
		c.AutoUpdate = other.AutoUpdate
	}
	if other.AutoCompactPercent != 0 {
		c.AutoCompactPercent = other.AutoCompactPercent
	}
	if other.KeepGoingEnabled != nil {
		c.KeepGoingEnabled = other.KeepGoingEnabled
	}
	if other.RepeatLimitSteps != nil {
		c.RepeatLimitSteps = other.RepeatLimitSteps
	}
	if other.ShowTPS != nil {
		c.ShowTPS = other.ShowTPS
	}
	if other.AutoDelegate != nil {
		c.AutoDelegate = other.AutoDelegate
	}
	if other.SkipPermissions != nil {
		c.SkipPermissions = other.SkipPermissions
	}
	if other.UpdateURL != "" {
		c.UpdateURL = other.UpdateURL
	}
	// The project's own, and it has to be: how a project is checked is a
	// fact about the project, so a repository's config.json naming its
	// test command must win over whatever is in the home directory.
	if other.VerifyCommand != "" {
		c.VerifyCommand = other.VerifyCommand
	}
	if other.SkipToolPermissions != nil {
		c.SkipToolPermissions = other.SkipToolPermissions
	}
	if other.ReadOutsideWorkspace != nil {
		c.ReadOutsideWorkspace = other.ReadOutsideWorkspace
	}
	if other.WriteOutsideWorkspace != nil {
		c.WriteOutsideWorkspace = other.WriteOutsideWorkspace
	}
	if other.Orchestrate != nil {
		c.Orchestrate = other.Orchestrate
	}
	if other.ModelInvocable != nil {
		c.ModelInvocable = other.ModelInvocable
	}
	// Replaced rather than concatenated. A project naming which built-ins
	// the model may run is answering the question for that project, and
	// appending would let a global list add commands to a project that
	// listed a shorter one deliberately — which is the direction that
	// matters here.
	if len(other.ModelCommands) > 0 {
		c.ModelCommands = other.ModelCommands
	}
	if other.SmartAgent != nil {
		c.SmartAgent = other.SmartAgent
	}
	if other.TraceMaxAgeDays != 0 {
		c.TraceMaxAgeDays = other.TraceMaxAgeDays
	}
	if other.TraceMaxTotalMB != 0 {
		c.TraceMaxTotalMB = other.TraceMaxTotalMB
	}
	for event, list := range other.Hooks {
		if c.Hooks == nil {
			c.Hooks = hooks.Config{}
		}
		c.Hooks[event] = list
	}
}
