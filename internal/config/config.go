// Package config loads and validates the JSON configuration that maps
// agent/task types to model profiles and provider connection details.
package config

import (
	"fmt"
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

	// DictationModelDir points at an unpacked sherpa-onnx streaming
	// speech model, enabling the desktop window's microphone button.
	// Empty disables dictation, which is the default: there is no
	// sensible model to guess, and guessing one would mean a silent
	// several-hundred-megabyte download the first time someone clicked.
	DictationModelDir string `json:"dictation_model_dir,omitempty"`

	// Dictation configures the speech engine. Every field is optional:
	// with none set, an installed engine beside the binary is found and
	// used. See docs/USAGE.md.
	Dictation *DictationConfig `json:"dictation,omitempty"`

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
// DictationConfig selects and locates the speech engine.
//
// Everything here is an override. The engine and its model are found
// beside the binary, which is where the installer puts them, so a
// working setup needs none of these fields.
type DictationConfig struct {
	// Engine is "whisper" or "sherpa". Empty prefers whisper, which is
	// the one that works in every build; sherpa needs a desktop build
	// because it is linked in rather than run as a child process.
	Engine string `json:"engine,omitempty"`
	// WhisperBin is the path to whisper.cpp's server executable.
	WhisperBin string `json:"whisper_bin,omitempty"`
	// WhisperModel is the path to a ggml model file, or to a directory
	// holding one. When several are installed the largest is used.
	WhisperModel string `json:"whisper_model,omitempty"`
	// WhisperURL points dictation at a whisper.cpp server on another
	// machine, as "host:port" or "http://host:port". Set, it wins over
	// everything local: no engine or model needs to be installed here and
	// no child process is started, which is the point — it puts the work
	// on a box that has the CPU or GPU for it.
	//
	// It also reverses this feature's main property, so it is worth being
	// blunt about: recorded audio then leaves this machine, over plain
	// HTTP. Only point it somewhere you would be willing to send what you
	// say out loud.
	WhisperURL string `json:"whisper_url,omitempty"`

	// WhisperAPI names the dialect the remote server speaks, when it
	// should not be discovered: "openai" (POST /v1/audio/transcriptions),
	// "whispercpp" (POST /inference) or "whisperx" (POST /asr).
	//
	// Empty means find out, which is the right default — the first
	// transcription tries each in turn and remembers which one answered.
	// Worth setting only to skip that, or when a server answers a path it
	// does not really implement.
	WhisperAPI string `json:"whisper_api,omitempty"`
	// Language is the spoken language as an ISO 639-1 code, "ko" or
	// "en". Empty auto-detects, which is what mixed speech wants and a
	// little slower and less certain for speech that is only ever one
	// language.
	Language string `json:"language,omitempty"`
	// Threads caps the CPU the engine may use. 0 picks a modest default:
	// this runs beside a language model doing the actual work.
	Threads int `json:"threads,omitempty"`
}

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

// TPSEnabled reports whether tokens-per-second display is on — the
// default when ShowTPS is unset.
func (c *Config) TPSEnabled() bool {
	return c.ShowTPS == nil || *c.ShowTPS
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

	// ContextWindow is the model's total input+output token limit. Zero
	// means "look it up from the model name", which is a guess: a local
	// server can host anything under any name, and the guess for an
	// unrecognised one is 128k. Guessing high is the harmful direction —
	// the whole point of knowing this number is to keep a request inside
	// the limit, and a request built against a window larger than the real
	// one is refused outright by the server.
	ContextWindow int `json:"context_window,omitempty"`

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

	for name, profile := range c.Profiles {
		if _, ok := c.Providers[profile.Provider]; !ok {
			return fmt.Errorf("profile %q references unknown provider %q", name, profile.Provider)
		}
		// Bounded at the point it is read rather than wherever it is used.
		// This number multiplies turns, and a typo in it — a stray zero —
		// is a session that keeps prompting itself.
		if profile.KeepGoing < -1 || profile.KeepGoing > maxKeepGoing {
			return fmt.Errorf("profile %q: keep_going is %d, which is outside -1..%d (-1 means never, 0 means the model's own default)",
				name, profile.KeepGoing, maxKeepGoing)
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
