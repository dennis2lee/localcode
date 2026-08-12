// Package agent implements the core agent loop: send a user message, stream
// the model's response into the session's event log, execute any requested
// tool calls, and repeat until the model stops asking for tools.
package agent

import (
	"context"
	"sync"

	"localcode/internal/commands"
	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/skills"
	"localcode/internal/tools"
)

const defaultSystemPrompt = "You are a helpful coding assistant with access to file and shell tools. Use them when needed; otherwise answer directly."

const defaultMaxTokens = 4096

// liveSettings are the process-global toggles "/config" flips at runtime —
// auto_compact, show_tps, auto_delegate. Kept under their own lock rather
// than Loop.mu: they are unrelated to per-session conversation history and
// usage, and sharing a mutex with those only happened because all of it
// once lived directly on Loop.
type liveSettings struct {
	mu           sync.Mutex
	autoCompact  bool
	showTPS      bool
	autoDelegate bool
}

func (s *liveSettings) AutoCompact() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoCompact
}

func (s *liveSettings) SetAutoCompact(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoCompact = v
}

func (s *liveSettings) ShowTPS() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.showTPS
}

func (s *liveSettings) SetShowTPS(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.showTPS = v
}

func (s *liveSettings) AutoDelegate() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoDelegate
}

func (s *liveSettings) SetAutoDelegate(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autoDelegate = v
}

// Loop wires a session store, tool registry, and the set of configured
// model providers together. One Loop instance is shared across sessions;
// per-session conversation history is kept in memory.
type Loop struct {
	Store        *session.Store
	Tools        *tools.Registry
	Providers    map[string]provider.Provider // provider config key -> client
	Config       *config.Config
	SystemPrompt string
	// Skills backs the /skill slash command (list / load by name). It's
	// separate from the Skill *tool* the model can call on its own —
	// this is the same skill set, just also reachable directly by the
	// user typing a command instead of waiting on the model to decide to
	// use it.
	Skills []skills.Skill

	// Commands backs custom user-defined slash commands ("/<name>"),
	// loaded from .localcode/commands/*.md (project) and
	// ~/.localcode/commands/*.md (global) — see internal/commands.
	Commands []commands.Command

	// ProjectDir is the working directory custom commands resolve
	// "!`shell`" and "@file" expansions against.
	ProjectDir string

	// PendingInput, if set, is asked at every tool boundary inside a turn
	// whether the user has typed anything since the turn began, and
	// returns one such message at a time until it reports false.
	//
	// This is what lets someone redirect a long job without stopping it —
	// "actually, skip the tests" lands at the model's next step instead of
	// waiting for the whole turn to finish. The daemon owns the queue
	// itself (see turnTracker), because only it knows whether a turn is
	// still registered as running.
	PendingInput func(sessionID string) (string, bool)

	// Tasks runs sub-agents in their own sessions. Set by NewTaskManager,
	// so a Loop built without one (a bare Loop in a test) simply has no
	// delegation rather than a nil dereference.
	Tasks *TaskManager

	// ProbeContextWindow, if set, asks a provider's server how big a
	// context window it is actually serving for a model — see
	// (*Loop).contextWindow and provider.OpenAICompat.ContextWindow.
	//
	// A hook rather than something this package does for itself, for the
	// same reason PickDirectory is one: it puts a request on the wire, and
	// what that costs depends on which provider is behind it. The daemon
	// wires it for the openai-compatible providers, where GET /v1/models
	// is free and is the only source that knows what a local server
	// actually loaded. Left nil, nothing is asked and the window is
	// guessed from the model name exactly as before.
	ProbeContextWindow func(ctx context.Context, providerKey, model string) (int, bool)

	// MemoryDir is this project's auto-memory directory (see
	// internal/memory) — "" if auto memory is disabled. Backs the
	// "/memory" local command; the actual read/write of memory files
	// happens via the model's ordinary file tools, not here.
	MemoryDir string

	// mu guards per-session state: conversation history and both usage
	// views (see usage.go), plus ProjectDir. Process-global runtime
	// settings ("/config" toggles) live in settings instead, below —
	// unrelated state that happened to share this lock only because it
	// used to live directly on Loop.
	mu              sync.Mutex
	messages        map[string][]provider.Message     // sessionID -> history
	usage           map[string]sessionUsage           // sessionID -> latest known usage
	cumulativeUsage map[string]map[string]modelTotals // sessionID -> model -> running totals, see /usage
	turnRate        map[string]turnRate               // sessionID -> this turn's tokens/generation time
	// probedWindows caches what a provider's server said its context
	// window is, keyed by provider+model. A zero value means "asked, and
	// it did not say" — recorded so a server that has no answer is not
	// asked again on every turn.
	probedWindows map[string]int

	settings liveSettings
}

