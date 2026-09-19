package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
//
// The second return is what the files said that localcode accepted and did
// not act on — opencode's spellings for things it has no equivalent of, in
// opencode's own dotted paths, so the person can grep their own file for
// the string. A caller with nowhere to print it writes `_`, and there is
// deliberately no second function that drops it for them: a wrapper that
// hides this was the one thing nobody called the moment the daemon started
// printing it.
func LoadMerged(projectDir string) (*Config, []string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve home dir: %w", err)
	}
	return loadMergedFrom(configSources(home, projectDir, os.Getenv("OPENCODE_CONFIG")))
}

// loadMergedFrom is LoadMerged with the list of files handed to it, so
// the order can be exercised without a home directory to arrange.
func loadMergedFrom(paths []string) (*Config, []string, error) {
	var cfg *Config
	var notes []string
	var read []string

	for _, path := range paths {
		// opencode writes .jsonc when it wants comments in the file, and
		// a person who did that should not find it unread.
		if alt := jsoncAlternative(path); alt != "" {
			aThere, bThere := exists(path), exists(alt)
			switch {
			case aThere && bThere:
				return nil, nil, bothSpellings(path, alt)
			case bThere:
				path = alt
			}
		}
		one, oneNotes, err := loadOptional(path)
		if err != nil {
			return nil, nil, err
		}
		if one == nil {
			continue
		}
		read = append(read, path)
		notes = append(notes, oneNotes...)
		if cfg == nil {
			cfg = one
			continue
		}
		cfg.merge(one)
	}

	if cfg == nil {
		return nil, nil, fmt.Errorf("no config found at any of: %s", strings.Join(paths, ", "))
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid merged config: %w", err)
	}

	seen := make(map[string]bool)
	out := notes[:0]
	for _, n := range notes {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return cfg, out, nil
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Load reads and validates a single config file from path. Its second
// return is LoadMerged's, for the same reason.
func Load(path string) (*Config, []string, error) {
	cfg, notes, err := loadOptional(path)
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil {
		return nil, nil, fmt.Errorf("config file not found: %s", path)
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, notes, nil
}

// LoadFile reads a single config file for editing (e.g. by `localcode
// mcp`). Unlike Load, a missing file is not an error — it returns an
// empty, unvalidated Config ready to be filled in and saved.
func LoadFile(path string) (*Config, error) {
	cfg, _, err := loadOptional(path)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return &Config{}, nil
	}
	return cfg, nil
}

func loadOptional(path string) (*Config, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read config %s: %w", path, err)
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
		return nil, nil, fmt.Errorf("config %s: %w", path, err)
	}
	norm, err := NormalizeOpencode(expanded)
	if err != nil {
		return nil, nil, fmt.Errorf("config %s: %w", path, err)
	}
	// Once more, and only for what the normaliser wrote itself: opencode
	// names the variable holding a key rather than the key, and the
	// placeholder that turns into is written after the first pass has
	// already run. See Normalized.MadePlaceholders.
	finalJSON := norm.JSON
	if norm.MadePlaceholders {
		finalJSON, err = expandEnv(finalJSON, osLookup)
		if err != nil {
			return nil, nil, fmt.Errorf("config %s: %w", path, err)
		}
	}
	var cfg Config
	if err := json.Unmarshal(finalJSON, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &cfg, norm.Ignored, nil
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
	if other.Shell != "" {
		c.Shell = other.Shell
	}
	if other.DefaultAgent != "" {
		c.DefaultAgent = other.DefaultAgent
	}
	if other.MaxConcurrentTasks != 0 {
		c.MaxConcurrentTasks = other.MaxConcurrentTasks
	}
	if other.SubagentDepth != nil {
		c.SubagentDepth = other.SubagentDepth
	}
	if other.Instructions != nil {
		c.Instructions = other.Instructions
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
	if other.ShowThinking != nil {
		c.ShowThinking = other.ShowThinking
	}
	if other.ShowTimestamps != nil {
		c.ShowTimestamps = other.ShowTimestamps
	}
	// A project where the mouse stays with the terminal (shared
	// editing over a multiplexer, say) can say so, the same way it
	// can for the display switches beside this one.
	if other.Mouse != nil {
		c.Mouse = other.Mouse
	}
	if other.AutoDelegate != nil {
		c.AutoDelegate = other.AutoDelegate
	}
	// Wholesale rather than field by field, the way AutoDelegate is: an
	// allow list is a set, and merging two of them would produce a third
	// nobody wrote — the project quietly widening what the home
	// directory permits is the wrong direction for this particular
	// setting to be wrong in.
	if other.Network != nil {
		c.Network = other.Network
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
