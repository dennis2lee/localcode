// Package agent implements the core agent loop: send a user message, stream
// the model's response into the session's event log, execute any requested
// tool calls, and repeat until the model stops asking for tools.
package agent

import (
	"context"
	"sync"

	"localcode/internal/commands"
	"localcode/internal/config"
	"localcode/internal/prompt"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/skills"
	"localcode/internal/tools"
	"localcode/internal/trace"
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

	// ConfigPath is the config.json the toggle commands write to, so a
	// switch flipped at the prompt survives a restart the way the same
	// switch flipped in the settings window does. Empty means there is
	// no file to write, and those commands then say the change is for
	// this run only rather than pretending it was saved.
	ConfigPath string

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

	// UserWaiting, if set, reports whether the user has typed something
	// that has not reached the model yet. Only keep_going reads it: when
	// the person has already said what should happen next, saying "carry
	// on" for them talks over them, and their message is one turn away
	// anyway. Nil simply means "not known", which is the same answer as
	// no.
	UserWaiting func(sessionID string) bool

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

	// WorkspaceRules, if set, returns the AGENTS.md/CLAUDE.md section for a
	// directory, and is asked once per turn for the directory the session
	// is actually working in — see internal/rules.
	//
	// A hook rather than a string baked into SystemPrompt, because the
	// answer is not a property of the daemon. Rules used to be loaded once
	// at startup from the process's own working directory, so a session
	// working in another project was told that project's rules did not
	// exist and the startup directory's did — and a desktop build launched
	// from Finder, whose working directory is "/", had no project rules at
	// all no matter which workspace was open.
	//
	// Left nil, no project rules are added, which is what a bare Loop in a
	// test wants.
	WorkspaceRules func(dir string) string

	// Trace, if set, is where the structured record of what each turn did
	// is written — see internal/trace. Only written to while Smart Agent
	// is on (see Loop.tracer), and every call is nil-safe, so a Loop built
	// without one simply records nothing.
	Trace *trace.Writer

	// lifecycle is the boundary between admitting work into a session
	// tree and deleting one. See lifecycle.go.
	lifecycle *lifecycle

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

	// SkillsSection and MemorySection are the startup-loaded halves of
	// the prompt that used to be folded into SystemPrompt before the
	// Loop ever saw them. Separate fields because they are separate
	// assets: a skill index is user-installed procedure, a memory index
	// is text the model wrote for itself, and an inventory that lists
	// them as "the base prompt" cannot answer questions about either.
	SkillsSection string
	// MemoryPolicy is the product's own description of the auto-memory
	// convention; MemorySection is the model's own notes read back.
	// Separate fields because they are separate trust classes: the
	// policy instructs, and a note a previous turn wrote does not.
	MemoryPolicy  string
	MemorySection string

	// Manifests is where assembly manifests are kept so a trace line's
	// manifest id can still be resolved after the call: identities,
	// hashes, reasons, exclusions, warnings and lowering, never bodies.
	// Nil is safe and simply records nothing.
	Manifests *prompt.Store

	// promptReg is the declared prompt surface (see prompt_assets.go),
	// built on first use and immutable after. Shared across turns on
	// purpose: an asset ID has to mean the same thing in every request
	// or the manifests cannot be compared.
	promptOnce sync.Once
	promptReg  *prompt.Registry

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
		lifecycle:       newLifecycle(),
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
	if l.Tasks != nil {
		// Background tasks launched from this session and never collected
		// hold their answers in the manager, not in the session log. The
		// session is going away, so nobody is coming for them.
		l.Tasks.forgetSession(sessionID)
	}
}

// claimSessionTree takes the deletion claim on sessionID and everything
// below it, and returns the ids it claimed plus the release.
//
// Two things happen here that the caller cannot do afterwards. The claim
// closes admission into the tree, and it waits for admissions already in
// flight, so the tree read after it is one nothing is still being added
// to. The loop is what covers the second level: a descendant that was not
// claimed yet could admit a child of its own between the read and the
// claim, so the set is re-read until it stops growing. It always does,
// because every pass claims strictly more and nothing can be added under
// what is already claimed.
//
// The claim is released by the caller, after the sessions are gone.
// Releasing it any earlier reopens admission into a tree that is
// half-deleted, which is the same defect one step later.
func (l *Loop) claimSessionTree(sessionID string) ([]string, func()) {
	lc := l.lifecycle
	if lc == nil {
		// A Loop built without the constructor. Nothing to claim, and no
		// reason to fail a delete over it.
		ids := []string{sessionID}
		if l.Store != nil {
			ids = append(l.Store.Descendants(sessionID), sessionID)
		}
		return ids, func() {}
	}

	claimed := map[string]bool{sessionID: true}
	order := []string{sessionID}
	lc.claim(order)

	for l.Store != nil {
		var fresh []string
		for _, id := range l.Store.Descendants(sessionID) {
			if !claimed[id] {
				claimed[id] = true
				fresh = append(fresh, id)
			}
		}
		if len(fresh) == 0 {
			break
		}
		lc.claim(fresh)
		order = append(order, fresh...)
	}

	return order, func() { lc.release(order) }
}

