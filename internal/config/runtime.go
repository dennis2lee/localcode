package config

// This file holds the mutex-guarded surface of Config that changes at
// runtime — a daemon settings endpoint flipping skip_permissions, adding or
// removing a permission rule, or repointing auto_delegate — as opposed to
// config.go's static DTO shape and load.go's read-from-disk path.

// PermissionsSkipped reports whether permission prompts are suppressed —
// false unless skip_permissions is explicitly true.
func (c *Config) PermissionsSkipped() bool {
	c.permMu.RLock()
	defer c.permMu.RUnlock()
	return c.SkipPermissions != nil && *c.SkipPermissions
}

// ToolPermissionsSkipped, ReadOutsideAllowed and WriteOutsideAllowed are
// the daemon defaults for the other three switches — what a session that
// has not answered the question for itself follows. All three default to
// off, which for the two boundary ones means "ask".
func (c *Config) ToolPermissionsSkipped() bool {
	c.permMu.RLock()
	defer c.permMu.RUnlock()
	return c.SkipToolPermissions != nil && *c.SkipToolPermissions
}

func (c *Config) ReadOutsideAllowed() bool {
	c.permMu.RLock()
	defer c.permMu.RUnlock()
	return c.ReadOutsideWorkspace != nil && *c.ReadOutsideWorkspace
}

func (c *Config) WriteOutsideAllowed() bool {
	c.permMu.RLock()
	defer c.permMu.RUnlock()
	return c.WriteOutsideWorkspace != nil && *c.WriteOutsideWorkspace
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

// SmartAgentLive reports whether Smart Agent is on right now.
//
// The live view rather than the file's, because the switch is flipped at
// runtime and read from three places that cannot share a copy: the agent
// loop deciding what a turn looks like, the daemon answering a settings
// request, and permission resolution, which runs on a tool call's own
// goroutine while any of the others might be writing.
func (c *Config) SmartAgentLive() bool {
	c.permMu.RLock()
	defer c.permMu.RUnlock()
	return c.SmartAgent != nil && *c.SmartAgent
}

// SetSmartAgentRuntime changes the live Smart Agent setting. The in-memory
// counterpart to SetSmartAgentInFile, which persists it.
func (c *Config) SetSmartAgentRuntime(v bool) {
	c.permMu.Lock()
	defer c.permMu.Unlock()
	c.SmartAgent = &v
}

// OrchestrateLive reports whether the Orchestrate tool is on right now,
// read the same way and for the same reasons as SmartAgentLive.
func (c *Config) OrchestrateLive() bool {
	c.permMu.RLock()
	defer c.permMu.RUnlock()
	return c.Orchestrate != nil && *c.Orchestrate
}

// SetOrchestrateRuntime changes the live setting. The in-memory
// counterpart to SetOrchestrateInFile.
func (c *Config) SetOrchestrateRuntime(v bool) {
	c.permMu.Lock()
	defer c.permMu.Unlock()
	c.Orchestrate = &v
}
