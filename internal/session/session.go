// Package session implements the session store: session metadata plus each
// session's append-only event log, with pub/sub for live consumers (TUI,
// web) and parent/child links for background tasks.
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"localcode/internal/events"
)

type Session struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Visible  bool   `json:"visible"`
	Agent    string `json:"agent,omitempty"`
	Title    string `json:"title,omitempty"`
	// Workspace is the directory this session works in, and it is the
	// session's own: two conversations on one daemon can be in two
	// projects at once. Every relative path and every shell command in a
	// turn resolves against the directory of the session it belongs to
	// (see agent.SessionDir and tools.WithWorkingDir), and it is what the
	// model is told in its prompt.
	//
	// This description used to say the opposite, that the daemon has one
	// live workspace at a time and this is only a record of where the
	// session currently is. That was true until v0.39.0, when the
	// workspace stopped being one os.Chdir for the process and became a
	// property of the session; handleSetWorkspace has taken a session_id
	// ever since. Left as it was, the comment described the mechanism a
	// reader would then go and look for.
	//
	// It is set at creation and moved by SetWorkspace.
	// Empty for sessions created before this field existed.
	Workspace string `json:"workspace,omitempty"`
	// Order is where this session sits in the session panel, when someone
	// has dragged the list into an order of their own. 1-based; zero means
	// "never placed by hand", which sorts above the placed ones so a new
	// session still appears at the top rather than at the bottom of a list
	// that was arranged before it existed.
	Order     int       `json:"order,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// ArchivedAt is when this conversation was put away, or nil while it
	// is active. Archiving is not deleting: everything here is kept, the
	// event log included, and Retrieve puts it back.
	//
	// A pointer rather than a bool, for two reasons. Nil is the zero
	// value, so every meta file written before this field existed loads
	// as an active session and there is no migration; a bare time.Time
	// would defeat omitempty and stamp 0001-01-01 into every one of them.
	// And the archive wants an order of its own, which is when things
	// were put away rather than when they were created.
	//
	// Not Visible, which already means something else: false marks a
	// background task's session. Three places read it that way, including
	// the permission broker looking for a visible ancestor to show an
	// unattended prompt in, so reusing it would make an archived
	// conversation indistinguishable from every task ever run and would
	// leave a child's permission question with nowhere to appear.
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	// Permissions is how this conversation answers permission questions,
	// where it differs from the daemon's defaults. See Permissions.
	Permissions Permissions `json:"permissions,omitempty"`
	// Effort is this conversation's own answer to how hard the model
	// should think, overriding the profile's. Empty means the profile
	// decides, which is what every session starts as.
	//
	// Per session rather than per profile because the question belongs to
	// the work, not to the model: the same model answering "which file is
	// this in" and "why does this deadlock" wants different amounts of
	// reasoning, and one is a profile people would otherwise have to
	// duplicate to get both.
	Effort string `json:"effort,omitempty"`
}

// Permissions is a session's own answer to the four permission switches.
//
// Per session rather than per daemon, for the same reason the workspace
// is: two conversations on one daemon are two projects, and "do not ask
// me about this one" is a thing said about a project. A skip flipped on
// for a throwaway experiment used to silence the prompts in the
// conversation editing something that mattered.
//
// A nil field means this session has not been asked the question and
// follows the daemon's default from config.json. That is what makes the
// default still worth setting, and it is why these are pointers: false
// and unset have to be different, or turning a switch off in one session
// would be indistinguishable from never having touched it.
type Permissions struct {
	// SkipAll allows every prompt that would have asked, the workspace
	// boundary included.
	SkipAll *bool `json:"skip_all,omitempty"`
	// SkipTools allows every tool prompt and leaves the boundary alone,
	// which is the useful middle: work without being interrupted about
	// this project, and still be asked before anything leaves it.
	SkipTools *bool `json:"skip_tools,omitempty"`
	// ReadOutside and WriteOutside allow reads and writes outside this
	// session's workspace without asking.
	ReadOutside  *bool `json:"read_outside,omitempty"`
	WriteOutside *bool `json:"write_outside,omitempty"`
}

// Switch names one of the four, so setting one is a value rather than a
// field name spelled out at each call site.
type Switch string

const (
	SwitchSkipAll      Switch = "skip_all"
	SwitchSkipTools    Switch = "skip_tools"
	SwitchReadOutside  Switch = "read_outside"
	SwitchWriteOutside Switch = "write_outside"
)

// Switches lists them in the order a client should show them: the two
// blanket ones first, then the two that survive the second blanket.
func Switches() []Switch {
	return []Switch{SwitchSkipAll, SwitchSkipTools, SwitchReadOutside, SwitchWriteOutside}
}

// Get returns this session's own answer for one switch, or nil when it
// has none and the daemon's default applies.
func (p Permissions) Get(sw Switch) *bool {
	switch sw {
	case SwitchSkipAll:
		return p.SkipAll
	case SwitchSkipTools:
		return p.SkipTools
	case SwitchReadOutside:
		return p.ReadOutside
	case SwitchWriteOutside:
		return p.WriteOutside
	}
	return nil
}

// set writes one switch. A nil value clears it, which is how a session
// goes back to following the daemon's default rather than pinning the
// same value the default happens to have today.
func (p *Permissions) set(sw Switch, v *bool) bool {
	switch sw {
	case SwitchSkipAll:
		p.SkipAll = v
	case SwitchSkipTools:
		p.SkipTools = v
	case SwitchReadOutside:
		p.ReadOutside = v
	case SwitchWriteOutside:
		p.WriteOutside = v
	default:
		return false
	}
	return true
}

type subscriber struct {
	ch chan events.Event
	// lost is closed the first time an event cannot be handed to this
	// subscriber because its buffer is full.
	//
	// A dropped event used to be the end of the story: the writer moved
	// on, and the reader was never told. Every event is one token of
	// model output, so a client that falls behind for a moment loses the
	// middle of a reply — and then loses turn.done as well, which is what
	// clears the spinner. What that looks like is a message that stops
	// halfway and a turn that never ends.
	//
	// Closing this is how the reader finds out. It ends the stream, and
	// the client reconnects with Last-Event-ID and replays what it missed
	// from the log, which is a machinery that already exists.
	lost     chan struct{}
	lostOnce sync.Once
}

// markLost reports that this subscriber missed an event. Safe to call
// from any number of writers; only the first does anything.
func (s *subscriber) markLost() {
	s.lostOnce.Do(func() { close(s.lost) })
}

type sessionState struct {
	meta      Session
	log       []events.Event
	nextSeq   uint64
	subs      map[int]*subscriber
	nextSubID int
	file      *os.File // nil if not persisted
}

// Store holds all sessions in memory, optionally persisting each session's
// event log to <dir>/<sessionID>.jsonl for crash recovery.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*sessionState
	dir      string // empty = no persistence
}

func NewStore(persistDir string) (*Store, error) {
	if persistDir != "" {
		if err := os.MkdirAll(persistDir, 0o755); err != nil {
			return nil, fmt.Errorf("create session dir: %w", err)
		}
	}
	return &Store{
		sessions: map[string]*sessionState{},
		dir:      persistDir,
	}, nil
}

// CreateSession creates a new session with no recorded workspace. parentID
// is empty for a top-level (user-facing) session, or set when this is a
// background task spawned by another session.
func (s *Store) CreateSession(id, parentID, agent string, visible bool) (*Session, error) {
	return s.CreateSessionIn(id, parentID, agent, "", visible)
}

// CreateSessionIn is CreateSession plus the workspace directory to stamp on
// the session — see Session.Workspace. The daemon uses this so a session
// list can show where each conversation was started.
func (s *Store) CreateSessionIn(id, parentID, agent, workspace string, visible bool) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[id]; exists {
		return nil, fmt.Errorf("session %s already exists", id)
	}

	// Nothing new starts under an archived conversation, and this one
	// check is most of the enforcement.
	//
	// Every way work begins in a session ends up here: a Task spawn, a
	// synchronous delegation, a scheduled run. Each of those could have
	// its own "is it archived" test, and each would be a check-then-act
	// with an interval an archive could land in. This shares the store's
	// mutex with Archive's write, so a child cannot be created under a
	// conversation being archived and an archive cannot slip past a
	// creation already committed. The callers keep their own checks for
	// the message they produce; this is the one that cannot be raced.
	if parentID != "" {
		if parent, ok := s.sessions[parentID]; ok && parent.meta.ArchivedAt != nil {
			return nil, fmt.Errorf("session %s is archived", parentID)
		}
	}

	meta := Session{
		ID:        id,
		ParentID:  parentID,
		Visible:   visible,
		Agent:     agent,
		Workspace: workspace,
		CreatedAt: time.Now().UTC(),
	}

	st := &sessionState{
		meta: meta,
		subs: map[int]*subscriber{},
	}

	if s.dir != "" {
		f, err := os.OpenFile(filepath.Join(s.dir, id+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open session log: %w", err)
		}
		st.file = f
		if err := writeSessionMeta(s.dir, meta); err != nil {
			_ = f.Close()
			return nil, err
		}
	}

	s.sessions[id] = st
	metaCopy := meta
	return &metaCopy, nil
}

// writeSessionMeta persists Session metadata (everything Append's jsonl
// event log doesn't capture — Agent/Title/Visible/ParentID/CreatedAt) to
// <dir>/<id>.meta.json, so a restart can reconstruct the session list and
// its per-session settings, not just replay the event log. Rewritten
// wholesale on every metadata change (CreateSession/SetAgent/SetTitle);
// small enough that this is simpler and safer than patching in place.
func writeSessionMeta(dir string, meta Session) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal session meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, meta.ID+".meta.json"), data, 0o644); err != nil {
		return fmt.Errorf("write session meta: %w", err)
	}
	return nil
}

func (s *Store) Get(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %s not found", id)
	}
	metaCopy := st.meta
	return &metaCopy, nil
}

// SetAgent changes which agent a session sends future messages as —
// e.g. switching a session from "plan" to "build" mid-conversation.
// Message history is untouched; only the agent used for the *next*
// SendMessage call changes, since callers re-read Session.Agent fresh on
// every send rather than caching it.
func (s *Store) SetAgent(sessionID, agent string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	st.meta.Agent = agent
	metaCopy := st.meta
	if s.dir != "" {
		if err := writeSessionMeta(s.dir, metaCopy); err != nil {
			return nil, err
		}
	}
	return &metaCopy, nil
}

// SetTitle renames a session — purely cosmetic (a user-facing label for
// the session picker), doesn't affect resolution or resumption, both of
// which are always by ID.
func (s *Store) SetTitle(sessionID, title string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	st.meta.Title = title
	metaCopy := st.meta
	if s.dir != "" {
		if err := writeSessionMeta(s.dir, metaCopy); err != nil {
			return nil, err
		}
	}
	return &metaCopy, nil
}

// SetWorkspace records dir as the session's current workspace. The daemon
// calls this when the workspace changes while this session is the open
// one, so the session list keeps naming where the conversation actually
// is, and so selecting the session later restores that directory instead
// of the one it happened to be created in.
func (s *Store) SetWorkspace(sessionID, dir string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	st.meta.Workspace = dir
	metaCopy := st.meta
	if s.dir != "" {
		if err := writeSessionMeta(s.dir, metaCopy); err != nil {
			return nil, err
		}
	}
	return &metaCopy, nil
}

// SetPermission records this session's own answer to one permission
// switch, or clears it (v nil) so the daemon's default applies again.
//
// Persisted with the rest of the session's metadata, so reopening a
// conversation reopens it configured the way it was left. That is the
// same promise the workspace makes, and it is the reason these are on
// the session at all: the setting describes the work, not the process.
func (s *Store) SetPermission(sessionID string, sw Switch, v *bool) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	if !st.meta.Permissions.set(sw, v) {
		return nil, fmt.Errorf("unknown permission switch %q", sw)
	}
	metaCopy := st.meta
	if s.dir != "" {
		if err := writeSessionMeta(s.dir, metaCopy); err != nil {
			return nil, err
		}
	}
	return &metaCopy, nil
}

// SetEffort records this conversation's own reasoning level, or clears
// it back to the profile's with "".
func (s *Store) SetEffort(sessionID, effort string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	st.meta.Effort = effort
	metaCopy := st.meta
	if s.dir != "" {
		if err := writeSessionMeta(s.dir, metaCopy); err != nil {
			return nil, err
		}
	}
	return &metaCopy, nil
}

// Delete removes one session from the store and, if persisted, deletes its
// on-disk JSONL log. It does not cascade: see DeleteTree for the operation
// a user's "delete this conversation" actually means.
// Dir is where sessions are persisted, or "" for a store that keeps
// nothing on disk. Exported for the checkpoint sidecar, which lives
// beside the logs and has to go when they do.
func (s *Store) Dir() string { return s.dir }

func (s *Store) Delete(sessionID string) error {
	s.mu.Lock()
	st, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session %s not found", sessionID)
	}
	delete(s.sessions, sessionID)
	dir := s.dir
	if st.file != nil {
		_ = st.file.Close()
	}
	// Everyone still reading this session is told, the same way a
	// subscriber that falls behind is told. Otherwise they sat on a
	// stream that would never produce another event and never end,
	// holding the connection until the client itself gave up.
	for _, sub := range st.subs {
		sub.markLost()
	}
	st.subs = map[int]*subscriber{}
	s.mu.Unlock()

	if dir != "" {
		// The rewind pre-images go with the log they belong to. Left
		// behind they are unreachable — nothing but this session's own
		// events names them — and they are copies of the user's files.
		if err := os.RemoveAll(filepath.Join(dir, sessionID+".checkpoints")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove session checkpoints: %w", err)
		}
		if err := os.Remove(filepath.Join(dir, sessionID+".jsonl")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove session log: %w", err)
		}
		if err := os.Remove(filepath.Join(dir, sessionID+".meta.json")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove session meta: %w", err)
		}
	}
	return nil
}

// DeleteAll removes every session in the store — visible sessions and
// background-task children alike — and their persisted files, if any.
// Unlike Delete, callers that need to refuse this while some session has
// a turn in-flight (see daemon.handleDeleteAllSessions) must check that
// themselves first; DeleteAll itself has no such guard.
func (s *Store) DeleteAll() error {
	s.mu.Lock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.Unlock()

	for _, id := range ids {
		if err := s.Delete(id); err != nil {
			return fmt.Errorf("delete session %s: %w", id, err)
		}
	}
	return nil
}

// Descendants returns every session below sessionID, deepest first.
//
// Deepest first because that is the order they can be removed in: a
// caller walking the result and deleting as it goes never removes a
// parent before the children that name it, so a walk interrupted halfway
// leaves a tree with a root rather than a set of orphans.
func (s *Store) Descendants(sessionID string) []string {
	byParent := map[string][]string{}
	s.mu.Lock()
	for id, st := range s.sessions {
		if st.meta.ParentID != "" {
			byParent[st.meta.ParentID] = append(byParent[st.meta.ParentID], id)
		}
	}
	s.mu.Unlock()

	var out []string
	var walk func(string)
	seen := map[string]bool{sessionID: true}
	walk = func(id string) {
		for _, child := range byParent[id] {
			// A cycle cannot happen through CreateSession, which only
			// ever names an existing parent, but a store rehydrated from
			// files on disk is only as well-formed as the files.
			if seen[child] {
				continue
			}
			seen[child] = true
			walk(child)
			out = append(out, child)
		}
	}
	walk(sessionID)
	return out
}

// DeleteTree removes sessionID and every session below it.
//
// This is what deleting a conversation means. A background task runs in a
// session of its own, invisible and unlisted, and deleting only the one
// the user can see left every one of those behind: a log file and a
// metadata entry per task, unreachable through any list, accumulating one
// per delete. Callers that need to stop the work first must do so before
// calling this; the store only owns the records.
//
// Children go first, so an interruption leaves a reachable tree rather
// than orphans, and a child that has already gone is not an error.
func (s *Store) DeleteTree(sessionID string) error {
	for _, id := range s.Descendants(sessionID) {
		if err := s.Delete(id); err != nil && !strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("delete child session %s: %w", id, err)
		}
	}
	return s.Delete(sessionID)
}

// ListVisible returns all top-level (visible:true) sessions that are not
// archived — i.e. the ones a user picks from when resuming, not
// background tasks and not the ones put away — newest first.
func (s *Store) ListVisible() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Session
	for _, st := range s.sessions {
		if st.meta.Visible && st.meta.ArchivedAt == nil {
			out = append(out, st.meta)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		// Hand-placed sessions keep the position they were dragged to;
		// everything else stays newest-first. Unplaced sessions sort above
		// placed ones (Order 0), so a session created after the list was
		// arranged appears at the top instead of below an arrangement it
		// was never part of.
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// SetOrder places the given sessions in the panel, in the order given.
//
// Every visible session is renumbered, not just the ones named: a partial
// order is one where two sessions can hold the same position, and the
// tie-break behind it is creation time, which is the very thing dragging is
// overriding. Unknown ids are an error rather than a silent skip — a client
// asking to arrange a session that no longer exists is out of date, and its
// idea of the order is not one to write down.
func (s *Store) SetOrder(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	at := make(map[string]int, len(ids))
	for i, id := range ids {
		st, ok := s.sessions[id]
		if !ok {
			return fmt.Errorf("session %s not found", id)
		}
		if st.meta.ArchivedAt != nil {
			// Its own message, because the fix is its own: retrieve it,
			// rather than wonder why a conversation you can see is "not in
			// the session list".
			return fmt.Errorf("session %s is archived", id)
		}
		if !st.meta.Visible {
			return fmt.Errorf("session %s is not in the session list", id)
		}
		at[id] = i + 1
	}

	// Anything visible the client did not mention keeps its relative place
	// after the ones it did, rather than jumping to the top: it is a
	// session that appeared between the drag and this request, and moving
	// it under the user's hands is the one thing a reorder must not do.
	var rest []Session
	for id, st := range s.sessions {
		if st.meta.Visible && st.meta.ArchivedAt == nil && at[id] == 0 {
			rest = append(rest, st.meta)
		}
	}
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].Order != rest[j].Order {
			return rest[i].Order < rest[j].Order
		}
		return rest[i].CreatedAt.After(rest[j].CreatedAt)
	})
	for i, meta := range rest {
		at[meta.ID] = len(ids) + i + 1
	}

	for id, pos := range at {
		st := s.sessions[id]
		st.meta.Order = pos
		if s.dir != "" {
			if err := writeSessionMeta(s.dir, st.meta); err != nil {
				return err
			}
		}
	}
	return nil
}

// AllSessions returns every session regardless of Visible — unlike
// ListVisible, this also includes background-task child sessions. Used by
// callers that need to rehydrate every session's in-memory state (e.g.
// agent.Loop's conversation history) after a restart, not just the ones a
// user would pick from.
func (s *Store) AllSessions() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Session, 0, len(s.sessions))
	for _, st := range s.sessions {
		out = append(out, st.meta)
	}
	return out
}

// Children returns sessions spawned by parentID (background tasks).
func (s *Store) Children(parentID string) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Session
	for _, st := range s.sessions {
		if st.meta.ParentID == parentID {
			out = append(out, st.meta)
		}
	}
	return out
}

// Append adds an event to the session's log, persists it if configured, and
// fans it out to live subscribers. Returns the stored event with its seq.
func (s *Store) Append(sessionID string, typ events.Type, data map[string]any) (events.Event, error) {
	s.mu.Lock()
	st, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return events.Event{}, fmt.Errorf("session %s not found", sessionID)
	}

	st.nextSeq++
	ev := events.Event{
		Seq:       st.nextSeq,
		Session:   sessionID,
		Type:      typ,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}
	st.log = append(st.log, ev)

	// Delivered while still holding the lock. The sends are
	// non-blocking, so this cannot stall behind a slow reader, and it
	// removes the window in which unsubscribe could close a channel
	// between the snapshot and the send — a send on a closed channel is a
	// panic, and this one would take the daemon down.
	for _, sub := range st.subs {
		select {
		case sub.ch <- ev:
		default:
			// The reader is behind. Still do not block the writer — a
			// stalled browser tab must not hold up the model — but do not
			// pretend nothing happened either: this event is gone for
			// this subscriber, and every event carries meaning it cannot
			// reconstruct. Telling it costs one reconnect and a replay
			// from the log; not telling it costs the rest of the turn.
			sub.markLost()
		}
	}
	// Written under the same lock that handed out the seq, which is what
	// makes the file's order the log's order.
	//
	// Outside it, two appends could take seq N and N+1 and then reach the
	// file in the opposite order — routine rather than exotic: a
	// background task writes task.status to its parent while that parent's
	// turn is streaming deltas. Measured at ~55 inverted lines per 200
	// concurrent appends. Restoring reads the file in file order, so the
	// in-memory log came back out of sequence and every seq-based lookup
	// after it — `since=`, Last-Event-ID resume, the tail cut — was
	// working against an unsorted list.
	//
	// It also closes the window where Delete could close the file between
	// the unlock and the write, since Delete takes this same lock.
	//
	// The cost is a small buffered write inside the lock. The fanout to
	// subscribers above already runs here, and nothing in this function
	// waits on anything but memory and one Write syscall.
	if st.file != nil {
		if line, err := json.Marshal(ev); err == nil {
			_, _ = st.file.Write(append(line, '\n'))
		}
	}
	s.mu.Unlock()

	return ev, nil
}

// Broadcast fans an event out to a session's live subscribers without
// recording it: no seq, no place in the log, nothing written to disk.
//
// For things that are only true right now — the live tokens-per-second
// readout is the case this exists for. Such an event fires once a second
// during a long generation, and appending it would grow the log (and the
// file behind it) by hundreds of entries that say nothing about what was
// said, and would replay as a burst of stale numbers on reconnect.
//
// Seq stays 0, which is how the SSE layer knows not to give it an `id:`
// line: a transient event must never become the Last-Event-ID a browser
// resumes the real log from.
func (s *Store) Broadcast(sessionID string, typ events.Type, data map[string]any) {
	s.mu.Lock()
	st, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return
	}
	ev := events.Event{
		Session:   sessionID,
		Type:      typ,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}
	for _, sub := range st.subs {
		select {
		case sub.ch <- ev:
		default:
			// Dropping a transient event costs nothing and must NOT count
			// as falling behind: another one is along in a second, it
			// carries no history, and tearing down a working stream over
			// a missed tokens-per-second reading would be absurd.
		}
	}
	s.mu.Unlock()
}

// Events returns all events with seq > since, for catch-up on
// (re)connection.
func (s *Store) Events(sessionID string, since uint64) ([]events.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	var out []events.Event
	for _, ev := range st.log {
		if ev.Seq > since {
			out = append(out, ev)
		}
	}
	return out, nil
}

// Subscribe returns a channel of live events, a channel closed if this
// subscriber ever misses one, and an unsubscribe func.
//
// Callers should first call Events(since) to catch up, then Subscribe to
// avoid missing events in the gap.
//
// The middle return value is the part that has to be honoured. Delivery
// is best effort — a reader that stops reading must never stall the
// model — so an event can be dropped, and a dropped event is not
// recoverable from the stream itself. When that channel closes, the only
// correct response is to stop trusting this stream: drop it, and catch
// up with Events(since) on a fresh one. Ignoring it means showing a
// conversation with a hole in it, and quite possibly missing the event
// that says the turn ended.
func (s *Store) Subscribe(sessionID string) (<-chan events.Event, <-chan struct{}, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, nil, nil, fmt.Errorf("session %s not found", sessionID)
	}

	id := st.nextSubID
	st.nextSubID++
	// A generous buffer, because every token of model output is one event
	// and 64 of them is well under a second of a local model talking. The
	// lost signal below is the correctness guarantee; this is what keeps
	// it from firing during ordinary use.
	sub := &subscriber{ch: make(chan events.Event, 4096), lost: make(chan struct{})}
	st.subs[id] = sub

	unsub := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(st.subs, id)
		close(sub.ch)
	}
	return sub.ch, sub.lost, unsub, nil
}

// LoadAllFromDisk restores every session found in dir (one <id>.meta.json +
// <id>.jsonl pair each) into a fresh, persisting Store — e.g. at daemon
// startup, so a restart doesn't wipe the session list the way a bare
// NewStore(dir) would. A directory with no sessions yet (or that doesn't
// exist) just yields an empty, working Store, same as NewStore.
//
// A <id>.jsonl with no matching <id>.meta.json (a log from before this
// sidecar file existed, or one that failed to write) is skipped with a
// warning appended to the returned slice rather than failing the whole
// restore — one corrupt session shouldn't take every other session down
// with it.
// Reload replaces what this store holds for one session with what is on
// disk, and tells anyone streaming it to reconnect.
//
// For the moment a session changes hands between two processes. A
// daemon handing itself to a newer version keeps writing the sessions
// whose turns it is still finishing, and the new daemon loaded those
// sessions at startup, before the last of that writing happened: its
// copy of the log is short and its next sequence number is stale, and
// the first event it appended would collide with one already on disk.
// So the new daemon does not touch such a session until the old one has
// released it, and then reads it again rather than trusting the copy.
//
// Subscribers are told the way a lagging one is told, because the log
// they were reading is being replaced under them; they reconnect from
// their last sequence and replay from the fresh copy.
func (s *Store) Reload(id string) error {
	if s.dir == "" {
		return nil
	}
	s.mu.Lock()
	if st, ok := s.sessions[id]; ok {
		if st.file != nil {
			_ = st.file.Close()
		}
		for _, sub := range st.subs {
			sub.markLost()
		}
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	return s.restoreOne(s.dir, id)
}

// EndAllStreams tells every subscriber of every session to reconnect.
//
// A process that has stopped serving cannot hold streams open, and the
// streams cannot simply be cut: a client reads a cut connection as the
// daemon being gone and shows a reply that stops halfway. Marking each
// lost is the signal the clients already handle — reconnect from the
// last sequence and replay — and once the new daemon is on the port,
// that reconnect lands on it.
func (s *Store) EndAllStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.sessions {
		for _, sub := range st.subs {
			sub.markLost()
		}
		st.subs = map[int]*subscriber{}
	}
}

// Close releases every session's log file and ends every stream.
//
// A daemon never needs this — its files live as long as it does — but a
// test does: on Windows an open file cannot be deleted, so a temporary
// directory holding a store's logs cannot be removed until the store has
// let go of them.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.sessions {
		for _, sub := range st.subs {
			sub.markLost()
		}
		st.subs = map[int]*subscriber{}
		if st.file != nil {
			_ = st.file.Close()
			st.file = nil
		}
	}
}

func LoadAllFromDisk(dir string) (*Store, []error, error) {
	s, err := NewStore(dir)
	if err != nil {
		return nil, nil, err
	}
	if dir == "" {
		return s, nil, nil
	}

	metaFiles, err := filepath.Glob(filepath.Join(dir, "*.meta.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("glob session metadata: %w", err)
	}

	var warnings []error
	for _, metaPath := range metaFiles {
		id := strings.TrimSuffix(filepath.Base(metaPath), ".meta.json")
		if err := s.restoreOne(dir, id); err != nil {
			warnings = append(warnings, fmt.Errorf("session %s: %w", id, err))
		}
	}
	return s, warnings, nil
}

// restoreOne loads one session's metadata + event log into s, opening its
// jsonl file in append mode so future Append calls continue the same file.
func (s *Store) restoreOne(dir, id string) error {
	metaData, err := os.ReadFile(filepath.Join(dir, id+".meta.json"))
	if err != nil {
		return fmt.Errorf("read meta: %w", err)
	}
	var meta Session
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return fmt.Errorf("parse meta: %w", err)
	}
	if meta.ID == "" {
		meta.ID = id
	}

	f, err := os.OpenFile(filepath.Join(dir, id+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open session log: %w", err)
	}

	st := &sessionState{
		meta: meta,
		subs: map[int]*subscriber{},
		file: f,
	}

	if logData, err := os.ReadFile(filepath.Join(dir, id+".jsonl")); err == nil {
		// Read with a Reader, not a Scanner. A Scanner has a maximum line
		// length and nothing here truncates tool output, so one `cat` of a
		// large file writes a line past any cap that could be chosen — and
		// a Scanner reports that by stopping, which is indistinguishable
		// from reaching the end of the file.
		//
		// The consequence was not a truncated transcript but a corrupted
		// log. Every later event was dropped, nextSeq was restored below
		// the highest seq actually in the file, and appends after the
		// restart handed out numbers the file already contained. Two
		// events with one seq breaks `since=` replay and Last-Event-ID
		// resume for that session permanently, and nothing reported a
		// problem: restore returned no error at all.
		r := bufio.NewReader(bytes.NewReader(logData))
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				var ev events.Event
				// A line that does not parse is skipped, not fatal: the
				// last line of a log whose write was interrupted is a
				// partial one, and losing it is right — losing everything
				// after it is not.
				if json.Unmarshal(bytes.TrimSpace(line), &ev) == nil {
					st.log = append(st.log, ev)
					if ev.Seq > st.nextSeq {
						st.nextSeq = ev.Seq
					}
				}
			}
			if err != nil {
				break // io.EOF, or a final line with no newline
			}
		}
	} else if !os.IsNotExist(err) {
		_ = f.Close()
		return fmt.Errorf("read session log: %w", err)
	}

	s.mu.Lock()
	s.sessions[id] = st
	s.mu.Unlock()
	return nil
}

// TailSince returns the seq to replay from so a client sees roughly the
// last n events, starting at a turn boundary.
//
// Opening a long conversation used to replay every event in it. The
// daemon is not what makes that slow — 7,680 events left it in 47ms —
// but the client then has to build the whole transcript, which measured
// at 751ms in a headless DOM and is worse in a real one, where every
// growing markdown re-render costs a reparse and a relayout. Since none
// of that history is on screen when the panel opens, the cheapest way to
// show the end of a conversation is not to send the beginning.
//
// The boundary matters as much as the count. Cutting at an arbitrary
// event can land between a tool call and its result, or part-way through
// a reply, and the transcript then opens on a fragment with a spinner
// that never resolves. So the cut is moved back to the most recent user
// message at or before it: the tail begins where someone asked
// something, which is where a conversation reads from.
//
// Bounded at 2n, because one turn can be longer than the whole window
// and "start at a turn boundary" must not become "send everything".
//
// n counts entries the reader will actually see, which is not the same as
// entries in the log. Nearly every event in a busy log is one
// message.part.delta — one fragment of streamed text, a few characters
// each — and every one of them was counted. So a single 2,000-token reply
// was 2,000 events, a tail of 400 landed inside it, and re-opening a
// conversation showed the last paragraph of the last answer and nothing
// else: the whole conversation above it, gone. Deltas are excluded from
// the count here and dropped from the replay in the daemon (they are
// superseded by the message.part.end that carries the same text whole), so
// n now means roughly what a reader would call n messages.
func (s *Store) TailSince(sessionID string, n int) (uint64, error) {
	if n <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return 0, fmt.Errorf("session %s not found", sessionID)
	}

	// The positions of the events that count, oldest first.
	counted := make([]int, 0, len(st.log))
	for i, ev := range st.log {
		if ev.Type != events.TypeMessagePartDelta {
			counted = append(counted, i)
		}
	}
	if len(counted) <= n {
		return 0, nil
	}

	start := counted[len(counted)-n]
	limitIdx := len(counted) - 2*n
	if limitIdx < 0 {
		limitIdx = 0
	}
	for i := len(counted) - n; i >= limitIdx; i-- {
		if st.log[counted[i]].Type == events.TypeUserMessage {
			start = counted[i]
			break
		}
	}
	if start == 0 {
		return 0, nil
	}
	// The seq *before* the first event to send: Events(since) is
	// exclusive, and this is fed straight to it.
	return st.log[start-1].Seq, nil
}

// Archiving.
//
// A shelf, not a bin. Everything a session has is kept: its title, its
// workspace, its permissions, its effort, its place in the list, its event
// log, the open file handle and the subscribers reading it. What changes is
// that it leaves the list, and that nothing new starts in it — which is
// enforced under this mutex by CreateSessionIn, not by a test each caller
// remembers to make.
//
// Appending still works, deliberately. A background task that outlives the
// archive still writes its status, a schedule still records that it was
// missed, and a client reading the transcript keeps reading it. The store
// refuses to start work, never to record what happened.

// Archive puts a conversation away and returns it. Archiving one that is
// already archived keeps the first timestamp and is not an error: two
// clients pressing the button is not a conflict worth an error page.
func (s *Store) Archive(sessionID string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	// A task's session is in no list, so archiving it hides nothing, and
	// its parent's turn is waiting on it.
	if !st.meta.Visible {
		return nil, fmt.Errorf("session %s is a background task, not a conversation", sessionID)
	}
	if st.meta.ArchivedAt != nil {
		metaCopy := st.meta
		return &metaCopy, nil
	}
	now := time.Now().UTC()
	st.meta.ArchivedAt = &now
	metaCopy := st.meta
	if s.dir != "" {
		if err := writeSessionMeta(s.dir, metaCopy); err != nil {
			st.meta.ArchivedAt = nil // the file is the record; do not claim a write that failed
			return nil, err
		}
	}
	return &metaCopy, nil
}

// Retrieve brings a conversation back and returns it. Retrieving one that
// is not archived is a no-op success, and leaves the order alone.
//
// The rank comes back, not the number. A session archived from third place
// returns to third place if the list has not moved; if it has, it lands as
// close as the remaining information allows, because the number it was
// holding may now belong to something else. Exact would require recording
// what the whole list looked like at archive time, which is a snapshot that
// is wrong the moment anything else is dragged.
func (s *Store) Retrieve(sessionID string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	if st.meta.ArchivedAt == nil {
		metaCopy := st.meta
		return &metaCopy, nil
	}
	want := st.meta.Order
	was := st.meta.ArchivedAt
	st.meta.ArchivedAt = nil

	// The active list as it now stands, without this one, in list order.
	active := make([]Session, 0, len(s.sessions))
	for id, other := range s.sessions {
		if id != sessionID && other.meta.Visible && other.meta.ArchivedAt == nil {
			active = append(active, other.meta)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].Order != active[j].Order {
			return active[i].Order < active[j].Order
		}
		return active[i].CreatedAt.After(active[j].CreatedAt)
	})

	// Order 0 means it was never placed by hand, and those sort to the
	// top, so that is where it goes back.
	at := max(min(want, len(active)+1), 1)
	if want == 0 {
		at = 1
	}
	order := make([]string, 0, len(active)+1)
	for i := range len(active) + 1 {
		if i == at-1 {
			order = append(order, sessionID)
		}
		if i < len(active) {
			order = append(order, active[i].ID)
		}
	}
	if err := s.renumberLocked(order); err != nil {
		// A write failed partway. Put the flag back rather than report a
		// session as retrieved when its meta file may still say otherwise:
		// the file is the record, and the next restart reads it.
		st.meta.ArchivedAt = was
		return nil, err
	}
	metaCopy := s.sessions[sessionID].meta
	return &metaCopy, nil
}

// renumberLocked writes a dense 1..N order over the ids given, which the
// caller has already established are exactly the active sessions. Factored
// out of SetOrder so "dense over the active list" is defined once and
// Retrieve cannot drift from it.
func (s *Store) renumberLocked(order []string) error {
	for i, id := range order {
		st, ok := s.sessions[id]
		if !ok {
			continue
		}
		st.meta.Order = i + 1
		if s.dir != "" {
			if err := writeSessionMeta(s.dir, st.meta); err != nil {
				return err
			}
		}
	}
	return nil
}

// ListArchived returns the archived conversations, most recently put away
// first. Order is the active list's arrangement and means nothing here, so
// the archive sorts by when things were shelved.
func (s *Store) ListArchived() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Session
	for _, st := range s.sessions {
		if st.meta.Visible && st.meta.ArchivedAt != nil {
			out = append(out, st.meta)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ArchivedAt.Equal(*out[j].ArchivedAt) {
			return out[i].ArchivedAt.After(*out[j].ArchivedAt)
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}
