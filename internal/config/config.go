// Package config loads and validates the JSON configuration that maps
// agent/task types to model profiles and provider connection details.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"localcode/internal/hooks"
)

// Config is the root of ~/.localcode/config.json (global) merged with
// .localcode/config.json (project-local override, same shape).
type Config struct {
	Providers          map[string]ProviderConfig  `json:"providers,omitempty"`
	Profiles           map[string]Profile         `json:"profiles,omitempty"`
	Agents             map[string]AgentConfig     `json:"agents,omitempty"`
	DefaultProfile     string                     `json:"default_profile,omitempty"`
	MaxConcurrentTasks int                        `json:"max_concurrent_tasks,omitempty"`
	MCPServers         map[string]MCPServerConfig `json:"mcp_servers,omitempty"`

	// AutoMemoryEnabled toggles Claude Code-style auto memory (the model
	// accumulating its own notes across sessions under a per-project
	// memory directory — see internal/memory). A nil pointer means
	// unset, which defaults to enabled.
	AutoMemoryEnabled *bool `json:"auto_memory_enabled,omitempty"`

	// Permissions holds opencode-style fine-grained allow/ask/deny rules,
	// keyed by tool name (or "*" for a fallback applied to every tool).
	// See ResolvePermission.
	Permissions map[string]ToolPermission `json:"permission,omitempty"`

	// AutoCompactEnabled toggles automatically summarizing a session's
	// history once its context window usage crosses 80%, freeing up
	// space to keep the conversation going. A nil pointer means unset,
	// defaulting to enabled. Also runtime-toggleable via "/config".
	AutoCompactEnabled *bool `json:"auto_compact_enabled,omitempty"`

	// ShowTPS toggles whether a tokens-per-second figure is included in
	// usage events for clients to display. A nil pointer means unset,
	// defaulting to enabled. Also runtime-toggleable via "/config".
	ShowTPS *bool `json:"show_tps,omitempty"`

	// Hooks holds Claude Code-style lifecycle hooks (shell commands run at
	// pre_tool_use/post_tool_use/user_prompt_submit/stop/session_start),
	// keyed by event name. See internal/hooks.
	Hooks hooks.Config `json:"hooks,omitempty"`

	// AutoDelegate routes matching prompts to a cheaper agent instead of
	// the session's own. Off unless configured. Also runtime-toggleable
	// via "/config auto_delegate on|off".
	AutoDelegate *AutoDelegateConfig `json:"auto_delegate,omitempty"`

	// SkipPermissions turns every "ask" decision into "allow" — the
	// equivalent of Claude Code's --dangerously-skip-permissions. A nil
	// pointer means unset, which defaults to OFF: it has to be opted into
	// deliberately, because with it on the model writes files and runs
	// shell commands with no confirmation at all.
	//
	// Explicit "deny" rules still deny. Skipping the prompts is a
	// convenience; silently overriding a rule someone wrote specifically
	// to forbid something would be a different, much worse promise.
	SkipPermissions *bool `json:"skip_permissions,omitempty"`

	// delegateMu guards AutoDelegate against the daemon's settings endpoint
	// rewriting the agent or match patterns while a turn on another
	// goroutine is deciding whether to delegate. Read unlocked at load time,
	// before the daemon exists to race with — see AutoDelegateSnapshot.
	delegateMu sync.RWMutex

	// permMu guards SkipPermissions and Permissions against the daemon's
	// permission-settings endpoints changing them at runtime (a client
	// toggling skip_permissions, or adding/removing a rule) while a tool
	// call on another goroutine is resolving a decision. Both are read
	// unlocked at load time, before the daemon exists to race with.
	permMu sync.RWMutex
}

// PermissionsSkipped reports whether permission prompts are suppressed —
// false unless skip_permissions is explicitly true.
func (c *Config) PermissionsSkipped() bool {
	c.permMu.RLock()
	defer c.permMu.RUnlock()
	return c.SkipPermissions != nil && *c.SkipPermissions
}

// SetSkipPermissionsRuntime changes the live skip_permissions setting —
// the in-memory counterpart to SetSkipPermissionsInFile, which persists it.
func (c *Config) SetSkipPermissionsRuntime(v bool) {
	c.permMu.Lock()
	defer c.permMu.Unlock()
	c.SkipPermissions = &v
}

