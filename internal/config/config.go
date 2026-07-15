// Package config loads and validates the JSON configuration that maps
// agent/task types to model profiles and provider connection details.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the root of ~/.localcode/config.json (global) merged with
// .localcode/config.json (project-local override, same shape).
type Config struct {
	Providers          map[string]ProviderConfig `json:"providers"`
	Profiles           map[string]Profile        `json:"profiles"`
	Agents             map[string]AgentConfig    `json:"agents"`
	DefaultProfile     string                    `json:"default_profile"`
	MaxConcurrentTasks int                       `json:"max_concurrent_tasks"`
	MCPServers         map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
}

// MCPServerConfig launches one MCP server over stdio, same shape as Claude
// Code's .mcp.json `mcpServers` entries (command/args/env) so an existing
// .mcp.json's entries can be copied in directly.
type MCPServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// ProviderConfig describes how to reach a model backend.
// Type selects which concrete client to construct (see provider.Provider).
type ProviderConfig struct {
	Type    ProviderType `json:"type"`               // "bedrock" | "openai-compat"
	Region  string       `json:"region,omitempty"`   // bedrock
	BaseURL string       `json:"base_url,omitempty"` // openai-compat
	APIKey  string       `json:"api_key,omitempty"`  // openai-compat, optional
}

type ProviderType string

const (
	ProviderBedrock      ProviderType = "bedrock"
	ProviderOpenAICompat ProviderType = "openai-compat"
)

// Profile pins a concrete provider+model combination.
type Profile struct {
	Provider    string  `json:"provider"` // key into Config.Providers
	Model       string  `json:"model"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
}

// AgentConfig maps an agent/task type name to a profile.
type AgentConfig struct {
	Profile string `json:"profile"` // key into Config.Profiles
}

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

func loadOptional(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &cfg, nil
}

// merge overlays other on top of c, with other's entries taking priority.
func (c *Config) merge(other *Config) {
	if other == nil {
		return
	}
	for k, v := range other.Providers {
		if c.Providers == nil {
			c.Providers = map[string]ProviderConfig{}
		}
		c.Providers[k] = v
	}
	for k, v := range other.Profiles {
		if c.Profiles == nil {
			c.Profiles = map[string]Profile{}
		}
		c.Profiles[k] = v
	}
	for k, v := range other.Agents {
		if c.Agents == nil {
			c.Agents = map[string]AgentConfig{}
		}
		c.Agents[k] = v
	}
	if other.DefaultProfile != "" {
		c.DefaultProfile = other.DefaultProfile
	}
	if other.MaxConcurrentTasks != 0 {
		c.MaxConcurrentTasks = other.MaxConcurrentTasks
	}
}

// Validate checks that all cross-references (agent -> profile -> provider)
// resolve, so the daemon fails fast at startup rather than mid-task.
func (c *Config) Validate() error {
	if c.DefaultProfile != "" {
		if _, ok := c.Profiles[c.DefaultProfile]; !ok {
			return fmt.Errorf("default_profile %q not found in profiles", c.DefaultProfile)
		}
	}

	for name, profile := range c.Profiles {
		if _, ok := c.Providers[profile.Provider]; !ok {
			return fmt.Errorf("profile %q references unknown provider %q", name, profile.Provider)
		}
	}

	for name, agent := range c.Agents {
		if _, ok := c.Profiles[agent.Profile]; !ok {
			return fmt.Errorf("agent %q references unknown profile %q", name, agent.Profile)
		}
	}

	return nil
}

// ResolveProfile returns the profile to use for a given agent/task type,
// falling back to DefaultProfile when the agent has no explicit mapping.
func (c *Config) ResolveProfile(agentName string) (Profile, error) {
	if agent, ok := c.Agents[agentName]; ok {
		if p, ok := c.Profiles[agent.Profile]; ok {
			return p, nil
		}
	}
	if c.DefaultProfile == "" {
		return Profile{}, fmt.Errorf("no profile for agent %q and no default_profile set", agentName)
	}
	return c.Profiles[c.DefaultProfile], nil
}

// ResolveProvider returns the provider config backing a profile.
func (c *Config) ResolveProvider(profile Profile) (ProviderConfig, error) {
	pc, ok := c.Providers[profile.Provider]
	if !ok {
		return ProviderConfig{}, fmt.Errorf("unknown provider %q", profile.Provider)
	}
	return pc, nil
}