func New(store *session.Store, reg *tools.Registry, providers map[string]provider.Provider, cfg *config.Config) *Loop {
	return &Loop{
		Store:        store,
		Tools:        reg,
		Providers:    providers,
		Config:       cfg,
		SystemPrompt: defaultSystemPrompt,
		settings: liveSettings{
			autoCompact:  cfg.CompactEnabled(),
			showTPS:      cfg.TPSEnabled(),
			autoDelegate: cfg.DelegateEnabled(),
		},
		messages:        map[string][]provider.Message{},
		usage:           map[string]sessionUsage{},
		cumulativeUsage: map[string]map[string]modelTotals{},
		turnRate:        map[string]turnRate{},
		probedWindows:   map[string]int{},
	}
}

// ClearSessionState drops all in-memory state Loop keeps for sessionID
// (conversation history, usage snapshot, cumulative per-model totals) —
// called when a session is deleted, so its memory isn't retained forever.
func (l *Loop) ClearSessionState(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.messages, sessionID)
	delete(l.usage, sessionID)
	delete(l.cumulativeUsage, sessionID)
	delete(l.turnRate, sessionID)
}

// AutoCompactEnabled reports whether auto-compaction is currently on —
// process-global, toggleable live via "/config auto_compact on|off".
func (l *Loop) AutoCompactEnabled() bool { return l.settings.AutoCompact() }

// SetAutoCompactEnabled changes the live auto-compaction setting.
func (l *Loop) SetAutoCompactEnabled(v bool) { l.settings.SetAutoCompact(v) }

// ShowTPS reports whether usage events should carry a tokens-per-second
// figure for display — process-global, toggleable live via "/config
// show_tps on|off".
func (l *Loop) ShowTPS() bool { return l.settings.ShowTPS() }

// SetShowTPS changes the live TPS-display setting.
func (l *Loop) SetShowTPS(v bool) { l.settings.SetShowTPS(v) }

// AutoDelegateEnabled reports whether prompts matching the auto_delegate
// rules are routed to the configured sub-agent — process-global,
// toggleable live via "/config auto_delegate on|off".
func (l *Loop) AutoDelegateEnabled() bool { return l.settings.AutoDelegate() }

// SetAutoDelegateEnabled changes the live auto-delegation setting. It has
// no effect when the config has no auto_delegate block to say which agent
// to delegate to — see delegateTarget.
func (l *Loop) SetAutoDelegateEnabled(v bool) { l.settings.SetAutoDelegate(v) }

// GetProjectDir reads ProjectDir under the same lock SetProjectDir writes
// it with — the daemon's workspace-switch endpoint changes it at runtime,
// so a concurrent "!`shell`"/"@file" expansion (see handleConfigCommand)
// must not observe a half-written value.
func (l *Loop) GetProjectDir() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ProjectDir
}

// SetProjectDir changes the live project directory. The caller is
// responsible for also os.Chdir-ing the process, since ProjectDir alone
// only affects custom-command expansion — tools resolve relative paths
// against the process's actual working directory.
func (l *Loop) SetProjectDir(dir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ProjectDir = dir
}

func (l *Loop) appendHistory(sessionID string, msg provider.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages[sessionID] = append(l.messages[sessionID], msg)
}

func (l *Loop) history(sessionID string) []provider.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]provider.Message, len(l.messages[sessionID]))
	copy(out, l.messages[sessionID])
	return out
}

// setHistory replaces sessionID's entire in-memory history — used only by
// auto-compaction to swap in a summary.
func (l *Loop) setHistory(sessionID string, msgs []provider.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages[sessionID] = msgs
}