// AddPermissionRuleRuntime mirrors AddPermissionRuleToFile's rule-append
// logic against the in-memory config, so a rule just persisted to disk also
// takes effect immediately rather than only after a restart.
func (c *Config) AddPermissionRuleRuntime(toolName string, rule PermissionRule) {
	c.permMu.Lock()
	defer c.permMu.Unlock()
	if c.Permissions == nil {
		c.Permissions = map[string]ToolPermission{}
	}
	addRule(c.Permissions, toolName, rule)
}

// RemovePermissionRuleRuntime mirrors RemovePermissionRuleFromFile against
// the in-memory config.
func (c *Config) RemovePermissionRuleRuntime(toolName string, rule PermissionRule) {
	c.permMu.Lock()
	defer c.permMu.Unlock()
	removeRule(c.Permissions, toolName, rule)
}

// PermissionsSnapshot returns the current skip_permissions state and a copy
// of every configured rule, tool by tool, for a client to display (e.g. a
// settings panel). It does not include the built-in defaults (see
// builtinRules) since those aren't something a user wrote and can't be
// removed the same way.
func (c *Config) PermissionsSnapshot() (skip bool, rules map[string][]PermissionRule) {
	c.permMu.RLock()
	defer c.permMu.RUnlock()
	skip = c.SkipPermissions != nil && *c.SkipPermissions
	rules = map[string][]PermissionRule{}
	for tool, tp := range c.Permissions {
		if tp.Flat != "" {
			rules[tool] = []PermissionRule{{Match: "*", Decision: tp.Flat}}
			continue
		}
		cp := make([]PermissionRule, len(tp.Rules))
		copy(cp, tp.Rules)
		rules[tool] = cp
	}
	return skip, rules
}

// AutoDelegateConfig sends small, mechanical prompts to a named agent
// running its own (typically cheaper) model, in its own session, instead
// of the session's main agent.
//
// The motivation is prompt-cache economics rather than raw model price.
// A cache read costs about a tenth of base input; a cache write costs
// 1.25x (or 2x on the 1h TTL). Because a cache entry is keyed by model as
// well as by prompt bytes, switching the *session's* model part-way
// through throws away the whole cached prefix — tools, system prompt, and
// every prior turn — and re-writes it at the write premium. On a long
// coding session that prefix is the expensive part.
//
// Delegating sidesteps that: the sub-agent's model runs against its own
// separate session, so the main session's model and prefix never change
// and its cache survives intact.
type AutoDelegateConfig struct {
	// Enabled is the configured default. Runtime toggling goes through
	// the loop's live setting, not this field.
	Enabled bool `json:"enabled,omitempty"`

	// Agent names which entry in Agents handles delegated prompts. It
	// must exist in the agents map.
	Agent string `json:"agent"`

	// Match is a list of opencode-style globs ("*" for any run of
	// characters, "?" for one) tried case-insensitively against the whole
	// trimmed prompt. A prompt matching any one of them is delegated. An
	// empty list delegates nothing, so a half-written config is inert
	// rather than silently routing every prompt to the cheap model.
	Match []string `json:"match,omitempty"`
}

// DelegateEnabled reports the configured default for auto-delegation. It
// is off unless a valid block turns it on, so adding the feature changes
// nothing for existing configs.
func (c *Config) DelegateEnabled() bool {
	return c.AutoDelegate != nil && c.AutoDelegate.Enabled
}

// AutoDelegateSnapshot returns a copy of the auto_delegate block, or nil if
// there is none. A copy rather than the live pointer because the daemon's
// settings endpoint can rewrite the agent and patterns at runtime while a
// turn on another goroutine is deciding whether to delegate — the caller
// gets one coherent view instead of a half-updated one.
func (c *Config) AutoDelegateSnapshot() *AutoDelegateConfig {
	c.delegateMu.RLock()
	defer c.delegateMu.RUnlock()
	if c.AutoDelegate == nil {
		return nil
	}
	cp := *c.AutoDelegate
	cp.Match = append([]string(nil), c.AutoDelegate.Match...)
	return &cp
}

// SetAutoDelegateRuntime replaces which agent handles delegated prompts and
// which prompts qualify, taking effect on the very next turn. The in-memory
// counterpart to SetAutoDelegateTargetInFile, which persists it.
//
// It creates the block if there wasn't one, so a config that never mentioned
// auto_delegate can be configured entirely from a client.
func (c *Config) SetAutoDelegateRuntime(agent string, match []string) {
	c.delegateMu.Lock()
	defer c.delegateMu.Unlock()
	if c.AutoDelegate == nil {
		c.AutoDelegate = &AutoDelegateConfig{}
	}
	c.AutoDelegate.Agent = agent
	c.AutoDelegate.Match = append([]string(nil), match...)
}