// StopSessionTree claims sessionID and its descendants, stops every
// background task in that tree, waits for each to unwind, and clears the
// loop state each of those sessions was holding.
//
// It returns the claimed ids and the release. The caller deletes the
// sessions and then releases: the claim has to outlive the removal, or an
// admission slips into the window between the last task stopping and the
// records going away.
//
// Deleting a conversation used to remove one record and leave the work:
// the tasks it had launched kept running, in sessions nothing lists,
// writing to a parent that no longer existed. See TaskManager.StopSession.
func (l *Loop) StopSessionTree(sessionID string) ([]string, func()) {
	ids, release := l.claimSessionTree(sessionID)
	if l.Tasks != nil {
		l.Tasks.StopSession(ids)
	}
	for _, id := range ids {
		l.ClearSessionState(id)
	}
	return ids, release
}

// StopEverything is StopSessionTree for the whole daemon: it refuses every
// admission anywhere, including new sessions and new top-level turns,
// stops all background work, and returns the release.
//
// Delete-all needs the barrier up before it checks whether anything is
// busy, not after. Checking first and stopping second left the interval
// between them open, and a turn that started in it had its session log
// removed while it was still writing to it.
func (l *Loop) StopEverything(ids []string) func() {
	if l.lifecycle == nil {
		if l.Tasks != nil {
			l.Tasks.StopSession(ids)
		}
		return func() {}
	}
	l.lifecycle.claimAll()
	if l.Tasks != nil {
		l.Tasks.StopSession(ids)
	}
	for _, id := range ids {
		l.ClearSessionState(id)
	}
	return l.lifecycle.releaseAll
}

// SessionsClosing reports whether a delete-all is in progress.
//
// Kept for reporting, not for deciding. Reading it and then acting is the
// check-then-act this whole boundary exists to remove: the answer can stop
// being true between the two. Anything that is about to start a top-level
// turn or create a conversation calls AdmitTopLevel instead, which decides
// and registers in one step.
func (l *Loop) SessionsClosing() bool {
	return l.lifecycle != nil && l.lifecycle.closingAll()
}

// AdmitTopLevel opens an admission window for starting a top-level turn or
// creating a conversation, or reports false if delete-all holds the
// daemon. The caller must call the returned release, and must hold the
// window until the thing it is admitting has committed: a turn until it is
// registered with the turn tracker, a session until it and its first log
// writes exist.
//
// The point is that delete-all cannot pass its busy check or take its
// cleanup snapshot while one of these is open. It either drains the
// admission and sees what it committed, or the admission is refused before
// it can commit. A read of SessionsClosing followed by a commit gives
// neither, because delete-all can claim the daemon in between.
func (l *Loop) AdmitTopLevel() (func(), bool) {
	if l.lifecycle == nil {
		return func() {}, true
	}
	if !l.lifecycle.admitTop() {
		return func() {}, false
	}
	var once sync.Once
	return func() { once.Do(l.lifecycle.admittedTop) }, true
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

// SetProjectDir changes the default project directory — the one a session
// with no directory of its own works in. It no longer touches the
// process's working directory: each turn carries its session's directory
// on the context instead (see SessionDir and tools.WithWorkingDir), which
// is what lets two sessions work in two different projects at once.
func (l *Loop) SetProjectDir(dir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ProjectDir = dir
}

// SessionDir is the directory sessionID's tools work in: the workspace
// stamped on the session, or the daemon's default when it has none.
//
// A session records its workspace at creation and whenever it is moved, so
// this survives a restart — which is the other half of the reason it is
// per-session. Before, reopening yesterday's session put its files
// wherever the daemon happened to be started today.
func (l *Loop) SessionDir(sessionID string) string {
	if sessionID != "" && l.Store != nil {
		if sess, err := l.Store.Get(sessionID); err == nil && sess.Workspace != "" {
			return sess.Workspace
		}
	}
	return l.GetProjectDir()
}

// systemPromptFor is the system prompt a turn in sessionID runs with: the
// daemon-wide one, plus the rules of the project that session is in.
func (l *Loop) systemPromptFor(sessionID string) string {
	if l.WorkspaceRules == nil {
		return l.SystemPrompt
	}
	section := l.WorkspaceRules(l.SessionDir(sessionID))
	if section == "" {
		return l.SystemPrompt
	}
	return l.SystemPrompt + "\n\n" + section
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
