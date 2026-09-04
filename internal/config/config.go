// Package config loads and validates the JSON configuration that maps
// agent/task types to model profiles and provider connection details.
package config

import (
	"fmt"
	"strings"
	"sync"

	"localcode/internal/hooks"
	"localcode/internal/provider"
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
	// history once its context window usage crosses the threshold
	// (AutoCompactPercent, 50 by default), freeing up space to keep the
	// conversation going. A nil pointer means unset, defaulting to
	// enabled. Also runtime-toggleable via "/auto-compact".
	AutoCompactEnabled *bool `json:"auto_compact_enabled,omitempty"`

	// AutoCompactPercent is the context-window fill percentage that
	// triggers auto-compaction. Zero means unset, defaulting to 50.
	// Settable at runtime with "/auto-compact <percent>".
	AutoCompactPercent int `json:"auto_compact_percent,omitempty"`

	// KeepGoingEnabled gates the carry-on nudge for models known to stop
	// mid-task (see internal/agent/keep_going.go). A nil pointer means
	// unset, defaulting to enabled. It only ever applies to models whose
	// id contains "muse"; on everything else the nudge never fires
	// regardless of this switch. Toggleable via "/keep-going" and the
	// settings window.
	KeepGoingEnabled *bool `json:"keep_going,omitempty"`

	// RepeatLimit is how many steps in a row a turn may spend repeating
	// tool calls it has already made before the turn is ended (see
	// maxRepeatSteps in internal/agent/keep_going.go for what a repeat
	// is). A nil pointer means unset, defaulting to 3. Zero turns the
	// guard off. Toggleable via "/repeat-limit" and the settings window.
	//
	// Exposed because the guard has two failure modes and only one of
	// them is visible from inside localcode. A model that will not stop
	// is caught at any small number; a model that thinks by re-reading
	// the same two places, which some local models do, is cut off by the
	// same number while it was about to act. Which of the two a session
	// is having is something the person watching it can see and the
	// guard cannot.
	RepeatLimitSteps *int `json:"repeat_limit,omitempty"`

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

	// SmartAgent turns on the Smart Agent bundle: a roster of built-in
	// specialist sub-agents (explore, librarian, oracle, plan, implement,
	// verify), the orchestration prompt that tells the session's own model
	// when to hand work to them, and the background delegation tools that
	// let it run several at once. See internal/smart.
	//
	// A nil pointer means unset, which defaults to OFF, and that default
	// is deliberate rather than cautious. With it on, one request can
	// become half a dozen model calls against half a dozen contexts, which
	// is the point of it and is also a bill nobody agreed to. Also
	// runtime-toggleable via "/config smart_agent on|off" and from the
	// settings panel.
	SmartAgent *bool `json:"smart_agent,omitempty"`

	// Orchestrate turns on the Orchestrate tool: a plan of delegated
	// stages, authored by filling in that tool's input schema and run by a
	// Go loop rather than by the model deciding one step at a time. See
	// internal/agent/orchestrate.go.
	//
	// Its own switch rather than part of Smart Agent, although it needs
	// that roster to have anywhere to delegate to, because the thing being
	// opted into is a different size. A Task call is one extra model turn;
	// a run is up to thirty-two, and somebody who wants the specialists
	// without that should be able to have exactly that.
	//
	// Nil means unset, which is OFF, for the reason smart_agent is: it
	// spends money the ordinary path does not. Also runtime-toggleable via
	// "/orchestrate on|off".
	Orchestrate *bool `json:"orchestrate,omitempty"`

	// ModelInvocable is the switch over all of it: whether the model may
	// run commands at all.
	//
	// Two levels, because they answer different questions. This one is
	// whether the capability is on, and it moves at runtime like Smart
	// Agent's does. The other is which commands — model_commands below
	// for the built-ins, model_invocable in a file's own frontmatter for
	// a custom command or a skill. Turning this off leaves every one of
	// those opt-ins written down and inert, which is what makes it safe
	// to turn off in a hurry.
	//
	// Nil means unset, which is OFF.
	ModelInvocable *bool `json:"model_invocable,omitempty"`

	// ModelCommands names the built-in commands the model may run itself,
	// each with its leading slash: ["/compact", "/usage"].
	//
	// Built-ins are listed here because they have no file to opt in from.
	// A custom command or a skill opts itself in with model_invocable in
	// its own frontmatter, which is the better place for it: that file is
	// text the person wrote, and this list is the product's own commands.
	//
	// There is no wildcard, on purpose. The list includes
	// "/permission-skip-all" if somebody writes it, and that is a
	// deliberate act with a name in a file rather than something a "*"
	// could sweep in — a model reads files, command output and whatever
	// an MCP server returned, so anything it can trigger is reachable
	// from text it did not write. Empty, which is the default, means the
	// model may run no built-in command at all.
	ModelCommands []string `json:"model_commands,omitempty"`

	// TraceMaxAgeDays and TraceMaxTotalMB bound the structured turn log
	// under ~/.localcode/trace/. Unset means the default age (30 days)
	// and no size cap; the age cannot be turned off, because a log that
	// grows forever is the defect the bound exists to fix.
	TraceMaxAgeDays int `json:"trace_max_age_days,omitempty"`
	TraceMaxTotalMB int `json:"trace_max_total_mb,omitempty"`

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

	// AutoUpdate toggles installing a newer release at startup, before
	// anything is served and before any conversation is opened.
	//
	// Startup is the moment with nothing to lose: no turn is in flight,
	// no background task is running, and the exec that follows costs the
	// same terminal a moment rather than costing somebody their work. It
	// is the only moment localcode replaces itself without being asked,
	// which is why the switch is here and why turning it off is one line.
	//
	// A nil pointer means unset, defaulting to on.
	AutoUpdate *bool `json:"auto_update,omitempty"`

	// UpdateURL, when set, is where the update button looks instead of
	// GitHub: one https address at which the current installers are
	// published, side by side, named the way localcode names them.
	//
	// It exists for a machine that cannot reach github.com, or an
	// organisation that would rather its own build were the one installed
	// — an internal Bitbucket, an artifact server, a file share. The
	// version is read out of the filenames, because on a directory of
	// files that is the only place it is written down. See
	// internal/update/mirror.go.
	UpdateURL string `json:"update_url,omitempty"`

	// VerifyCommand is the one command this project can be checked with:
	// its tests, its build, its linter. It backs the `check` tool, which
	// runs exactly this and takes no arguments.
	//
	// Declared here rather than composed by a model, which is the whole
	// of why it exists. A debate reviewer is read-only and must stay that
	// way — a reviewer with a shell is a second author — but a reviewer
	// that cannot find out whether the code runs is judging by reading
	// alone, which is the weaker half of a review. One fixed command the
	// person wrote settles both: the model chooses whether to run it and
	// never what it is.
	//
	// Empty means the tool is not registered at all, rather than
	// registered and always failing.
	VerifyCommand string `json:"verify_command,omitempty"`

	// The other three switches, and what all four are: the daemon's
	// defaults. A session answers these questions for itself (see
	// session.Permissions); these are what a session that has not been
	// asked follows, and what a new one starts with.
	//
	// SkipToolPermissions is the useful middle. It allows every tool
	// prompt the way SkipPermissions does and stops at the edge of the
	// project: a shell command, a write, an edit, all without asking, and
	// still a question before anything reaches outside the workspace.
	// Someone working head-down in one repository wants exactly that, and
	// before this the only way to stop being interrupted was to turn off
	// the guard that matters most.
	SkipToolPermissions *bool `json:"skip_tool_permissions,omitempty"`

	// ReadOutsideWorkspace and WriteOutsideWorkspace allow the two halves
	// of leaving the project without being asked. Separate, because they
	// are not the same risk: reading a header in /usr/include is ordinary
	// work, and writing to a directory this conversation was never told
	// about is the failure a boundary exists to catch.
	//
	// Nil means unset, which defaults to OFF, which means "ask". That is
	// the whole point of the feature: the model leaving the project is a
	// question, and the two switches are how someone answers it once
	// instead of every time.
	ReadOutsideWorkspace  *bool `json:"read_outside_workspace,omitempty"`
	WriteOutsideWorkspace *bool `json:"write_outside_workspace,omitempty"`

	// delegateMu guards AutoDelegate against the daemon's settings endpoint
	// rewriting the agent or match patterns while a turn on another
	// goroutine is deciding whether to delegate. Read unlocked at load time,
	// before the daemon exists to race with — see AutoDelegateSnapshot in
	// runtime.go.
	delegateMu sync.RWMutex

	// permMu guards SkipPermissions and Permissions against the daemon's
	// permission-settings endpoints changing them at runtime (a client
	// toggling skip_permissions, or adding/removing a rule) while a tool
	// call on another goroutine is resolving a decision. Both are read
	// unlocked at load time, before the daemon exists to race with. See
	// runtime.go.
	permMu sync.RWMutex
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

// DefaultRepeatLimit is the number of nothing-new steps that end a turn
// when repeat_limit is unset. Small on purpose: see maxRepeatSteps.
const DefaultRepeatLimit = 3

// MaxRepeatLimit caps repeat_limit. Past this the guard is off in all but
// name, and off is a setting with its own spelling.
const MaxRepeatLimit = 50

// RepeatLimit reports the repeat guard's ceiling: the default when unset,
// zero when the guard is off, and never above MaxRepeatLimit.
func (c *Config) RepeatLimit() int {
	if c.RepeatLimitSteps == nil {
		return DefaultRepeatLimit
	}
	n := *c.RepeatLimitSteps
	if n < 0 {
		return 0
	}
	if n > MaxRepeatLimit {
		return MaxRepeatLimit
	}
	return n
}

// AutoUpdateEnabled reports whether localcode installs a newer release at
// startup — the default when AutoUpdate is unset.
func (c *Config) AutoUpdateEnabled() bool {
	return c.AutoUpdate == nil || *c.AutoUpdate
}

// DefaultCompactPercent is the context-window fill that triggers
// auto-compaction when no threshold is configured.
const DefaultCompactPercent = 50

// CompactPercent reports the auto-compaction threshold, clamped to a
// range where it can still mean something: below 10 would compact almost
// every turn, and 100 or above would never fire, which is what the off
// switch is for.
func (c *Config) CompactPercent() int {
	p := c.AutoCompactPercent
	if p == 0 {
		return DefaultCompactPercent
	}
	if p < 10 {
		return 10
	}
	if p > 95 {
		return 95
	}
	return p
}

// KeepGoing reports whether the carry-on nudge is enabled — the default
// when KeepGoingEnabled is unset. Whether it applies to a given model is
// a separate question; see internal/agent/keep_going.go.
func (c *Config) KeepGoing() bool {
	return c.KeepGoingEnabled == nil || *c.KeepGoingEnabled
}

// TPSEnabled reports whether tokens-per-second display is on — the
// default when ShowTPS is unset.
func (c *Config) TPSEnabled() bool {
	return c.ShowTPS == nil || *c.ShowTPS
}

// SmartAgentEnabled reports the configured default for Smart Agent. Unset
// is off, so adding the feature changes nothing for an existing config.
func (c *Config) SmartAgentEnabled() bool {
	return c.SmartAgent != nil && *c.SmartAgent
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

	// MaxConcurrentTasks caps how many background tasks may run against
	// this endpoint at once. Zero, the default, means no lane: this
	// provider is bounded only by the daemon-wide max_concurrent_tasks,
	// which is exactly what it was before this field existed.
	//
	// Deliberately the same name as the top-level setting, one scope
	// down, because it is the same quantity: that one bounds tasks on the
	// daemon, this one bounds tasks at one endpoint. Contention is not a
	// single global integer. One local model on one GPU serves one
	// request at a time whatever the daemon-wide number says, so eight
	// background tasks against it are a queue however they were admitted,
	// while a hosted provider on the same daemon is held to the same
	// small number for no reason.
	//
	// Read once, when the task manager is built, like the daemon-wide one
	// beside it. A provider block already needs a restart to take effect,
	// since the clients themselves are constructed at startup.
	MaxConcurrentTasks int `json:"max_concurrent_tasks,omitempty"`
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

	// ContextWindow is the model's total input+output token limit. Zero
	// means "look it up from the model name", which is a guess: a local
	// server can host anything under any name, and the guess for an
	// unrecognised one is 128k. Guessing high is the harmful direction —
	// the whole point of knowing this number is to keep a request inside
	// the limit, and a request built against a window larger than the real
	// one is refused outright by the server.
	ContextWindow int `json:"context_window,omitempty"`

	// Effort is how hard this model is asked to think: "off", "low",
	// "medium" or "high". Empty, the default, says nothing at all and
	// leaves the request exactly as it has always been.
	//
	// One word over several wires, and what it reaches depends on the
	// model. An OpenAI-compatible server is sent "reasoning_effort",
	// which is the spelling local servers use, so a muse or a gemma that
	// supports reasoning takes the level and one that does not ignores
	// the field. Anthropic's API is sent extended thinking: the newest
	// Claude families decide the amount themselves and every level maps
	// to the same switch there, while an older one gets a token budget
	// per level. Bedrock is not wired to it yet and says so rather than
	// pretending. See internal/provider.
	//
	// Off by default on purpose: this changes what a model does with a
	// request and what it costs, so nothing happens to anybody who has
	// not asked for it.
	Effort string `json:"effort,omitempty"`

	// Fallback names other profiles to try, in order, when a request to
	// this one fails for a reason another model could survive: a rate
	// limit, a provider outage, a model that is not there, an expired
	// credential. Empty, the default, means a failure is a failure.
	//
	// Model failure is an ordinary condition in a long agent session
	// rather than an exception, and the failures worth naming here are the
	// ones that are about the endpoint rather than about the request: a
	// conversation too long for the window is not one of them (it is
	// summarized and retried on the same model, which is the right
	// answer), and neither is a refused tool call.
	//
	// The chain is flat. A fallback's own Fallback list is not followed,
	// so a chain cannot loop and its length is what it says it is.
	//
	// Read only when Smart Agent is on. Switching models mid-session is a
	// visible change in who is answering, and it is part of the bundle
	// that a user opts into rather than something that starts happening
	// after an update.
	Fallback []string `json:"fallback,omitempty"`

	// KeepGoing is how many times one turn may be told to carry on after
	// the model stops with the task unfinished. 0, the default, defers to
	// the model: families known to stall get a small budget out of the
	// box (see modelKeepGoing in internal/agent), everything else gets
	// none. -1 forces it off whatever the model.
	//
	// This exists for a habit the strong hosted models do not have and
	// several local ones do: after a tool result, the model writes down
	// what still needs doing — "global_init.cpp also has to be updated" —
	// and ends its turn instead of doing it. Typing "carry on" makes it
	// pick up again, every time, which is a person acting as the model's
	// own loop.
	//
	// Per profile because it is a property of the model, not of the work:
	// a turn that ends after tool use looks the same whether the model
	// finished or gave up, and on a model that stops when it is done a
	// carry-on spends a turn asking "anything else?" after every task —
	// which is why the default budget for unrecognised models is zero.
	KeepGoing int `json:"keep_going,omitempty"`
}

// maxKeepGoing caps keep_going. Ten is already far past the point where a
// model that has not finished is going to.
const maxKeepGoing = 10

// maxProviderConcurrency caps a provider's own max_concurrent_tasks. The
// number is not the interesting part: what matters is that the field is
// bounded at load, so a typo is a startup error naming the provider rather
// than a lane the width of an int.
const maxProviderConcurrency = 64

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

// Validate checks that all cross-references (agent -> profile -> provider)
// resolve, so the daemon fails fast at startup rather than mid-task.
func (c *Config) Validate() error {
	if c.DefaultProfile != "" {
		if _, ok := c.Profiles[c.DefaultProfile]; !ok {
			return fmt.Errorf("default_profile %q not found in profiles", c.DefaultProfile)
		}
	}

	// Bounded where it is read, for the reason keep_going is: a stray
	// number here is not one anybody meant, and failing at load says so at
	// the moment it can still be fixed rather than in the middle of a
	// fan-out.
	for name, p := range c.Providers {
		if p.MaxConcurrentTasks < 0 || p.MaxConcurrentTasks > maxProviderConcurrency {
			return fmt.Errorf("provider %q: max_concurrent_tasks is %d, outside 0..%d (0 means no per-provider limit)",
				name, p.MaxConcurrentTasks, maxProviderConcurrency)
		}
	}

	for name, profile := range c.Profiles {
		if _, ok := c.Providers[profile.Provider]; !ok {
			return fmt.Errorf("profile %q references unknown provider %q", name, profile.Provider)
		}
		// Bounded at the point it is read rather than wherever it is used.
		// This number multiplies turns, and a typo in it — a stray zero —
		// is a session that keeps prompting itself.
		if profile.Effort != "" && !provider.ValidEffort(profile.Effort) {
			return fmt.Errorf("profile %q: effort is %q, which is not one of off, low, medium, high, xhigh",
				name, profile.Effort)
		}
		if profile.KeepGoing < -1 || profile.KeepGoing > maxKeepGoing {
			return fmt.Errorf("profile %q: keep_going is %d, which is outside -1..%d (-1 means never, 0 means the model's own default)",
				name, profile.KeepGoing, maxKeepGoing)
		}
		// Checked at load rather than at the moment of a failure. A
		// fallback chain is read exactly when something has already gone
		// wrong, which is the worst possible time to discover a typo in
		// it.
		for _, fb := range profile.Fallback {
			if fb == name {
				return fmt.Errorf("profile %q lists itself in fallback, which would retry the endpoint that just failed", name)
			}
			if _, ok := c.Profiles[fb]; !ok {
				return fmt.Errorf("profile %q has fallback %q, which is not a profile", name, fb)
			}
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