// MatchesPrompt reports whether text should be delegated. Matching is
// case-insensitive because these are natural-language prompts, not paths.
func (a *AutoDelegateConfig) MatchesPrompt(text string) bool {
	if a == nil {
		return false
	}
	subject := strings.ToLower(strings.TrimSpace(text))
	for _, pattern := range a.Match {
		if globMatch(strings.ToLower(pattern), subject) {
			return true
		}
	}
	return false
}

// MemoryEnabled reports whether auto memory is on — the default when
// AutoMemoryEnabled is unset.
func (c *Config) MemoryEnabled() bool {
	return c.AutoMemoryEnabled == nil || *c.AutoMemoryEnabled
}

// CompactEnabled reports whether auto-compaction is on — the default
// when AutoCompactEnabled is unset.
func (c *Config) CompactEnabled() bool {
	return c.AutoCompactEnabled == nil || *c.AutoCompactEnabled
}

// TPSEnabled reports whether tokens-per-second display is on — the
// default when ShowTPS is unset.
func (c *Config) TPSEnabled() bool {
	return c.ShowTPS == nil || *c.ShowTPS
}

// MCP transport names. They match the "type" field Claude Code writes in
// .mcp.json / ~/.claude.json, so an entry can be copied across verbatim.
const (
	MCPTransportStdio = "stdio"
	MCPTransportHTTP  = "http" // streamable HTTP, the current remote transport
	MCPTransportSSE   = "sse"  // the older HTTP+SSE transport, still in use
)

// MCPServerConfig describes one MCP server, in the same shape as Claude
// Code's `mcpServers` entries so an existing .mcp.json can be copied in
// directly. Two kinds of server share this struct, distinguished by Type:
//
//   - stdio: a local child process (Command/Args/Env)
//   - http, sse: a remote endpoint (URL/Headers)
//
// Type may be omitted, in which case Transport infers it — an entry with a
// URL is remote, anything else is a local command. That's what makes a
// hand-written `{"command": "npx", ...}` from before remote support existed
// keep working untouched.
type MCPServerConfig struct {
	// Type is "stdio", "http", or "sse". Empty means "infer" — see
	// Transport.
	Type string `json:"type,omitempty"`

	// stdio only.
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// http/sse only. Headers is where an API token goes, so treat its
	// values as secrets: nothing in this repo prints them, only their key
	// names (see the `mcp list` / `mcp get` output).
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Transport resolves which transport this entry uses, filling in the
// default for an entry that doesn't say. An explicit Type always wins, so a
// server can be pinned to "sse" even though "http" is what a bare URL
// infers.
func (c MCPServerConfig) Transport() string {
	switch c.Type {
	case MCPTransportStdio, MCPTransportHTTP, MCPTransportSSE:
		return c.Type
	}
	if c.URL != "" {
		// Streamable HTTP is the current spec's remote transport, so it is
		// the better guess for an entry that only carries a URL. A server
		// that still speaks the older protocol needs "type": "sse".
		return MCPTransportHTTP
	}
	return MCPTransportStdio
}

// IsRemote reports whether this server is reached over the network rather
// than started as a child process.
func (c MCPServerConfig) IsRemote() bool {
	return c.Transport() != MCPTransportStdio
}

// Validate checks that the entry carries what its transport needs, so a
// half-written config fails with a clear message instead of at connect time.
func (c MCPServerConfig) Validate() error {
	switch t := c.Transport(); t {
	case MCPTransportStdio:
		if c.Command == "" {
			return fmt.Errorf(`stdio server needs a "command"`)
		}
		if c.URL != "" {
			return fmt.Errorf(`stdio server must not set "url" (use "type": "http" or "sse" for a remote server)`)
		}
	case MCPTransportHTTP, MCPTransportSSE:
		if c.URL == "" {
			return fmt.Errorf(`%s server needs a "url"`, t)
		}
		if c.Command != "" {
			return fmt.Errorf(`%s server must not set "command"`, t)
		}
		u, err := url.Parse(c.URL)
		if err != nil {
			return fmt.Errorf("invalid url %q: %w", c.URL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("url %q must be http or https", c.URL)
		}
	}
	return nil
}

// ProviderConfig describes how to reach a model backend.
// Type selects which concrete client to construct (see provider.Provider).
type ProviderConfig struct {
	Type ProviderType `json:"type"` // "bedrock" | "openai-compat" | "anthropic"

	Region  string `json:"region,omitempty"`  // bedrock
	Profile string `json:"profile,omitempty"` // bedrock: AWS named profile to use (e.g. one set up by `localcode login bedrock`); empty uses the default credential chain

	BaseURL string `json:"base_url,omitempty"` // openai-compat (required); anthropic (optional override, e.g. an enterprise proxy — defaults to api.anthropic.com)

	// APIKey is used by openai-compat directly, and by anthropic as a
	// fallback: if empty, the anthropic provider reads the key saved by
	// `localcode login anthropic` from ~/.localcode/credentials.json
	// instead — so a project-local config.json naming an "anthropic"
	// provider doesn't need to embed the key itself.
	APIKey string `json:"api_key,omitempty"`
}

type ProviderType string

const (
	ProviderBedrock      ProviderType = "bedrock"
	ProviderOpenAICompat ProviderType = "openai-compat"
	ProviderAnthropic    ProviderType = "anthropic"
)

// Profile pins a concrete provider+model combination.
type Profile struct {
	Provider    string  `json:"provider"` // key into Config.Providers
	Model       string  `json:"model"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
}

// AgentConfig defines one named agent role: which model profile it runs
// on, and optionally a scoped system prompt and a restricted tool set —
// the same idea as oh-my-opencode's per-agent model/prompt matching (a
// cheap/fast model for a grep-only "explore" agent, a strong model for
// planning, etc.), and what lets Task-tool delegation between agents mean
// something beyond just picking a model.
type AgentConfig struct {
	Profile string `json:"profile"` // key into Config.Profiles

	// Description is shown to the model (via the Task tool) when deciding
	// which agent to delegate a piece of work to.
	Description string `json:"description,omitempty"`

	// Prompt, if set, is appended to the base system prompt for turns run
	// as this agent — e.g. "You are the review agent: look for bugs, do
	// not edit files."
	Prompt string `json:"prompt,omitempty"`

	// Tools, if non-empty, restricts this agent to only these tool names
	// (both which tools the model sees and, as defense in depth, which it
	// can actually call). Empty/absent means no restriction — every
	// registered tool is available, matching prior behavior.
	Tools []string `json:"tools,omitempty"`
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

// UpdateMCPServersInFile rewrites only the "mcp_servers" key of the JSON
// config at path, leaving every other top-level key intact — including
// keys this version of localcode doesn't know about, which a full
// Config-struct round-trip would silently drop. Used by `localcode mcp
// add/remove`. A missing file starts from an empty object; update receives
// the current entries (never nil) and mutates them in place. update may
// return an error (e.g. "server not found") to abort the whole operation —
// nothing is written in that case.
func UpdateMCPServersInFile(path string, update func(servers map[string]MCPServerConfig) error) error {
	return updateRawConfig(path, func(raw map[string]json.RawMessage) error {
		servers := map[string]MCPServerConfig{}
		if rawServers, ok := raw["mcp_servers"]; ok {
			if err := json.Unmarshal(rawServers, &servers); err != nil {
				return fmt.Errorf("parse mcp_servers in %s: %w", path, err)
			}
		}

		if err := update(servers); err != nil {
			return err
		}

		if len(servers) == 0 {
			delete(raw, "mcp_servers")
			return nil
		}
		encoded, err := json.Marshal(servers)
		if err != nil {
			return fmt.Errorf("marshal mcp_servers: %w", err)
		}
		raw["mcp_servers"] = encoded
		return nil
	})
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
	if other.ShowTPS != nil {
		c.ShowTPS = other.ShowTPS
	}
	if other.AutoDelegate != nil {
		c.AutoDelegate = other.AutoDelegate
	}
	if other.SkipPermissions != nil {
		c.SkipPermissions = other.SkipPermissions
	}
	for event, list := range other.Hooks {
		if c.Hooks == nil {
			c.Hooks = hooks.Config{}
		}
		c.Hooks[event] = list
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

	for event := range c.Hooks {
		if !hooks.KnownEvents[event] {
			return fmt.Errorf("hooks: unknown event %q (want one of pre_tool_use, post_tool_use, user_prompt_submit, stop, session_start)", event)
		}
	}

	for name, server := range c.MCPServers {
		if err := server.Validate(); err != nil {
			return fmt.Errorf("mcp_servers %q: %w", name, err)
		}
	}

	if c.AutoDelegate != nil {
		if c.AutoDelegate.Agent == "" {
			return fmt.Errorf("auto_delegate: agent is required")
		}
		if _, ok := c.Agents[c.AutoDelegate.Agent]; !ok {
			return fmt.Errorf("auto_delegate references unknown agent %q", c.AutoDelegate.Agent)
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
